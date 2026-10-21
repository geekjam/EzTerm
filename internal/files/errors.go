// Package files exposes the file operations used by the session file transfer
// API. It has two backends with identical semantics: Local works on the host
// that runs the daemon, and SFTP works on a remote host through a session's
// existing SSH connection.
package files

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/pkg/sftp"
)

// Sentinel errors. Backends translate their native errors into these values so
// the daemon can map them onto HTTP status codes without knowing which backend
// served the request.
var (
	// ErrNotExist reports a missing path, including a dangling symlink.
	ErrNotExist = errors.New("files: path does not exist")
	// ErrPermission reports a permission failure on the target filesystem.
	ErrPermission = errors.New("files: permission denied")
	// ErrDenied reports a path rejected by policy (the daemon data directory).
	ErrDenied = errors.New("files: path is not available for transfer")
	// ErrIsDir reports that a file operation targeted a directory.
	ErrIsDir = errors.New("files: path is a directory")
	// ErrNotDir reports that a directory operation targeted a non-directory.
	ErrNotDir = errors.New("files: path is not a directory")
	// ErrSymlink reports a symlink where following it is not allowed.
	ErrSymlink = errors.New("files: path is a symbolic link")
	// ErrSpecial reports a FIFO, device, socket, or other non-regular file.
	ErrSpecial = errors.New("files: path is not a regular file")
	// ErrExists reports that the target already exists.
	ErrExists = errors.New("files: path already exists")
	// ErrNotEmpty reports a non-empty directory that cannot be removed.
	ErrNotEmpty = errors.New("files: directory is not empty")
	// ErrInvalidPath reports a malformed or unsupported path argument.
	ErrInvalidPath = errors.New("files: invalid path")
	// ErrRootDelete reports an attempt to delete a protected root directory.
	ErrRootDelete = errors.New("files: refusing to delete a root directory")
	// ErrTransport reports an SSH/SFTP layer failure (closed connection,
	// missing sftp subsystem, protocol error).
	ErrTransport = errors.New("files: transport failure")
)

// Errorf builds an error that wraps one of the sentinel errors above.
func Errorf(sentinel error, format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{sentinel}, args...)...)
}

// translate converts a backend-native error into a sentinel error. Unknown
// errors are returned unchanged so callers can distinguish "unexpected local
// IO failure" from "transport failure".
func translate(err error, local bool) error {
	if err == nil {
		return nil
	}
	for _, sentinel := range []error{
		ErrNotExist, ErrPermission, ErrDenied, ErrIsDir, ErrNotDir,
		ErrSymlink, ErrSpecial, ErrExists, ErrNotEmpty, ErrInvalidPath,
		ErrRootDelete, ErrTransport,
	} {
		if errors.Is(err, sentinel) {
			return err
		}
	}
	if local {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return Errorf(ErrNotExist, "%v", err)
		case errors.Is(err, fs.ErrPermission):
			return Errorf(ErrPermission, "%v", err)
		case errors.Is(err, fs.ErrExist):
			return Errorf(ErrExists, "%v", err)
		}
		return err
	}
	// The SFTP client normalises the common status codes to the fs sentinels,
	// so they are checked before the raw status error (which carries the rest).
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Errorf(ErrNotExist, "%v", err)
	case errors.Is(err, fs.ErrPermission):
		return Errorf(ErrPermission, "%v", err)
	case errors.Is(err, fs.ErrExist):
		return Errorf(ErrExists, "%v", err)
	}
	var status *sftp.StatusError
	if errors.As(err, &status) {
		switch status.FxCode() {
		case sftp.ErrSSHFxNoSuchFile:
			return Errorf(ErrNotExist, "%v", err)
		case sftp.ErrSSHFxPermissionDenied:
			return Errorf(ErrPermission, "%v", err)
		}
	}
	return Errorf(ErrTransport, "%v", err)
}
