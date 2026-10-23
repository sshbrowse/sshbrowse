package sessionlog

import (
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

func TestDefaultDirectoryUsesHomeLogsRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DefaultDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "SSHBrowse", "logs"); got != want {
		t.Fatalf("DefaultDirectory() = %q, want %q", got, want)
	}
}

func TestNewCreatesLocalDayDirectoryAndDefaultsLabel(t *testing.T) {
	root := filepath.Join(t.TempDir(), "logs")
	startedBefore := time.Now()
	recorder, err := New(Options{Directory: root, Kind: "Local"})
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close("test"); err != nil {
		t.Fatal(err)
	}
	startedAfter := time.Now()
	if !filepath.IsAbs(recorder.Path()) {
		t.Fatalf("Path() is not absolute: %q", recorder.Path())
	}
	dayDirectory := filepath.Base(filepath.Dir(recorder.Path()))
	if dayDirectory != startedBefore.Format("2006-01-02") && dayDirectory != startedAfter.Format("2006-01-02") {
		t.Fatalf("log directory %q is not the local recording start day", dayDirectory)
	}
	name := filepath.Base(recorder.Path())
	if !strings.HasPrefix(name, "local-"+dayDirectory+"_") || !strings.HasSuffix(name, ".txt") {
		t.Fatalf("transcript name = %q, want local-YYYY-MM-DD_HH-MM-SS.txt", name)
	}
}

func TestCreateUniqueFileUsesLocalStartTimeAndNumericCollisions(t *testing.T) {
	directory := t.TempDir()
	started := time.Date(2025, time.October, 2, 0, 15, 16, 0, time.FixedZone("UTC-7", -7*60*60))
	wantPath := filepath.Join(directory, "edge.router-2025-10-02_00-15-16.txt")
	first, firstPath, err := createUniqueFile(directory, "Edge.Router", started)
	if err != nil {
		t.Fatal(err)
	}
	if firstPath != wantPath {
		t.Fatalf("first path = %q, want %q", firstPath, wantPath)
	}
	if _, err := first.WriteString("keep"); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, secondPath, err := createUniqueFile(directory, "Edge.Router", started)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	third, thirdPath, err := createUniqueFile(directory, "Edge.Router", started)
	if err != nil {
		t.Fatal(err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
	if secondPath != filepath.Join(directory, "edge.router-2025-10-02_00-15-16-1.txt") ||
		thirdPath != filepath.Join(directory, "edge.router-2025-10-02_00-15-16-2.txt") {
		t.Fatalf("collision paths = %q and %q", secondPath, thirdPath)
	}
	data, err := os.ReadFile(wantPath)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing transcript changed: contents = %q, error = %v", data, err)
	}
}

func TestCreateUniqueFileConcurrentCollisionsAreUnique(t *testing.T) {
	const count = 32
	directory := t.TempDir()
	started := time.Date(2025, time.October, 2, 0, 15, 16, 0, time.FixedZone("UTC-7", -7*60*60))
	paths := make(chan string, count)
	var workers sync.WaitGroup
	for i := 0; i < count; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			file, path, err := createUniqueFile(directory, "edge-router", started)
			if err != nil {
				t.Errorf("createUniqueFile() error = %v", err)
				return
			}
			if err := file.Close(); err != nil {
				t.Errorf("Close() error = %v", err)
			}
			paths <- path
		}()
	}
	workers.Wait()
	close(paths)

	seen := make(map[string]bool, count)
	for path := range paths {
		if seen[path] {
			t.Errorf("multiple recordings received path %q", path)
		}
		seen[path] = true
	}
	if len(seen) != count {
		t.Fatalf("created %d unique paths, want %d", len(seen), count)
	}
}

func TestRecorderWritesReadableTranscriptAndSanitizesMetadata(t *testing.T) {
	recorder, err := New(Options{
		Directory: t.TempDir(),
		Label:     "router\n\x1b[31m one",
		Kind:      "SSH\rsession",
	})
	if err != nil {
		t.Fatal(err)
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
	if !strings.Contains(log, "Started: ") || !strings.Contains(log, "Session: router  [31m one\n") || !strings.Contains(log, "Type: SSH session\n") {
		t.Fatalf("header metadata was not written as expected: %q", log)
	}
	if !strings.Contains(log, "ready ✓\n") || !strings.Contains(log, "Recording ended: manual stop reason at ") {
		t.Fatalf("transcript is missing output or footer: %q", log)
	}
	if !utf8.Valid(data) {
		t.Fatal("transcript is not valid UTF-8")
	}
}

func TestRecorderStopsAtPrivateFileLimitWithUTF8FooterAndCallback(t *testing.T) {
	directory := t.TempDir()
	started := time.Now()
	options := Options{
		Label: "size-limit",
		Kind:  "SSH",
	}
	headerSize := int64(len((&transcriptWriter{options: options, started: started}).header()))
	limit := headerSize + footerReserveBytes + 4
	writer, err := newTranscriptWriter(directory, options, started, limit)
	if err != nil {
		t.Fatal(err)
	}
	callback := make(chan error, 1)
	options.OnError = func(err error) { callback <- err }
	recorder := &Recorder{
		queue:       make(chan []byte, queueChunks),
		done:        make(chan struct{}),
		path:        writer.path(),
		closeReason: "session ended",
		options:     options,
	}
	go recorder.run(writer)
	if err := recorder.Write([]byte("a界b\n")); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close("manual stop"); !errors.Is(err, ErrSizeLimit) {
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
	data, err := os.ReadFile(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) > limit {
		t.Fatalf("transcript has %d bytes, exceeding limit %d", len(data), limit)
	}
	if !utf8.Valid(data) {
		t.Fatal("size-limited transcript is not valid UTF-8")
	}
	if !strings.Contains(string(data), "\na界\n") {
		t.Fatalf("expected complete UTF-8 output before the limit: %q", data)
	}
	if !strings.Contains(string(data), "Recording ended: stopped at the file size limit at ") {
		t.Fatalf("size-limit footer missing: %q", data)
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
	accepted := make(chan string, writers)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := 0; i < writers; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			<-start
			record := fmt.Sprintf("record-%02d", i)
			if err := recorder.Write([]byte(record + "\n")); err == nil {
				accepted <- record
			} else if !errors.Is(err, ErrClosed) {
				t.Errorf("Write() error = %v", err)
			}
		}(i)
	}
	close(start)
	// Let one concurrent writer succeed before Close can reject the rest.
	var firstAccepted string
	select {
	case firstAccepted = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("no concurrent write was accepted before close")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- recorder.Close("concurrent close") }()
	workers.Wait()
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	close(accepted)
	wantRecords := map[string]struct{}{firstAccepted: {}}
	for record := range accepted {
		wantRecords[record] = struct{}{}
	}
	data, err := os.ReadFile(recorder.Path())
	if err != nil {
		t.Fatal(err)
	}
	gotRecords := make(map[string]struct{}, len(wantRecords))
	gotRecordCount := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "record-") {
			gotRecordCount++
			gotRecords[line] = struct{}{}
		}
	}
	if gotRecordCount != len(wantRecords) {
		t.Fatalf("transcript contains %d record lines, want %d accepted records", gotRecordCount, len(wantRecords))
	}
	for record := range wantRecords {
		if _, ok := gotRecords[record]; !ok {
			t.Errorf("transcript is missing accepted record %q", record)
		}
	}
}

func TestRecorderCloseReturnsWriterErrors(t *testing.T) {
	callback := make(chan error, 1)
	options := Options{
		Directory: t.TempDir(),
		Label:     "close-error",
		OnError:   func(err error) { callback <- err },
	}
	started := time.Now()
	writer, err := newTranscriptWriter(options.Directory, options, started, MaxFileBytes)
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
