package files

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/pkg/sftp"
)

func TestTranslateLocalErrors(t *testing.T) {
	cases := []struct {
		err  error
		want error
	}{
		{fs.ErrNotExist, ErrNotExist},
		{fs.ErrPermission, ErrPermission},
	}
	for _, tc := range cases {
		if got := translate(tc.err, true); !errors.Is(got, tc.want) {
			t.Fatalf("translate(%v, local) = %v, want %v", tc.err, got, tc.want)
		}
	}
	if err := translate(errors.New("disk on fire"), true); errors.Is(err, ErrTransport) {
		t.Fatal("unexpected transport classification for a local error")
	}
}

// TestTranslateSFTPErrors guards the client's normalisation of the two common
// SFTP status codes: pkg/sftp turns them into fs sentinels before returning,
// so the remote branch must check those too.
func TestTranslateSFTPErrors(t *testing.T) {
	cases := []struct {
		err  error
		want error
	}{
		{fs.ErrNotExist, ErrNotExist},
		{fs.ErrPermission, ErrPermission},
		{fs.ErrExist, ErrExists},
	}
	for _, tc := range cases {
		if got := translate(tc.err, false); !errors.Is(got, tc.want) {
			t.Fatalf("translate(%v, remote) = %v, want %v", tc.err, got, tc.want)
		}
	}
	if got := translate(&sftp.StatusError{Code: uint32(sftp.ErrSSHFxNoSuchFile)}, false); !errors.Is(got, ErrNotExist) {
		t.Fatalf("raw no-such-file status = %v, want ErrNotExist", got)
	}
	if got := translate(errors.New("connection lost"), false); !errors.Is(got, ErrTransport) {
		t.Fatalf("unknown remote error = %v, want ErrTransport", got)
	}
}

func TestTranslateKeepsSentinels(t *testing.T) {
	if got := translate(Errorf(ErrSymlink, "x"), false); !errors.Is(got, ErrSymlink) {
		t.Fatalf("sentinel was not preserved: %v", got)
	}
	if err := translate(nil, false); err != nil {
		t.Fatalf("translate(nil) = %v", err)
	}
}
