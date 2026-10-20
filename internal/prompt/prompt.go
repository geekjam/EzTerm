// Package prompt detects interactive password prompts in terminal output.
//
// Detection is deliberately conservative: a rule matches only when the prompt
// text sits at the very end of the buffered output and the last line has no
// trailing newline yet. Text such as "password:" appearing mid-line therefore
// never triggers a dialog.
package prompt

import (
	"regexp"
	"strings"
	"sync"

	"ezterm/internal/ansi"
)

// tailLimit bounds the retained output used for matching.
const tailLimit = 256

// Rule matches one family of password prompts against the last output line.
type Rule struct {
	// Name identifies the rule (sudo, password, openssh, passphrase).
	Name string
	// Re matches the final line, anchored at both ends.
	Re *regexp.Regexp
	// Enter reports whether the answer must be submitted with a carriage
	// return. Every current rule needs it.
	Enter bool
}

// Rules is the built-in detection table. It has no configuration surface by
// design: a wrong rule would either hide a prompt or leak a secret into an
// unrelated program's stdin.
var Rules = []Rule{
	{
		Name:  "sudo",
		Re:    regexp.MustCompile(`(?i)^\[sudo\] password for .+:\s*$`),
		Enter: true,
	},
	{
		Name:  "password",
		Re:    regexp.MustCompile(`(?i)^(?:password|passwd)(?: for .+)?:\s*$`),
		Enter: true,
	},
	{
		Name:  "openssh",
		Re:    regexp.MustCompile(`(?i)^.+@.+'s password:\s*$`),
		Enter: true,
	},
	{
		Name:  "passphrase",
		Re:    regexp.MustCompile(`(?i)^enter passphrase for .+:\s*$`),
		Enter: true,
	},
}

// Detector tracks the tail of a session's output and reports the password
// prompt currently visible on screen, if any. A session's output may be
// produced by multiple goroutines (for example one per SSH stream), so the
// detector serializes access internally.
type Detector struct {
	mu   sync.Mutex
	tail []byte
	cur  string // label of the active prompt, "" when none
}

// New returns a detector using the built-in rule table.
func New() *Detector { return &Detector{} }

// Feed appends chunk to the retained tail and reports the current prompt plus
// whether it changed since the previous call. A prompt is reported only while
// it remains the unterminated final line.
func (d *Detector) Feed(chunk []byte) (label string, changed bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(chunk) > 0 {
		d.tail = append(d.tail, chunk...)
		if len(d.tail) > tailLimit {
			d.tail = append(d.tail[:0], d.tail[len(d.tail)-tailLimit:]...)
		}
	}
	next := d.match()
	if next == d.cur {
		return next, false
	}
	d.cur = next
	return next, true
}

// Current returns the active prompt label without consuming new output.
func (d *Detector) Current() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cur
}

// Reset forgets the retained output and any active prompt. It is called after
// a secret is submitted so the same prompt cannot fire twice.
func (d *Detector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tail = d.tail[:0]
	d.cur = ""
}

// match evaluates the rule table against the last output line.
func (d *Detector) match() string {
	if len(d.tail) == 0 {
		return ""
	}
	line := lastLine(string(d.tail))
	if line == "" {
		return ""
	}
	for _, rule := range Rules {
		if rule.Re.MatchString(line) {
			return line
		}
	}
	return ""
}

// lastLine returns the text after the final newline of raw output, with ANSI
// escape sequences removed. A trailing newline yields an empty line, which
// callers treat as "no prompt" (the prompt was answered).
func lastLine(raw string) string {
	if idx := strings.LastIndexAny(raw, "\r\n"); idx >= 0 {
		raw = raw[idx+1:]
	}
	return strings.TrimRight(ansi.Strip(raw), " \t")
}
