package ansi

import "regexp"

var ansiRe = regexp.MustCompile(
	`\x1b(?:` +
		`[\[(][0-?]*[ -/]*[@-~]` +
		`|\].*?(?:\x1b\\|\x07)` +
		`|[()][Bb0UK]` +
		`|[ -/]*[0-~]` +
		`)`,
)

// Strip removes common ANSI escape sequences from terminal output.
func Strip(text string) string {
	return ansiRe.ReplaceAllString(text, "")
}
