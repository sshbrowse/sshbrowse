package sessionlog

import (
	"bytes"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

const (
	maxEscapeBytes  = 4096
	maxLineColumns  = 16 * 1024
	maxLineBytes    = 64 * 1024
	maxClusterBytes = 256
)

type escapeState uint8

const (
	escapeNormal escapeState = iota
	escapeStart
	escapeSequence
	escapeCSI
	escapeOSC
	escapeControlString
	escapeDiscardSequence
	escapeDiscardOSC
	escapeDiscardControlString
)

// formatter strips terminal control sequences and resolves basic line editing
// while retaining only a bounded current line.
type formatter struct {
	state        escapeState
	sequenceLen  int
	stringEsc    bool
	stringC2     bool
	utf8Pending  []byte
	csiParameter int
	csiValid     bool

	// Each entry is one screen column. Empty entries continue the preceding
	// grapheme; unwritten columns contain a space.
	line        []string
	lineBytes   int
	cursor      int
	outputStart int // Skip the unrecorded first-line prefix until it is redrawn.
}

func newFormatter(initialColumn int) *formatter {
	initialColumn = min(maxLineColumns, max(0, initialColumn))
	return &formatter{
		cursor:      initialColumn,
		outputStart: initialColumn,
		utf8Pending: make([]byte, 0, utf8.UTFMax),
		line:        make([]string, 0, 256),
	}
}

func (f *formatter) feed(input []byte) []byte {
	var output bytes.Buffer
	for _, b := range input {
		f.feedByte(b, &output)
	}
	return output.Bytes()
}

func (f *formatter) finish() []byte {
	var output bytes.Buffer
	for len(f.utf8Pending) > 0 {
		if f.utf8Pending[0] < utf8.RuneSelf {
			b := f.utf8Pending[0]
			f.utf8Pending = f.utf8Pending[1:]
			f.feedByte(b, &output)
			continue
		}
		f.writeRune(utf8.RuneError, &output)
		f.utf8Pending = f.utf8Pending[1:]
	}
	if len(f.line) > 0 {
		f.writeLine(&output)
	}
	f.resetLine()
	f.state = escapeNormal
	return output.Bytes()
}

func (f *formatter) feedByte(b byte, output *bytes.Buffer) {
	if f.state != escapeNormal {
		f.feedEscapeByte(b)
		return
	}
	if len(f.utf8Pending) > 0 {
		f.utf8Pending = append(f.utf8Pending, b)
		f.decodePending(output)
		return
	}
	if b == 0x1b {
		f.state = escapeStart
		f.sequenceLen = 0
		return
	}
	if b < utf8.RuneSelf {
		f.feedASCII(b, output)
		return
	}
	f.utf8Pending = append(f.utf8Pending, b)
	f.decodePending(output)
}

func (f *formatter) decodePending(output *bytes.Buffer) {
	for len(f.utf8Pending) > 0 {
		if f.utf8Pending[0] < utf8.RuneSelf {
			b := f.utf8Pending[0]
			f.utf8Pending = f.utf8Pending[1:]
			f.feedByte(b, output)
			continue
		}
		if !utf8.FullRune(f.utf8Pending) {
			return
		}
		r, size := utf8.DecodeRune(f.utf8Pending)
		if size == 0 {
			return
		}
		f.utf8Pending = f.utf8Pending[size:]
		f.writeRune(r, output)
	}
}

func (f *formatter) feedASCII(b byte, output *bytes.Buffer) {
	switch b {
	case '\n':
		f.writeLine(output)
		f.resetLine()
	case '\r':
		f.cursor = 0
	case '\b':
		if f.cursor > 0 {
			f.cursor--
		}
	case '\t':
		spaces := 8 - f.cursor%8
		for i := 0; i < spaces; i++ {
			f.writeRune(' ', output)
		}
	default:
		if b >= 0x20 && b != 0x7f {
			f.writeRune(rune(b), output)
		}
	}
}

func (f *formatter) writeRune(r rune, output *bytes.Buffer) {
	switch r {
	case '\u0090', '\u0098', '\u009e', '\u009f':
		f.startControlString()
		return
	case '\u009b':
		f.startCSI()
		return
	case '\u009d':
		f.startOSC()
		return
	case '\u009c':
		return
	}
	if unicode.IsControl(r) {
		return
	}
	text := string(r)
	if r >= utf8.RuneSelf && f.cursor > 0 && f.cursor <= len(f.line) {
		start := f.cursor - 1
		for start > 0 && f.line[start] == "" {
			start--
		}
		previous := f.line[start]
		cluster, rest, width, _ := uniseg.FirstGraphemeClusterInString(previous+text, -1)
		if rest == "" {
			if len(cluster) <= maxClusterBytes {
				if start+width > maxLineColumns {
					// Move a growing grapheme to the continuation once, without
					// emitting its earlier text presentation on the previous line.
					f.truncateLine(start)
				}
				f.writeCluster(start, cluster, width, output)
				return
			}
			// A combining stream must not grow one cell indefinitely or make
			// grapheme segmentation progressively more expensive.
			f.continueLine(output)
		}
	}
	width := 1
	if r >= utf8.RuneSelf {
		width = uniseg.StringWidth(text)
	}
	// Like a terminal, an unattached zero-width mark cannot advance the cursor.
	if width > 0 {
		f.writeCluster(f.cursor, text, width, output)
	}
}

func (f *formatter) writeCluster(column int, text string, width int, output *bytes.Buffer) {
	if column+width > maxLineColumns {
		f.continueLine(output)
		column = 0
	}
	end := column + width
	for len(f.line) < end {
		f.line = append(f.line, " ")
		f.lineBytes++
	}
	f.eraseColumns(column, end)
	f.lineBytes += len(text) - len(f.line[column])
	f.line[column] = text
	for i := column + 1; i < end; i++ {
		f.lineBytes -= len(f.line[i])
		f.line[i] = ""
	}
	f.cursor = end
	f.outputStart = min(f.outputStart, column)
	if f.lineBytes >= maxLineBytes {
		f.continueLine(output)
	}
}

func (f *formatter) continueLine(output *bytes.Buffer) {
	f.writeLine(output)
	output.WriteString(" [line continued]\n")
	f.resetLine()
}

// Erasing any part of a wide grapheme clears its remaining columns too.
func (f *formatter) eraseColumns(start, end int) {
	if start >= end {
		return
	}
	for start > 0 && f.line[start] == "" {
		start--
	}
	for end < len(f.line) && f.line[end] == "" {
		end++
	}
	for i := start; i < end; i++ {
		f.lineBytes += 1 - len(f.line[i])
		f.line[i] = " "
	}
}

func (f *formatter) resetLine() {
	clear(f.line)
	f.line = f.line[:0]
	f.lineBytes = 0
	f.cursor = 0
	f.outputStart = 0
}

func (f *formatter) truncateLine(end int) {
	for _, cell := range f.line[end:] {
		f.lineBytes -= len(cell)
	}
	clear(f.line[end:])
	f.line = f.line[:end]
}

func (f *formatter) writeLine(output *bytes.Buffer) {
	for _, cell := range f.line[min(f.outputStart, len(f.line)):] {
		output.WriteString(cell)
	}
	output.WriteByte('\n')
}

func (f *formatter) feedEscapeByte(b byte) {
	if b == 0x18 || b == 0x1a { // CAN and SUB cancel any in-progress escape sequence.
		f.state = escapeNormal
		f.sequenceLen = 0
		f.stringEsc = false
		f.stringC2 = false
		return
	}
	if f.sequenceLen <= maxEscapeBytes {
		f.sequenceLen++
	}
	if f.sequenceLen > maxEscapeBytes {
		switch f.state {
		case escapeOSC, escapeDiscardOSC:
			f.state = escapeDiscardOSC
		case escapeControlString, escapeDiscardControlString:
			f.state = escapeDiscardControlString
		default:
			f.state = escapeDiscardSequence
		}
	}

	switch f.state {
	case escapeStart:
		switch b {
		case 0x1b:
			f.state = escapeStart
			f.sequenceLen = 0
		case '[':
			f.startCSI()
		case ']':
			f.startOSC()
		case 'P', '_', '^', 'X':
			f.startControlString()
		default:
			if b >= 0x30 && b <= 0x7e {
				f.state = escapeNormal
			} else {
				f.state = escapeSequence
			}
		}
	case escapeSequence:
		if b >= 0x30 && b <= 0x7e {
			f.state = escapeNormal
		} else if b == 0x1b {
			f.state = escapeStart
			f.sequenceLen = 0
		}
	case escapeCSI:
		if b >= 0x40 && b <= 0x7e {
			f.applyCSI(b)
			f.state = escapeNormal
		} else if b == 0x1b {
			f.state = escapeStart
			f.sequenceLen = 0
		} else if b >= '0' && b <= '9' {
			f.csiParameter = min(maxLineColumns, f.csiParameter*10+int(b-'0'))
		} else {
			// Private, intermediate, and multiple parameters are outside the
			// single-line editing commands supported by this transcript.
			f.csiValid = false
		}
	case escapeOSC:
		f.feedControlStringByte(b, true)
	case escapeControlString:
		f.feedControlStringByte(b, false)
	case escapeDiscardSequence:
		if b >= 0x40 && b <= 0x7e {
			f.state = escapeNormal
		} else if b == 0x1b {
			f.state = escapeStart
			f.sequenceLen = 0
		}
	case escapeDiscardOSC:
		f.feedControlStringByte(b, true)
	case escapeDiscardControlString:
		f.feedControlStringByte(b, false)
	}
	if f.state == escapeNormal {
		f.sequenceLen = 0
		f.stringEsc = false
		f.stringC2 = false
	}
}

func (f *formatter) startCSI() {
	f.state = escapeCSI
	f.sequenceLen = 0
	f.csiParameter = 0
	f.csiValid = true
}

// Resolve the current-line cursor and erase commands used by shell redraws.
// Other CSI commands remain filtered; this is not a screen emulator.
func (f *formatter) applyCSI(command byte) {
	if !f.csiValid {
		return
	}
	count := max(1, f.csiParameter)
	switch command {
	case 'C':
		f.cursor = min(maxLineColumns, f.cursor+count)
	case 'D':
		f.cursor = max(0, f.cursor-count)
	case 'G':
		f.cursor = count - 1
	case 'K':
		switch f.csiParameter {
		case 0:
			end := min(f.cursor, len(f.line))
			f.eraseColumns(end, len(f.line))
			f.truncateLine(end)
		case 1:
			f.eraseColumns(0, min(f.cursor+1, len(f.line)))
		case 2:
			cursor := f.cursor
			f.resetLine()
			f.cursor = cursor
		}
	}
}

func (f *formatter) startOSC() {
	f.state = escapeOSC
	f.sequenceLen = 0
	f.stringEsc = false
	f.stringC2 = false
}

func (f *formatter) startControlString() {
	f.state = escapeControlString
	f.sequenceLen = 0
	f.stringEsc = false
	f.stringC2 = false
}

func (f *formatter) feedControlStringByte(b byte, osc bool) {
	if f.stringC2 {
		f.stringC2 = false
		if b == 0x9c {
			f.state = escapeNormal
			return
		}
	}
	if b == 0xc2 {
		f.stringC2 = true
		f.stringEsc = false
		return
	}
	if osc && b == 0x07 || f.stringEsc && b == '\\' {
		f.state = escapeNormal
		return
	}
	f.stringEsc = b == 0x1b
}
