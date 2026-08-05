package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// keyModifiers is a bitmask of the supported keyboard modifiers.
type keyModifiers uint8

const (
	modifierShift keyModifiers = 1 << iota
	modifierAlt
	modifierCtrl
)

// keySequence describes a key's plain and xterm-modified encodings.
type keySequence struct {
	plain string
	param string
	final string
}

var simpleKeySequences = map[string]string{
	"enter":     "\r",
	"esc":       "\x1b",
	"tab":       "\t",
	"space":     " ",
	"backspace": "\x7f",
}

var namedKeySequences = map[string]keySequence{
	"insert":   {plain: "\x1b[2~", param: "2", final: "~"},
	"delete":   {plain: "\x1b[3~", param: "3", final: "~"},
	"home":     {plain: "\x1b[H", param: "1", final: "H"},
	"end":      {plain: "\x1b[F", param: "1", final: "F"},
	"pageup":   {plain: "\x1b[5~", param: "5", final: "~"},
	"pagedown": {plain: "\x1b[6~", param: "6", final: "~"},
	"up":       {plain: "\x1b[A", param: "1", final: "A"},
	"down":     {plain: "\x1b[B", param: "1", final: "B"},
	"right":    {plain: "\x1b[C", param: "1", final: "C"},
	"left":     {plain: "\x1b[D", param: "1", final: "D"},
	"f1":       {plain: "\x1bOP", param: "1", final: "P"},
	"f2":       {plain: "\x1bOQ", param: "1", final: "Q"},
	"f3":       {plain: "\x1bOR", param: "1", final: "R"},
	"f4":       {plain: "\x1bOS", param: "1", final: "S"},
	"f5":       {plain: "\x1b[15~", param: "15", final: "~"},
	"f6":       {plain: "\x1b[17~", param: "17", final: "~"},
	"f7":       {plain: "\x1b[18~", param: "18", final: "~"},
	"f8":       {plain: "\x1b[19~", param: "19", final: "~"},
	"f9":       {plain: "\x1b[20~", param: "20", final: "~"},
	"f10":      {plain: "\x1b[21~", param: "21", final: "~"},
	"f11":      {plain: "\x1b[23~", param: "23", final: "~"},
	"f12":      {plain: "\x1b[24~", param: "24", final: "~"},
}

// parseKey converts one key name or combination into terminal input bytes.
// Names and modifiers are case-insensitive; a plain single ASCII character is
// sent literally.
func parseKey(s string) ([]byte, error) {
	expression := strings.ToLower(strings.TrimSpace(s))
	if expression == "" {
		return nil, fmt.Errorf("key is empty")
	}

	parts := strings.Split(expression, "+")
	var modifiers keyModifiers
	var base string
	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("empty key component in %q", expression)
		}
		switch part {
		case "shift":
			if modifiers&modifierShift != 0 {
				return nil, fmt.Errorf("duplicate modifier %q", part)
			}
			modifiers |= modifierShift
		case "alt":
			if modifiers&modifierAlt != 0 {
				return nil, fmt.Errorf("duplicate modifier %q", part)
			}
			modifiers |= modifierAlt
		case "ctrl":
			if modifiers&modifierCtrl != 0 {
				return nil, fmt.Errorf("duplicate modifier %q", part)
			}
			modifiers |= modifierCtrl
		default:
			if base != "" {
				return nil, fmt.Errorf("multiple keys in %q", expression)
			}
			base = part
		}
	}
	if base == "" {
		return nil, fmt.Errorf("missing key in %q", expression)
	}

	return encodeKey(base, modifiers)
}

func encodeKey(base string, modifiers keyModifiers) ([]byte, error) {
	if len(base) == 1 {
		ch := base[0]
		if ch < 0x20 || ch > 0x7e {
			return nil, fmt.Errorf("unsupported character %q", base)
		}

		switch {
		case modifiers&modifierCtrl != 0:
			if ch < 'a' || ch > 'z' {
				return nil, undefinedCombination(base, modifiers)
			}
			return applyMeta(modifiers, []byte{ch & 0x1f}), nil
		case modifiers&modifierShift != 0:
			if ch < 'a' || ch > 'z' {
				return nil, undefinedCombination(base, modifiers)
			}
			return applyMeta(modifiers, []byte{ch - 'a' + 'A'}), nil
		default:
			return applyMeta(modifiers, []byte{ch}), nil
		}
	}

	if sequence, ok := simpleKeySequences[base]; ok {
		switch base {
		case "tab":
			if modifiers&modifierCtrl != 0 {
				return nil, undefinedCombination(base, modifiers)
			}
			if modifiers&modifierShift != 0 {
				return applyMeta(modifiers, []byte("\x1b[Z")), nil
			}
		default:
			if modifiers&(modifierCtrl|modifierShift) != 0 {
				return nil, undefinedCombination(base, modifiers)
			}
		}
		return applyMeta(modifiers, []byte(sequence)), nil
	}

	sequence, ok := namedKeySequences[base]
	if !ok {
		return nil, fmt.Errorf("unknown key %q", base)
	}

	// Alt alone uses the traditional Meta encoding. Ctrl and/or Shift on
	// navigation and function keys use xterm's CSI modifier parameter.
	if modifiers == 0 {
		return []byte(sequence.plain), nil
	}
	if modifiers == modifierAlt {
		return applyMeta(modifiers, []byte(sequence.plain)), nil
	}
	code, ok := modifierCode(modifiers)
	if !ok {
		return nil, undefinedCombination(base, modifiers)
	}
	return []byte("\x1b[" + sequence.param + ";" + strconv.Itoa(code) + sequence.final), nil
}

func applyMeta(modifiers keyModifiers, data []byte) []byte {
	if modifiers&modifierAlt == 0 {
		return data
	}
	return append([]byte{0x1b}, data...)
}

func modifierCode(modifiers keyModifiers) (int, bool) {
	switch modifiers {
	case modifierShift:
		return 2, true
	case modifierAlt:
		return 3, true
	case modifierShift | modifierAlt:
		return 4, true
	case modifierCtrl:
		return 5, true
	case modifierCtrl | modifierShift:
		return 6, true
	case modifierCtrl | modifierAlt:
		return 7, true
	case modifierCtrl | modifierAlt | modifierShift:
		return 8, true
	default:
		return 0, false
	}
}

func undefinedCombination(base string, modifiers keyModifiers) error {
	parts := make([]string, 0, 4)
	if modifiers&modifierCtrl != 0 {
		parts = append(parts, "ctrl")
	}
	if modifiers&modifierAlt != 0 {
		parts = append(parts, "alt")
	}
	if modifiers&modifierShift != 0 {
		parts = append(parts, "shift")
	}
	parts = append(parts, base)
	return fmt.Errorf("undefined combination %q", strings.Join(parts, "+"))
}
