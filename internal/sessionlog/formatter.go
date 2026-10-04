package sessionlog

import (
	"bytes"
	"unicode"
	"unicode/utf8"
)

const (
	maxEscapeBytes = 4096
	maxLineRunes   = 16 * 1024
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
	state       escapeState
	sequenceLen int
	stringEsc   bool
	stringC2    bool
	utf8Pending []byte

	line   []rune
	cursor int
}

func newFormatter() *formatter {
	return &formatter{
		utf8Pending: make([]byte, 0, utf8.UTFMax),
		line:        make([]rune, 0, 256),
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
	f.line = f.line[:0]
	f.cursor = 0
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
		f.line = f.line[:0]
		f.cursor = 0
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
		f.state = escapeCSI
		f.sequenceLen = 0
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
	if f.cursor >= maxLineRunes {
		// A newline keeps a malicious or accidental unbroken stream bounded and
		// makes each continuation readable in a plain-text transcript.
		f.writeLine(output)
		output.WriteString(" [line continued]\n")
		f.line = f.line[:0]
		f.cursor = 0
	}
	if f.cursor < len(f.line) {
		f.line[f.cursor] = r
	} else {
		for len(f.line) < f.cursor {
			f.line = append(f.line, ' ')
		}
		f.line = append(f.line, r)
	}
	f.cursor++
}

func (f *formatter) writeLine(output *bytes.Buffer) {
	for _, r := range f.line {
		output.WriteRune(r)
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
			f.state = escapeCSI
			f.stringEsc = false
			f.stringC2 = false
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
			f.state = escapeNormal
		} else if b == 0x1b {
			f.state = escapeStart
			f.sequenceLen = 0
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
