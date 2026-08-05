package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestPumpAttachInputForwardsChunks(t *testing.T) {
	// Large payload forces multiple reads; an arrow-key escape (not the detach
	// key) must pass through untouched.
	payload := strings.Repeat("x", 5000) + "\x1b[D"
	var sent []byte
	err := pumpAttachInput(strings.NewReader(payload), func(data []byte) error {
		sent = append(sent, data...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(sent) != payload {
		t.Fatalf("forwarded %d bytes, want %d", len(sent), len(payload))
	}
}

func TestPumpAttachInputDetaches(t *testing.T) {
	input := bytes.NewReader([]byte{'h', 'i', detachKey, 'x'})
	var sent []byte
	err := pumpAttachInput(input, func(data []byte) error {
		sent = append(sent, data...)
		return nil
	})
	if !errors.Is(err, errDetached) {
		t.Fatalf("err = %v, want errDetached", err)
	}
	// Bytes before the detach key are still forwarded; the key itself is not.
	if string(sent) != "hi" {
		t.Fatalf("sent = %q, want %q", sent, "hi")
	}
}

func TestPumpAttachInputDetachesAtStart(t *testing.T) {
	err := pumpAttachInput(bytes.NewReader([]byte{detachKey, 'a'}), func([]byte) error { return nil })
	if !errors.Is(err, errDetached) {
		t.Fatalf("err = %v, want errDetached", err)
	}
}

func TestPumpAttachInputSendError(t *testing.T) {
	wantErr := errors.New("send failed")
	err := pumpAttachInput(strings.NewReader("data"), func([]byte) error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}

func TestPumpAttachInputEOF(t *testing.T) {
	if err := pumpAttachInput(strings.NewReader(""), func([]byte) error { return nil }); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestCmdAttachRejectsJSON(t *testing.T) {
	// attach is interactive; --json must be rejected with exit code 2.
	if code := cmdAttach(newClient(18766), true, []string{"some-id"}); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}
