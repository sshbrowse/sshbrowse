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
	directory string
	prefix    string
	options   Options
	started   time.Time

	file        *os.File
	currentPath string
	segment     int
	segmentSize int64
	totalSize   int64
	closed      bool
}

func newTranscriptWriter(directory, prefix string, options Options, started time.Time) (*transcriptWriter, error) {
	writer := &transcriptWriter{
		directory: directory,
		prefix:    prefix,
		options:   options,
		started:   started,
	}
	if err := writer.openSegment(1); err != nil {
		return nil, err
	}
	return writer, nil
}

func (w *transcriptWriter) path() string { return w.currentPath }

func (w *transcriptWriter) openSegment(segment int) error {
	header := w.header(segment)
	minimumCapacity := int64(len(header)) + footerReserveBytes + utf8.UTFMax
	if w.options.MaxFileBytes < minimumCapacity ||
		w.options.MaxTotalBytes < w.totalSize+minimumCapacity {
		return ErrSizeLimit
	}
	file, path, prefix, err := createUniqueSegment(w.directory, w.prefix, segment)
	if err != nil {
		return fmt.Errorf("create session transcript: %w", err)
	}
	w.file = file
	w.currentPath = path
	w.prefix = prefix
	w.segment = segment
	w.segmentSize = 0
	if err := w.writeBytes(header); err != nil {
		_ = file.Close()
		w.file = nil
		return fmt.Errorf("write session transcript header: %w", err)
	}
	return nil
}

func (w *transcriptWriter) writeOutput(output []byte) error {
	for len(output) > 0 {
		fileAvailable := w.options.MaxFileBytes - w.segmentSize - footerReserveBytes
		totalAvailable := w.options.MaxTotalBytes - w.totalSize - footerReserveBytes
		available := fileAvailable
		if totalAvailable < available {
			available = totalAvailable
		}
		if available <= 0 {
			if err := w.rotate(); err != nil {
				return err
			}
			continue
		}

		count := int64(len(output))
		if count > available {
			count = available
		}
		count = int64(validUTF8Prefix(output, int(count)))
		if count == 0 {
			if err := w.rotate(); err != nil {
				return err
			}
			continue
		}
		if err := w.writeBytes(output[:int(count)]); err != nil {
			return fmt.Errorf("write session transcript: %w", err)
		}
		output = output[int(count):]
	}
	return nil
}

func (w *transcriptWriter) rotate() error {
	if w.closed {
		return ErrSizeLimit
	}
	nextSegment := w.segment + 1
	footer := w.footer(fmt.Sprintf("continued in segment %03d", nextSegment))
	header := w.header(nextSegment)
	if w.options.MaxFileBytes < int64(len(header))+footerReserveBytes+utf8.UTFMax ||
		w.totalSize+int64(len(footer)+len(header))+footerReserveBytes+utf8.UTFMax > w.options.MaxTotalBytes {
		return w.stopAtLimit()
	}
	nextFile, nextPath, nextPrefix, err := createUniqueSegment(w.directory, w.prefix, nextSegment)
	if err != nil {
		return fmt.Errorf("create next session transcript segment: %w", err)
	}
	if err := w.writeReservedFooter(footer); err != nil {
		_ = nextFile.Close()
		return fmt.Errorf("close previous session transcript segment: %w", err)
	}
	if err := w.file.Close(); err != nil {
		w.file = nil
		_ = nextFile.Close()
		return fmt.Errorf("close previous session transcript segment: %w", err)
	}
	w.file = nextFile
	w.currentPath = nextPath
	w.prefix = nextPrefix
	w.segment = nextSegment
	w.segmentSize = 0
	if err := w.writeBytes(header); err != nil {
		return fmt.Errorf("write next session transcript segment header: %w", err)
	}
	return nil
}

func (w *transcriptWriter) stopAtLimit() error {
	if w.closed {
		return ErrSizeLimit
	}
	footer := w.footer("stopped at the configured size limit")
	var footerErr error
	if w.file != nil {
		footerErr = w.writeReservedFooter(footer)
		if err := w.file.Close(); err != nil {
			footerErr = errors.Join(footerErr, fmt.Errorf("close session transcript: %w", err))
		}
		w.file = nil
	}
	w.closed = true
	return errors.Join(ErrSizeLimit, footerErr)
}

func (w *transcriptWriter) close(reason string) error {
	if w.closed {
		return nil
	}
	if w.file == nil {
		w.closed = true
		return nil
	}
	footer := w.footer(reason)
	writeErr := w.writeReservedFooter(footer)
	closeErr := w.file.Close()
	w.file = nil
	w.closed = true
	if writeErr != nil {
		writeErr = fmt.Errorf("write session transcript footer: %w", writeErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close session transcript: %w", closeErr)
	}
	return errors.Join(writeErr, closeErr)
}

func (w *transcriptWriter) writeReservedFooter(footer []byte) error {
	if int64(len(footer)) > footerReserveBytes {
		return errors.New("session transcript footer exceeded its reserved size")
	}
	if w.totalSize+int64(len(footer)) > w.options.MaxTotalBytes ||
		w.segmentSize+int64(len(footer)) > w.options.MaxFileBytes {
		return ErrSizeLimit
	}
	return w.writeBytes(footer)
}

func (w *transcriptWriter) writeBytes(data []byte) error {
	for len(data) > 0 {
		written, err := w.file.Write(data)
		w.totalSize += int64(written)
		w.segmentSize += int64(written)
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

func (w *transcriptWriter) header(segment int) []byte {
	return []byte(fmt.Sprintf(
		"SSHBrowse session transcript\nStarted: %s\nSession: %s\nType: %s\nSegment: %03d\n\n",
		w.started.Format(time.RFC3339Nano), w.options.Label, w.options.Kind, segment,
	))
}

func (w *transcriptWriter) footer(reason string) []byte {
	// Ninety-six four-byte runes plus the fixed timestamp and framing fit
	// within footerReserveBytes.
	reason = cleanMetadata(reason, "session ended", 96)
	return []byte(fmt.Sprintf("\n[Recording ended: %s at %s]\n", reason, time.Now().UTC().Format(time.RFC3339Nano)))
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
