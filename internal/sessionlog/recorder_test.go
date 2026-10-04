package sessionlog

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRecorderWritesReadableTranscriptAndSanitizesMetadata(t *testing.T) {
	directory := t.TempDir()
	recorder, err := New(Options{Directory: directory, Label: "router\n\x1b[31m one", Kind: "SSH\rsession"})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(recorder.Path()) {
		t.Fatalf("Path() is not absolute: %q", recorder.Path())
	}
	if err := recorder.Write([]byte("ready \x1b[32m✓\x1b[0m\rready\n")); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close("manual stop\nreason"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if strings.ContainsRune(log, '\x1b') || strings.Contains(log, "\r") {
		t.Fatalf("terminal control bytes leaked into transcript: %q", log)
	}
	if !strings.Contains(log, "Session: router  [31m one\n") || !strings.Contains(log, "Type: SSH session\n") {
		t.Fatalf("metadata was not sanitized as expected: %q", log)
	}
	if !strings.Contains(log, "ready ✓\n") || !strings.Contains(log, "Recording ended: manual stop reason at ") {
		t.Fatalf("transcript is missing output or footer: %q", log)
	}
}

func TestRecorderRotatesWithoutExceedingEitherCap(t *testing.T) {
	directory := t.TempDir()
	const maxFile = 1024
	const maxTotal = 64 * 1024
	recorder, err := New(Options{
		Directory:     directory,
		Label:         "edge-router",
		Kind:          "SSH",
		MaxFileBytes:  maxFile,
		MaxTotalBytes: maxTotal,
	})
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&output, "%03d %s\n", i, strings.Repeat("output-", 10))
	}
	if err := recorder.Write([]byte(output.String())); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close("session ended"); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(directory, "session-*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 2 {
		t.Fatalf("expected multiple transcript segments, found %d", len(files))
	}
	var total int64
	var transcriptBody bytes.Buffer
	var expectedBody strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&expectedBody, "%03d %s\n", i, strings.Repeat("output-", 10))
	}
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > maxFile {
			t.Errorf("segment %q has %d bytes, limit is %d", path, info.Size(), maxFile)
		}
		total += info.Size()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.Valid(data) {
			t.Errorf("segment %q is not valid UTF-8", path)
		}
		headerEnd := bytes.Index(data, []byte("\n\n"))
		footerStart := bytes.LastIndex(data, []byte("\n[Recording ended:"))
		if headerEnd < 0 || footerStart < headerEnd+2 {
			t.Fatalf("segment %q is missing a header or footer", path)
		}
		transcriptBody.Write(data[headerEnd+2 : footerStart])
	}
	if total > maxTotal {
		t.Fatalf("transcript uses %d bytes, total limit is %d", total, maxTotal)
	}
	if got, want := transcriptBody.String(), expectedBody.String(); got != want {
		t.Fatalf("rotated transcript body differs from accepted output\ngot:  %q\nwant: %q", got, want)
	}
}

func TestNewRejectsSegmentLimitTooSmallForUTF8RuneBeforeCreatingDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "logs")
	_, err := New(Options{
		Directory:     directory,
		Label:         "small",
		Kind:          "SSH",
		MaxFileBytes:  1,
		MaxTotalBytes: 16 * 1024,
	})
	if !errors.Is(err, ErrSizeLimit) {
		t.Fatalf("New() error = %v, want ErrSizeLimit", err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("New() created the log directory for invalid size caps: stat error = %v", err)
	}
}

func TestTranscriptWriterRejectsLessThanOneRunePerSegment(t *testing.T) {
	directory := t.TempDir()
	started := time.Date(2025, time.October, 1, 12, 0, 0, 0, time.UTC)
	options := Options{Label: "small", Kind: "SSH", MaxTotalBytes: 16 * 1024}
	headerSize := int64(len((&transcriptWriter{options: options, started: started}).header(1)))
	options.MaxFileBytes = headerSize + footerReserveBytes + 1

	_, err := newTranscriptWriter(directory, "session-one-byte-limit", options, started)
	if !errors.Is(err, ErrSizeLimit) {
		t.Fatalf("newTranscriptWriter() error = %v, want ErrSizeLimit", err)
	}
	files, err := filepath.Glob(filepath.Join(directory, "session-one-byte-limit-*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("writer created %d segment files for a one-byte output limit", len(files))
	}
}

func TestTranscriptWriterDoesNotCreateEmptySegmentForIncompleteRune(t *testing.T) {
	directory := t.TempDir()
	started := time.Date(2025, time.October, 1, 12, 0, 0, 0, time.UTC)
	options := Options{Label: "small", Kind: "SSH", MaxTotalBytes: 64 * 1024}
	headerSize := int64(len((&transcriptWriter{options: options, started: started}).header(1)))
	options.MaxFileBytes = headerSize + footerReserveBytes + utf8.UTFMax

	writer, err := newTranscriptWriter(directory, "session-small-limit", options, started)
	if err != nil {
		t.Fatal(err)
	}
	footer := writer.footer("continued in segment 002")
	nextHeader := writer.header(2)
	writer.options.MaxTotalBytes = writer.totalSize + utf8.UTFMax + int64(len(footer)+len(nextHeader)) + footerReserveBytes + 2

	if err := writer.writeOutput([]byte("abcd界")); !errors.Is(err, ErrSizeLimit) {
		t.Fatalf("writeOutput() error = %v, want ErrSizeLimit", err)
	}
	files, err := filepath.Glob(filepath.Join(directory, "session-small-limit-*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("writer created %d segment files without enough output capacity, want only the initial segment", len(files))
	}
}

func TestRecorderReportsTotalLimitAndPreservesExistingFiles(t *testing.T) {
	directory := t.TempDir()
	existingPath := filepath.Join(directory, "keep.txt")
	if err := os.WriteFile(existingPath, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	callback := make(chan error, 1)
	recorder, err := New(Options{
		Directory:     directory,
		Label:         "large-output",
		Kind:          "SSH",
		MaxFileBytes:  1024,
		MaxTotalBytes: 1024,
		OnError:       func(err error) { callback <- err },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Write([]byte(strings.Repeat("界", 1000) + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close("session ended"); !errors.Is(err, ErrSizeLimit) {
		t.Fatalf("Close() error = %v, want ErrSizeLimit", err)
	}
	select {
	case err := <-callback:
		if !errors.Is(err, ErrSizeLimit) {
			t.Fatalf("OnError() error = %v, want ErrSizeLimit", err)
		}
	case <-time.After(time.Second):
		t.Fatal("size-limit failure was not reported")
	}
	kept, err := os.ReadFile(existingPath)
	if err != nil || string(kept) != "keep me" {
		t.Fatalf("existing file changed: contents = %q, error = %v", kept, err)
	}
	info, err := os.Stat(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 1024 {
		t.Fatalf("transcript exceeded total cap: %d bytes", info.Size())
	}
	data, err := os.ReadFile(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "stopped at the configured size limit") {
		t.Fatalf("size-limit footer missing: %q", data)
	}
	footerStart := bytes.LastIndex(data, []byte("\n[Recording ended:"))
	if footerStart < 0 || len(data)-footerStart > int(footerReserveBytes) {
		t.Fatalf("Unicode footer exceeded reserved bound: %d bytes", len(data)-footerStart)
	}
}

func TestRecorderUnicodeCloseReasonFitsFooterReservation(t *testing.T) {
	recorder, err := New(Options{Directory: t.TempDir(), Label: "unicode-footer"})
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(strings.Repeat("界", 128)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}
	footerStart := bytes.LastIndex(data, []byte("\n[Recording ended:"))
	if footerStart < 0 || len(data)-footerStart > int(footerReserveBytes) {
		t.Fatalf("Unicode footer exceeded reserved bound: %d bytes", len(data)-footerStart)
	}
	if !utf8.Valid(data) {
		t.Fatal("Unicode close reason produced invalid UTF-8")
	}
}

func TestRecorderQueueIsBoundedCopiesBytesAndReportsOnce(t *testing.T) {
	var callbackCount atomic.Int32
	callback := make(chan error, 2)
	recorder := &Recorder{
		queue:       make(chan []byte, queueChunks),
		closeReason: "session ended",
		options: Options{OnError: func(err error) {
			callbackCount.Add(1)
			callback <- err
		}},
	}
	input := []byte("copy me")
	if err := recorder.Write(input); err != nil {
		t.Fatal(err)
	}
	input[0] = 'X'
	if got := string(<-recorder.queue); got != "copy me" {
		t.Fatalf("queued output = %q, want copied bytes", got)
	}
	if err := recorder.Write(make([]byte, (queueChunks+1)*chunkBytes)); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("Write() error = %v, want ErrQueueFull", err)
	}
	select {
	case err := <-callback:
		if !errors.Is(err, ErrQueueFull) {
			t.Fatalf("OnError() error = %v, want ErrQueueFull", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queue-full failure was not reported")
	}
	if got := callbackCount.Load(); got != 1 {
		t.Fatalf("OnError called %d times, want once", got)
	}
}

func TestRecorderCloseDrainsAcceptedConcurrentWrites(t *testing.T) {
	recorder, err := New(Options{Directory: t.TempDir(), Label: "concurrent"})
	if err != nil {
		t.Fatal(err)
	}
	const writers = 32
	var successful atomic.Int32
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := 0; i < writers; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			<-start
			if err := recorder.Write([]byte(fmt.Sprintf("record-%02d\n", i))); err == nil {
				successful.Add(1)
			} else if !errors.Is(err, ErrClosed) {
				t.Errorf("Write() error = %v", err)
			}
		}(i)
	}
	close(start)
	closeDone := make(chan error, 1)
	go func() { closeDone <- recorder.Close("concurrent close") }()
	workers.Wait()
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}
	count := strings.Count(string(data), "record-")
	if count != int(successful.Load()) {
		t.Fatalf("transcript contains %d accepted records, Write accepted %d", count, successful.Load())
	}
}

func TestRecorderCloseReturnsWriterErrors(t *testing.T) {
	callback := make(chan error, 1)
	options := Options{
		Directory:     t.TempDir(),
		Label:         "close-error",
		MaxFileBytes:  1024,
		MaxTotalBytes: 4096,
		OnError:       func(err error) { callback <- err },
	}
	started := time.Now().UTC()
	prefix, err := uniqueNamePrefix(options.Label, started)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := newTranscriptWriter(options.Directory, prefix, options, started)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.file.Close(); err != nil {
		t.Fatal(err)
	}
	recorder := &Recorder{
		queue:       make(chan []byte),
		done:        make(chan struct{}),
		closed:      true,
		closeReason: "session ended",
		options:     options,
	}
	close(recorder.queue)
	go recorder.run(writer)
	<-recorder.done
	if err := recorder.Close("session ended"); err == nil {
		t.Fatal("Close() returned nil after transcript footer and file close failed")
	}
	select {
	case err := <-callback:
		if err == nil {
			t.Fatal("OnError received nil after writer failure")
		}
	case <-time.After(time.Second):
		t.Fatal("writer failure was not reported")
	}
}

func TestCreateUniqueSegmentDoesNotOverwriteCollision(t *testing.T) {
	directory := t.TempDir()
	path := segmentPath(directory, "session-existing", 1)
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, createdPath, newPrefix, err := createUniqueSegment(directory, "session-existing", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if createdPath == path || newPrefix == "session-existing" {
		t.Fatalf("collision reused existing name: %q", createdPath)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep" {
		t.Fatalf("collision changed existing file: %q, %v", data, err)
	}
}
