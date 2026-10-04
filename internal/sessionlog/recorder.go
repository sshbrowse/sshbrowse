package sessionlog

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	DefaultMaxFileBytes  int64 = 10 * 1024 * 1024
	DefaultMaxTotalBytes int64 = 100 * 1024 * 1024

	queueChunks  = 64
	chunkBytes   = 32 * 1024
	closeTimeout = 5 * time.Second
)

var (
	ErrClosed       = errors.New("session recorder is closed")
	ErrQueueFull    = errors.New("session recording stopped because output arrived faster than it could be written")
	ErrSizeLimit    = errors.New("session recording stopped at its size limit")
	ErrCloseTimeout = errors.New("timed out finalizing session recording")
)

// Options configures an output-only session transcript.
type Options struct {
	Directory     string
	Label         string
	Kind          string
	MaxFileBytes  int64
	MaxTotalBytes int64
	OnError       func(error)
}

// Recorder writes a readable transcript on a background goroutine. Write only
// copies bytes into a bounded queue; filesystem work never runs in the caller.
type Recorder struct {
	queue chan []byte
	done  chan struct{}
	path  string

	writeMu     sync.Mutex
	closed      bool
	closeReason string
	notifyOnce  sync.Once

	statusMu sync.Mutex
	failure  error
	finalErr error

	options Options
}

// New creates a private, uniquely named transcript and starts its writer.
// It returns ErrSizeLimit if either size cap cannot fit a segment header, the
// reserved footer, and one maximum-length UTF-8 rune.
func New(options Options) (*Recorder, error) {
	if options.Directory == "" {
		directory, err := DefaultDirectory()
		if err != nil {
			return nil, err
		}
		options.Directory = directory
	}
	if options.MaxFileBytes <= 0 {
		options.MaxFileBytes = DefaultMaxFileBytes
	}
	if options.MaxTotalBytes <= 0 {
		options.MaxTotalBytes = DefaultMaxTotalBytes
	}
	if options.MaxFileBytes > math.MaxInt64/2 || options.MaxTotalBytes > math.MaxInt64/2 {
		return nil, errors.New("session log size limit is too large")
	}

	options.Label = cleanMetadata(options.Label, "Session", 160)
	options.Kind = cleanMetadata(options.Kind, "Terminal", 48)
	started := time.Now().UTC()
	header := (&transcriptWriter{options: options, started: started}).header(1)
	minimumCapacity := int64(len(header)) + footerReserveBytes + utf8.UTFMax
	if options.MaxFileBytes < minimumCapacity || options.MaxTotalBytes < minimumCapacity {
		return nil, ErrSizeLimit
	}

	directory, err := filepath.Abs(options.Directory)
	if err != nil {
		return nil, fmt.Errorf("resolve session log directory: %w", err)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create session log directory: %w", err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect session log directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("session log path %q is not a directory", directory)
	}

	namePrefix, err := uniqueNamePrefix(options.Label, started)
	if err != nil {
		return nil, err
	}
	writer, err := newTranscriptWriter(directory, namePrefix, options, started)
	if err != nil {
		return nil, err
	}

	recorder := &Recorder{
		queue:       make(chan []byte, queueChunks),
		done:        make(chan struct{}),
		path:        writer.path(),
		closeReason: "session ended",
		options:     options,
	}
	go recorder.run(writer)
	return recorder, nil
}

// DefaultDirectory returns the platform's per-user configuration directory
// used for session transcripts. New creates this directory when needed.
func DefaultDirectory() (string, error) {
	configDirectory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user configuration directory: %w", err)
	}
	return filepath.Join(configDirectory, "sshbrowse", "session-logs"), nil
}

// Write enqueues a copy of output bytes. It never waits for the disk writer.
// When its bounded queue is full, recording stops and the first error is
// reported asynchronously through OnError.
func (r *Recorder) Write(output []byte) error {
	if len(output) == 0 {
		return nil
	}

	r.writeMu.Lock()
	if r.closed {
		r.writeMu.Unlock()
		if err := r.firstError(); err != nil {
			return err
		}
		return ErrClosed
	}
	if err := r.firstError(); err != nil {
		r.closed = true
		close(r.queue)
		r.writeMu.Unlock()
		return err
	}

	neededChunks := (len(output)-1)/chunkBytes + 1
	if neededChunks > cap(r.queue)-len(r.queue) {
		notify := r.failLocked(ErrQueueFull)
		r.writeMu.Unlock()
		if notify {
			r.notifyError(ErrQueueFull)
		}
		return ErrQueueFull
	}

	for len(output) > 0 {
		chunkLength := len(output)
		if chunkLength > chunkBytes {
			chunkLength = chunkBytes
		}
		chunk := make([]byte, chunkLength)
		copy(chunk, output[:chunkLength])
		r.queue <- chunk
		output = output[chunkLength:]
	}
	r.writeMu.Unlock()
	return nil
}

// Close stops accepting output and waits up to a fixed deadline for queued
// bytes, the footer, and the file handle to be finalized.
func (r *Recorder) Close(reason string) error {
	reason = cleanMetadata(reason, "session ended", 128)
	r.writeMu.Lock()
	if !r.closed {
		r.closed = true
		r.closeReason = reason
		close(r.queue)
	}
	r.writeMu.Unlock()

	timer := time.NewTimer(closeTimeout)
	defer timer.Stop()
	select {
	case <-r.done:
		return r.closeError()
	case <-timer.C:
		return ErrCloseTimeout
	}
}

// Path is the absolute path of the first transcript segment.
func (r *Recorder) Path() string { return r.path }

// Done returns a channel that closes after the writer has finalized the
// transcript and closed its file.
func (r *Recorder) Done() <-chan struct{} { return r.done }

func (r *Recorder) run(writer *transcriptWriter) {
	defer close(r.done)
	formatter := newFormatter()
	writerFailed := false
	for chunk := range r.queue {
		if writerFailed {
			continue
		}
		formatted := formatter.feed(chunk)
		if len(formatted) == 0 {
			continue
		}
		if err := writer.writeOutput(formatted); err != nil {
			writerFailed = true
			r.stopWithFailure(err)
		}
	}

	if !writerFailed {
		if tail := formatter.finish(); len(tail) > 0 {
			if err := writer.writeOutput(tail); err != nil {
				writerFailed = true
				r.stopWithFailure(err)
			}
		}
	}
	if err := writer.close(r.reason()); err != nil {
		r.recordFinalError(err)
		r.stopWithFailure(err)
	}
}

func (r *Recorder) reason() string {
	if err := r.firstError(); err != nil {
		if errors.Is(err, ErrSizeLimit) {
			return "stopped at the configured size limit"
		}
		if errors.Is(err, ErrQueueFull) {
			return "stopped because output could not be queued"
		}
		return "stopped after an error"
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return r.closeReason
}

func (r *Recorder) stopWithFailure(err error) {
	if err == nil {
		return
	}
	r.writeMu.Lock()
	wasClosed := r.closed
	r.closed = true
	if !wasClosed {
		r.closeReason = "recording stopped after an error"
		close(r.queue)
	}
	notify := r.setFailure(err)
	r.writeMu.Unlock()
	if notify {
		r.notifyError(err)
	}
}

func (r *Recorder) failLocked(err error) bool {
	r.closed = true
	r.closeReason = "recording stopped after an error"
	notify := r.setFailure(err)
	close(r.queue)
	return notify
}

func (r *Recorder) setFailure(err error) bool {
	r.statusMu.Lock()
	first := r.failure == nil
	if first {
		r.failure = err
	}
	r.statusMu.Unlock()
	return first
}

func (r *Recorder) notifyError(err error) {
	r.notifyOnce.Do(func() {
		if r.options.OnError != nil {
			callback := r.options.OnError
			go callback(err)
		}
	})
}

func (r *Recorder) firstError() error {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	return r.failure
}

func (r *Recorder) recordFinalError(err error) {
	r.statusMu.Lock()
	r.finalErr = errors.Join(r.finalErr, err)
	r.statusMu.Unlock()
}

func (r *Recorder) closeError() error {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	return errors.Join(r.failure, r.finalErr)
}

func cleanMetadata(value, fallback string, maxRunes int) string {
	var clean strings.Builder
	runeCount := 0
	for _, r := range strings.ToValidUTF8(value, "�") {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			clean.WriteByte(' ')
		} else {
			clean.WriteRune(r)
		}
		runeCount++
		if runeCount >= maxRunes {
			break
		}
	}
	result := strings.TrimSpace(clean.String())
	if result == "" {
		return fallback
	}
	return result
}
