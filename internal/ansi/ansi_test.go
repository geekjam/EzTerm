package ansi

import (
	"strings"
	"testing"
)

func TestStrip(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b[31mred\x1b[0m", "red"},
		{"\x1b[1;32mgreen\x1b[0m", "green"},
		{"no escapes here", "no escapes here"},
		{"\x1b]0;title\x07plain", "plain"},
		{"a\x1b[2K\rb", "a\rb"}, // \r is a carriage return, not an ANSI escape
	}
	for _, tc := range cases {
		if got := Strip(tc.in); got != tc.want {
			t.Errorf("Strip(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCompact(t *testing.T) {
	cases := []struct{ in, want string }{
		{"line1\r\nline2", "line1\nline2"},
		{"progress\r50%\r100%\n", "100%\n"},
		{"a\n\n\n\nb", "a\n\nb"},
		{"\x07bell\x00", "bell"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := Compact(tc.in); got != tc.want {
			t.Errorf("Compact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripThenCompactIntegration(t *testing.T) {
	out := Compact(Strip("\x1b[32mLoading...\r\x1b[0mDone.\r\n"))
	if strings.Contains(out, "\x1b") {
		t.Fatalf("output still contains escapes: %q", out)
	}
	if !strings.Contains(out, "Done.") {
		t.Fatalf("expected Done. in output: %q", out)
	}
}
