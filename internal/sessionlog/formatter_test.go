package sessionlog

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFormatterHandlesChunkBoundariesAndTerminalControls(t *testing.T) {
	format := newFormatter()
	chunks := [][]byte{
		[]byte("ready \x1b[3"),
		[]byte("1m猫\x1b[0m\x1b]0;title"),
		[]byte("\a\n\x1bPignored"),
		[]byte(" dcs\x1b\\ 20%\r40%\r100%\n"),
	}
	var output bytes.Buffer
	for _, chunk := range chunks {
		output.Write(format.feed(chunk))
	}
	output.Write(format.finish())

	if got, want := output.String(), "ready 猫\n100%\n"; got != want {
		t.Fatalf("formatted transcript = %q, want %q", got, want)
	}
}

func TestFormatterCancelsEscapeSequencesAndControlStrings(t *testing.T) {
	tests := []struct {
		name   string
		prefix []byte
		cancel byte
	}{
		{name: "CSI CAN", prefix: []byte("\x1b[31"), cancel: 0x18},
		{name: "escape sequence SUB", prefix: []byte("\x1b("), cancel: 0x1a},
		{name: "OSC CAN", prefix: []byte("\x1b]title"), cancel: 0x18},
		{name: "control string SUB", prefix: []byte("\x1bPpayload"), cancel: 0x1a},
		{name: "discarded CSI CAN", prefix: append([]byte("\x1b["), bytes.Repeat([]byte("1"), maxEscapeBytes)...), cancel: 0x18},
		{name: "discarded OSC SUB", prefix: append([]byte("\x1b]"), bytes.Repeat([]byte("x"), maxEscapeBytes+1)...), cancel: 0x1a},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			format := newFormatter()
			var output bytes.Buffer
			output.Write(format.feed(append([]byte("prefix"), test.prefix...)))
			output.Write(format.feed([]byte{test.cancel}))
			output.Write(format.feed([]byte("visible\n")))
			output.Write(format.finish())
			if got, want := output.String(), "prefixvisible\n"; got != want {
				t.Fatalf("formatted transcript = %q, want %q", got, want)
			}
		})
	}
}

func TestFormatterHandlesBackspaceAndIncompleteUTF8(t *testing.T) {
	format := newFormatter()
	got := append(format.feed([]byte("abcde\rxy\bZ\n\xe2\x82")), format.finish()...)
	if string(got) != "xZcde\n��\n" {
		t.Fatalf("formatted transcript = %q", got)
	}
}

func TestFormatterDiscardsOverlongControlStringsAndResumes(t *testing.T) {
	for _, sequence := range []string{"\x1b]", "\x1bP", "\u009d", "\u0090"} {
		format := newFormatter()
		terminator := "\x1b\\"
		if sequence == "\u009d" || sequence == "\u0090" {
			terminator = "\u009c"
		}
		longContent := strings.Repeat("x", maxEscapeBytes+32)
		input := sequence + longContent + terminator + "visible\n"
		terminatorStart := len(sequence) + len(longContent)
		chunks := [][]byte{
			[]byte(input[:len(sequence)-1]),
			[]byte(input[len(sequence)-1 : terminatorStart+1]),
			[]byte(input[terminatorStart+1:]),
		}
		var output bytes.Buffer
		for _, chunk := range chunks {
			output.Write(format.feed(chunk))
		}
		output.Write(format.finish())
		got := output.Bytes()
		if string(got) != "visible\n" {
			t.Fatalf("sequence %q leaked or suppressed output: %q", sequence, got)
		}
	}
}

func TestFormatterHandlesSplitC1IntroducersAndTerminators(t *testing.T) {
	format := newFormatter()
	chunks := [][]byte{
		[]byte("a\xc2"),
		[]byte("\x9b31mred\xc2\x9dhidden\xc2"),
		[]byte("\x9c\xc2\x90dcs\xc2"),
		[]byte("\x9cend\xc2\x9cy\n"),
	}
	var output bytes.Buffer
	for _, chunk := range chunks {
		output.Write(format.feed(chunk))
	}
	output.Write(format.finish())
	if got, want := output.String(), "aredendy\n"; got != want {
		t.Fatalf("C1 sequences were not filtered across chunks: got %q, want %q", got, want)
	}
	assertNoC1Controls(t, output.String())
}

func TestFormatterSuppressesAllOtherC1Controls(t *testing.T) {
	var controls strings.Builder
	for control := rune(0x80); control <= 0x9f; control++ {
		controls.WriteRune(control)
		switch control {
		case '\u0090', '\u0098', '\u009e', '\u009f':
			controls.WriteString("discarded")
			controls.WriteRune('\u009c')
		case '\u009b':
			controls.WriteString("31m")
		case '\u009d':
			controls.WriteString("discarded")
			controls.WriteRune('\u009c')
		}
	}
	format := newFormatter()
	got := append(format.feed([]byte(controls.String()+"visible\n")), format.finish()...)
	if string(got) != "visible\n" {
		t.Fatalf("C1 controls leaked into transcript: %q", got)
	}
	if !utf8.Valid(got) {
		t.Fatal("formatted output is not valid UTF-8")
	}
	assertNoC1Controls(t, string(got))
}

func assertNoC1Controls(t *testing.T, text string) {
	t.Helper()
	for _, r := range text {
		if r >= 0x80 && r <= 0x9f {
			t.Fatalf("C1 control U+%04X survived formatting", r)
		}
	}
}

func TestFormatterRecoversFromRepeatedEscapeBytes(t *testing.T) {
	format := newFormatter()
	got := append(format.feed([]byte("before\x1b\x1b[31mred\n")), format.finish()...)
	if string(got) != "beforered\n" {
		t.Fatalf("malformed escape sequence leaked into transcript: %q", got)
	}
}

func TestFormatterBoundsLongLinesAndTabAfterLimit(t *testing.T) {
	format := newFormatter()
	input := strings.Repeat("a", maxLineRunes) + "\tZ\n"
	got := append(format.feed([]byte(input)), format.finish()...)
	if !strings.Contains(string(got), " [line continued]\n") {
		t.Fatal("expected an explicit continuation marker for a long line")
	}
	if !strings.HasSuffix(string(got), "Z\n") {
		t.Fatalf("tab after a full line was not processed: suffix %q", string(got)[len(got)-20:])
	}
	if len(format.line) > maxLineRunes {
		t.Fatalf("formatter line buffer grew to %d runes", len(format.line))
	}
}
