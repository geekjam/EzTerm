//go:build windows

package files

import "errors"

// mkfifo is unsupported on Windows; the calling test skips there.
func mkfifo(string) error {
	return errors.New("fifo unsupported on windows")
}
