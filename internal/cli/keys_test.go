package cli

import "testing"

func TestParseKeySequences(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "enter", input: "enter", want: "\r"},
		{name: "escape", input: "esc", want: "\x1b"},
		{name: "tab", input: "tab", want: "\t"},
		{name: "space", input: "space", want: " "},
		{name: "backspace", input: "backspace", want: "\x7f"},
		{name: "insert", input: "insert", want: "\x1b[2~"},
		{name: "delete", input: "delete", want: "\x1b[3~"},
		{name: "home", input: "home", want: "\x1b[H"},
		{name: "end", input: "end", want: "\x1b[F"},
		{name: "page up", input: "pageup", want: "\x1b[5~"},
		{name: "page down", input: "pagedown", want: "\x1b[6~"},
		{name: "up", input: "up", want: "\x1b[A"},
		{name: "down", input: "down", want: "\x1b[B"},
		{name: "left", input: "left", want: "\x1b[D"},
		{name: "right", input: "right", want: "\x1b[C"},
		{name: "f1", input: "f1", want: "\x1bOP"},
		{name: "f2", input: "f2", want: "\x1bOQ"},
		{name: "f3", input: "f3", want: "\x1bOR"},
		{name: "f4", input: "f4", want: "\x1bOS"},
		{name: "f5", input: "f5", want: "\x1b[15~"},
		{name: "f6", input: "f6", want: "\x1b[17~"},
		{name: "f7", input: "f7", want: "\x1b[18~"},
		{name: "f8", input: "f8", want: "\x1b[19~"},
		{name: "f9", input: "f9", want: "\x1b[20~"},
		{name: "f10", input: "f10", want: "\x1b[21~"},
		{name: "f11", input: "f11", want: "\x1b[23~"},
		{name: "f12", input: "f12", want: "\x1b[24~"},
		{name: "letter", input: "a", want: "a"},
		{name: "digit", input: "1", want: "1"},
		{name: "punctuation", input: "?", want: "?"},
		{name: "uppercase alias", input: "A", want: "a"},
		{name: "uppercase combination", input: "CTRL+C", want: "\x03"},
		{name: "ctrl c", input: "ctrl+c", want: "\x03"},
		{name: "ctrl d", input: "ctrl+d", want: "\x04"},
		{name: "ctrl z", input: "ctrl+z", want: "\x1a"},
		{name: "shift letter", input: "shift+a", want: "A"},
		{name: "alt letter", input: "alt+a", want: "\x1ba"},
		{name: "ctrl alt letter", input: "ctrl+alt+c", want: "\x1b\x03"},
		{name: "shift tab", input: "shift+tab", want: "\x1b[Z"},
		{name: "alt left meta", input: "alt+left", want: "\x1b\x1b[D"},
		{name: "ctrl up", input: "ctrl+up", want: "\x1b[1;5A"},
		{name: "shift up", input: "shift+up", want: "\x1b[1;2A"},
		{name: "ctrl shift up", input: "ctrl+shift+up", want: "\x1b[1;6A"},
		{name: "shift alt left", input: "shift+alt+left", want: "\x1b[1;4D"},
		{name: "ctrl alt shift up", input: "ctrl+alt+shift+up", want: "\x1b[1;8A"},
		{name: "shift f5", input: "shift+f5", want: "\x1b[15;2~"},
		{name: "alt f5 meta", input: "alt+f5", want: "\x1b\x1b[15~"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseKey(tt.input)
			if err != nil {
				t.Fatalf("parseKey(%q): %v", tt.input, err)
			}
			if string(got) != tt.want {
				t.Fatalf("parseKey(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseKeyRejectsInvalidInput(t *testing.T) {
	tests := []string{
		"",
		" ",
		"foo",
		"f13",
		"ctrl+ctrl",
		"ctrl+ctrl+c",
		"shift+shift+a",
		"ctrl+",
		"+c",
		"a+b",
		"ctrl+1",
		"shift+1",
		"ctrl+tab",
		"ctrl+enter",
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if _, err := parseKey(input); err == nil {
				t.Fatalf("parseKey(%q) returned nil error", input)
			}
		})
	}
}
