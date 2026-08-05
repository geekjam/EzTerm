package ansi

import (
	"regexp"
	"strings"
)

var blankLineRe = regexp.MustCompile(`\n{3,}`)

// Compact removes control-character noise and normalizes terminal output for agents.
func Compact(text string) string {
	if text == "" {
		return ""
	}

	var cleaned strings.Builder
	cleaned.Grow(len(text))
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c < 0x20 && c != '\r' && c != '\n' && c != '\t' {
			continue
		}
		if c == 0x7f {
			continue
		}
		cleaned.WriteByte(c)
	}

	lines := strings.Split(strings.ReplaceAll(cleaned.String(), "\r\n", "\n"), "\n")
	for i, line := range lines {
		if index := strings.LastIndexByte(line, '\r'); index >= 0 {
			line = line[index+1:]
		}
		lines[i] = strings.TrimRight(line, " \t")
	}
	result := blankLineRe.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	result = strings.TrimLeft(result, "\n")
	if strings.HasSuffix(result, "\n\n") {
		result = strings.TrimRight(result, "\n")
	}
	return result
}
