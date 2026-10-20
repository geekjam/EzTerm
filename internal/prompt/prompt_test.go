package prompt

import "testing"

func TestDetectorMatchesRules(t *testing.T) {
	cases := []struct {
		name  string
		chunk string
		want  string
	}{
		{"sudo", "[sudo] password for deploy: ", "[sudo] password for deploy:"},
		{"generic password", "Password: ", "Password:"},
		{"password for user", "Password for root: ", "Password for root:"},
		{"passwd", "passwd: ", "passwd:"},
		{"openssh", "deploy@db.example.com's password: ", "deploy@db.example.com's password:"},
		{"passphrase", "Enter passphrase for key '/home/u/.ssh/id_ed25519': ",
			"Enter passphrase for key '/home/u/.ssh/id_ed25519':"},
		{"uppercase", "PASSWORD:", "PASSWORD:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := New()
			label, changed := d.Feed([]byte(tc.chunk))
			if !changed || label != tc.want {
				t.Fatalf("Feed(%q) = (%q, %v), want (%q, true)", tc.chunk, label, changed, tc.want)
			}
		})
	}
}

func TestDetectorIgnoresNonPrompts(t *testing.T) {
	cases := []struct {
		name  string
		chunk string
	}{
		{"plain output", "hello world\n"},
		{"prompt not at line end", "Password: retry 3 times\n"},
		{"prompt answered", "[sudo] password for deploy: \n"},
		{"no colon", "password required\n"},
		{"mid line", "error: password: value is empty"},
		{"crlf answered", "[sudo] password for deploy: \r\n"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := New()
			if label, changed := d.Feed([]byte(tc.chunk)); changed || label != "" {
				t.Fatalf("Feed(%q) = (%q, %v), want (\"\", false)", tc.chunk, label, changed)
			}
		})
	}
}

func TestDetectorClearsAfterNewline(t *testing.T) {
	d := New()
	if label, changed := d.Feed([]byte("[sudo] password for deploy: ")); !changed || label == "" {
		t.Fatalf("prompt not detected: %q %v", label, changed)
	}
	// The program continues on a new line: the prompt is gone.
	label, changed := d.Feed([]byte("\r\n"))
	if !changed || label != "" {
		t.Fatalf("prompt not cleared after newline: (%q, %v)", label, changed)
	}
	if got := d.Current(); got != "" {
		t.Fatalf("Current() = %q, want empty", got)
	}
}

func TestDetectorReportsOnlyChanges(t *testing.T) {
	d := New()
	if _, changed := d.Feed([]byte("[sudo] password for a: ")); !changed {
		t.Fatal("first detection should report a change")
	}
	if _, changed := d.Feed([]byte("")); changed {
		t.Fatal("repeated detection must not report a change")
	}
	if _, changed := d.Feed([]byte("  ")); changed {
		t.Fatal("trailing spaces keep the same prompt, no change expected")
	}
}

func TestDetectorHandlesSplitChunks(t *testing.T) {
	d := New()
	chunks := []string{"[sudo] pass", "word for ", "deploy: "}
	for _, chunk := range chunks[:len(chunks)-1] {
		if label, changed := d.Feed([]byte(chunk)); changed || label != "" {
			t.Fatalf("partial chunk %q triggered %q", chunk, label)
		}
	}
	label, changed := d.Feed([]byte(chunks[len(chunks)-1]))
	if !changed || label != "[sudo] password for deploy:" {
		t.Fatalf("final chunk = (%q, %v)", label, changed)
	}
}

func TestDetectorStripsANSI(t *testing.T) {
	d := New()
	// A coloured prompt: the escape sequence sits inside the matched line.
	label, changed := d.Feed([]byte("\x1b[1;31m[sudo] password for deploy:\x1b[0m "))
	if !changed || label != "[sudo] password for deploy:" {
		t.Fatalf("ANSI prompt = (%q, %v)", label, changed)
	}
}

func TestDetectorTailIsBounded(t *testing.T) {
	d := New()
	d.Feed([]byte("Password: "))
	// Push the prompt far out of the window with ordinary output.
	for i := 0; i < 8; i++ {
		d.Feed([]byte("0123456789012345678901234567890123456789"))
	}
	if label, changed := d.Feed(nil); changed || label != "" {
		t.Fatalf("stale prompt still reported: (%q, %v)", label, changed)
	}
	if len(d.tail) > tailLimit {
		t.Fatalf("tail = %d bytes, want <= %d", len(d.tail), tailLimit)
	}
}

func TestDetectorReset(t *testing.T) {
	d := New()
	d.Feed([]byte("Password: "))
	d.Reset()
	if label, changed := d.Feed(nil); changed || label != "" {
		t.Fatalf("prompt survived Reset: (%q, %v)", label, changed)
	}
	// After a reset the same prompt text can fire again once reprinted.
	if label, changed := d.Feed([]byte("Password: ")); !changed || label != "Password:" {
		t.Fatalf("reprinted prompt = (%q, %v)", label, changed)
	}
}
