package main

import (
	"testing"
)

func TestTerminalSafe(t *testing.T) {
	for _, c := range []struct {
		input     string
		multiline bool
		want      string
	}{
		// OSC sequence
		{"\x1b]0;pwned\x07", false, "\\x1b]0;pwned\\x07"},
		// Newline and tab with multiline=false
		{"a\nb", false, "a\\x0ab"},
		// Newline and tab preserved with multiline=true
		{"a\nb", true, "a\nb"},
		{"a\tb", true, "a\tb"},
		// Bidi control character (U+202E)
		{"\u202eevil", false, "\\u202eevil"},
		// C1 NEL control character
		{"\u0085", false, "\\x85"},
		// Plain text with tab, multiline=true preserves it
		{"正常 text\t", true, "正常 text\t"},
		// Plain text unchanged
		{"正常 text", false, "正常 text"},
		// ESC character
		{"\x1b", false, "\\x1b"},
		// Empty string
		{"", false, ""},
		// Failed task note with OSC sequence (peer-controlled error reason)
		{"对方拒绝了\x1b]0;evil\x07", false, "对方拒绝了\\x1b]0;evil\\x07"},
	} {
		got := terminalSafe(c.input, c.multiline)
		if got != c.want {
			t.Errorf("terminalSafe(%q, %v) = %q; want %q", c.input, c.multiline, got, c.want)
		}
	}
}
