//go:build !windows

package files

import "syscall"

// mkfifo creates a named pipe for special-file rejection tests.
func mkfifo(path string) error {
	return syscall.Mkfifo(path, 0o600)
}
