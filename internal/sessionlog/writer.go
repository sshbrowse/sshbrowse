package sessionlog

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"
	"unicode/utf8"
)

const footerReserveBytes int64 = 512

type transcriptWriter struct {
	options  Options
	started  time.Time
	maxBytes int64

	file        *os.File
	currentPath string
	size        int64
	closed      bool
}

func newTranscriptWriter(directory string, options Options, started time.Time, maxBytes int64) (*transcriptWriter, error) {
	writer := &transcriptWriter{
		options:  options,
		started:  started,
		maxBytes: maxBytes,
	}
	header := writer.header()
	if maxBytes < int64(len(header))+footerReserveBytes+utf8.UTFMax {
		return nil, ErrSizeLimit
	}

	file, path, err := createUniqueFile(directory, options.Label, started)
	if err != nil {
		return nil, fmt.Errorf("create session transcript: %w", err)
	}
	writer.file = file
	writer.currentPath = path
	if err := writer.writeBytes(header); err != nil {
		// Report the write failure even if best-effort cleanup also fails.
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write session transcript header: %w", err)
	}
	return writer, nil
}

func (w *transcriptWriter) path() string { return w.currentPath }

func (w *transcriptWriter) writeOutput(output []byte) error {
	if w.closed {
		return ErrSizeLimit
	}
	available := w.maxBytes - w.size - footerReserveBytes
	if available <= 0 {
		return w.stopAtLimit()
	}
	count := len(output)
	if int64(count) > available {
		count = int(available)
	}
	count = validUTF8Prefix(output, count)
	if count > 0 {
		if err := w.writeBytes(output[:count]); err != nil {
			return fmt.Errorf("write session transcript: %w", err)
		}
	}
	if count < len(output) {
		return w.stopAtLimit()
	}
	return nil
}

func (w *transcriptWriter) stopAtLimit() error {
	footerErr := w.finish("stopped at the file size limit")
	return errors.Join(ErrSizeLimit, footerErr)
}

func (w *transcriptWriter) close(reason string) error {
	if w.closed {
		return nil
	}
	return w.finish(reason)
}

func (w *transcriptWriter) finish(reason string) error {
	if w.closed {
		return nil
	}
	w.closed = true
	footer := w.footer(reason)
	var writeErr error
	if int64(len(footer)) > footerReserveBytes || w.size+int64(len(footer)) > w.maxBytes {
		writeErr = ErrSizeLimit
	} else {
		writeErr = w.writeBytes(footer)
	}
	closeErr := w.file.Close()
	w.file = nil
	if writeErr != nil {
		writeErr = fmt.Errorf("write session transcript footer: %w", writeErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close session transcript: %w", closeErr)
	}
	return errors.Join(writeErr, closeErr)
}

func (w *transcriptWriter) writeBytes(data []byte) error {
	for len(data) > 0 {
		written, err := w.file.Write(data)
		w.size += int64(written)
		data = data[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (w *transcriptWriter) header() []byte {
	return []byte(fmt.Sprintf(
		"SSHBrowse session transcript\nStarted: %s\nSession: %s\nType: %s\n\n",
		w.started.Format(time.RFC3339Nano), w.options.Label, w.options.Kind,
	))
}

func (w *transcriptWriter) footer(reason string) []byte {
	// Bound the reason so the footer always fits in its reserved space.
	reason = cleanMetadata(reason, "session ended", 72)
	return []byte(fmt.Sprintf("\n[Recording ended: %s at %s]\n", reason, time.Now().Format(time.RFC3339Nano)))
}

func validUTF8Prefix(data []byte, maximum int) int {
	if maximum > len(data) {
		maximum = len(data)
	}
	for maximum > 0 && !utf8.Valid(data[:maximum]) {
		maximum--
	}
	return maximum
}
