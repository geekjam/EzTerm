package files

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"sync"

	"ezterm/internal/api"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTP implements Backend on a remote host through a session's existing SSH
// connection. A final symlink is never followed: listing reports it, reading
// and uploading reject it, and Remove deletes the link itself.
type SFTP struct {
	client *sftp.Client

	rootOnce sync.Once
	rootPath string
	rootErr  error
}

// OpenSFTP opens the sftp subsystem on an established SSH connection.
func OpenSFTP(sc *ssh.Client) (*SFTP, error) {
	client, err := sftp.NewClient(sc)
	if err != nil {
		return nil, Errorf(ErrTransport, "open sftp subsystem (the remote host may not enable sftp-server): %v", err)
	}
	return &SFTP{client: client}, nil
}

// Root returns the remote login directory, resolving it once. Servers that do
// not implement realpath fall back to "/".
func (s *SFTP) Root() (string, error) {
	s.rootOnce.Do(func() {
		dir, err := s.client.Getwd()
		if err != nil {
			var status *sftp.StatusError
			if errors.As(err, &status) && status.FxCode() == sftp.ErrSSHFxOpUnsupported {
				s.rootPath = "/"
				return
			}
			s.rootErr = translate(err, false)
			return
		}
		s.rootPath = path.Clean(dir)
	})
	return s.rootPath, s.rootErr
}

// Close closes the SFTP channel. The SSH connection itself belongs to the
// session.
func (s *SFTP) Close() error {
	if err := s.client.Close(); err != nil {
		return translate(err, false)
	}
	return nil
}

// List returns direct children of a remote directory, or a single entry when
// path names a regular file or symlink.
func (s *SFTP) List(input string) (*api.FileListResponse, error) {
	root, err := s.Root()
	if err != nil {
		return nil, err
	}
	target, err := ResolveRemote(root, input)
	if err != nil {
		return nil, err
	}
	info, err := s.client.Lstat(target)
	if err != nil {
		return nil, translate(err, false)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return &api.FileListResponse{
			Path:    target,
			Parent:  parentRemote(target),
			Root:    root,
			Entries: []api.FileEntry{newEntry(path.Base(target), target, info, true)},
		}, nil
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return nil, Errorf(ErrSpecial, "%s is not a regular file or directory", target)
		}
		return &api.FileListResponse{
			Path:    target,
			Parent:  parentRemote(target),
			Root:    root,
			Entries: []api.FileEntry{newEntry(path.Base(target), target, info, false)},
		}, nil
	}
	dirEntries, err := s.client.ReadDir(target)
	if err != nil {
		return nil, translate(err, false)
	}
	entries := make([]api.FileEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		name := de.Name()
		if name == "." || name == ".." {
			continue
		}
		child := path.Join(target, name)
		// Some servers omit the file-type bits from readdir attributes; fall
		// back to Lstat so symlinks are still detected (and then not followed).
		if de.Mode()&os.ModeType == 0 {
			if lstat, err := s.client.Lstat(child); err == nil {
				de = lstat
			}
		}
		symlink := de.Mode()&os.ModeSymlink != 0
		entries = append(entries, newEntry(name, child, de, symlink))
	}
	sortEntries(entries)
	return &api.FileListResponse{
		Path:    target,
		Parent:  parentRemote(target),
		Root:    root,
		Entries: entries,
	}, nil
}

// OpenRead opens a remote regular file for download. Directories, symlinks, and
// special files are rejected.
func (s *SFTP) OpenRead(input string) (io.ReadCloser, api.FileEntry, error) {
	root, err := s.Root()
	if err != nil {
		return nil, api.FileEntry{}, err
	}
	target, err := ResolveRemote(root, input)
	if err != nil {
		return nil, api.FileEntry{}, err
	}
	info, err := s.client.Lstat(target)
	if err != nil {
		return nil, api.FileEntry{}, translate(err, false)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, api.FileEntry{}, Errorf(ErrSymlink, "%s is a symbolic link", target)
	}
	if info.IsDir() {
		return nil, api.FileEntry{}, Errorf(ErrIsDir, "%s is a directory", target)
	}
	if !info.Mode().IsRegular() {
		return nil, api.FileEntry{}, Errorf(ErrSpecial, "%s is not a regular file", target)
	}
	f, err := s.client.Open(target)
	if err != nil {
		return nil, api.FileEntry{}, translate(err, false)
	}
	return f, newEntry(path.Base(target), target, info, false), nil
}

// Upload streams r into a temporary file next to the target and publishes it
// with rename. Symlinks are never followed and are rejected as upload targets.
func (s *SFTP) Upload(ctx context.Context, input string, r io.Reader, overwrite bool) (api.FileEntry, error) {
	root, err := s.Root()
	if err != nil {
		return api.FileEntry{}, err
	}
	target, err := ResolveRemote(root, input)
	if err != nil {
		return api.FileEntry{}, err
	}
	var existing fs.FileInfo
	info, statErr := s.client.Lstat(target)
	switch {
	case statErr == nil && info.Mode()&os.ModeSymlink != 0:
		return api.FileEntry{}, Errorf(ErrSymlink, "%s is a symbolic link", target)
	case statErr == nil && info.IsDir():
		return api.FileEntry{}, Errorf(ErrIsDir, "%s is a directory", target)
	case statErr == nil && !info.Mode().IsRegular():
		return api.FileEntry{}, Errorf(ErrSpecial, "%s is not a regular file", target)
	case statErr == nil && !overwrite:
		return api.FileEntry{}, Errorf(ErrExists, "%s already exists", target)
	case statErr == nil:
		existing = info
	case !errors.Is(statErr, fs.ErrNotExist):
		return api.FileEntry{}, translate(statErr, false)
	}

	tempPath := path.Join(path.Dir(target), tempName())
	temp, err := s.client.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return api.FileEntry{}, translate(err, false)
	}
	cleanup := func() {
		_ = temp.Close()
		_ = s.client.Remove(tempPath)
	}
	buf := make([]byte, 64*1024)
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			cleanup()
			return api.FileEntry{}, ctxErr
		}
		n, readErr := r.Read(buf)
		if n > 0 {
			if _, writeErr := temp.Write(buf[:n]); writeErr != nil {
				cleanup()
				return api.FileEntry{}, translate(writeErr, false)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			cleanup()
			return api.FileEntry{}, translate(readErr, false)
		}
	}
	if err := temp.Close(); err != nil {
		_ = s.client.Remove(tempPath)
		return api.FileEntry{}, translate(err, false)
	}
	mode := fs.FileMode(0o644)
	if existing != nil {
		mode = existing.Mode().Perm()
	}
	if err := s.client.Chmod(tempPath, mode); err != nil {
		_ = s.client.Remove(tempPath)
		return api.FileEntry{}, translate(err, false)
	}
	if existing != nil {
		if err := s.publishOverwrite(tempPath, target); err != nil {
			_ = s.client.Remove(tempPath)
			return api.FileEntry{}, err
		}
	} else if err := s.client.Rename(tempPath, target); err != nil {
		_ = s.client.Remove(tempPath)
		return api.FileEntry{}, translate(err, false)
	}
	return s.entryFor(target)
}

// publishOverwrite replaces an existing file. OpenSSH's posix-rename extension
// makes this atomic; otherwise the target is removed before the rename.
func (s *SFTP) publishOverwrite(tempPath, target string) error {
	if _, ok := s.client.HasExtension("posix-rename@openssh.com"); ok {
		if err := s.client.PosixRename(tempPath, target); err == nil {
			return nil
		}
		// Fall through to the portable path when the extension is advertised
		// but rejected.
	}
	if err := s.client.Remove(target); err != nil {
		return translate(err, false)
	}
	if err := s.client.Rename(tempPath, target); err != nil {
		return translate(err, false)
	}
	return nil
}

// Mkdir creates one remote directory; the parent must exist.
func (s *SFTP) Mkdir(input string) (api.FileEntry, error) {
	root, err := s.Root()
	if err != nil {
		return api.FileEntry{}, err
	}
	target, err := ResolveRemote(root, input)
	if err != nil {
		return api.FileEntry{}, err
	}
	if _, statErr := s.client.Lstat(target); statErr == nil {
		return api.FileEntry{}, Errorf(ErrExists, "%s already exists", target)
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return api.FileEntry{}, translate(statErr, false)
	}
	if err := s.client.Mkdir(target); err != nil {
		return api.FileEntry{}, translate(err, false)
	}
	return s.entryFor(target)
}

// Remove deletes a remote file, symlink (the link itself), or empty directory.
// The session root and "/" are protected.
func (s *SFTP) Remove(input string) error {
	root, err := s.Root()
	if err != nil {
		return err
	}
	target, err := ResolveRemote(root, input)
	if err != nil {
		return err
	}
	if target == "/" || path.Clean(target) == path.Clean(root) {
		return Errorf(ErrRootDelete, "%s is the session root", target)
	}
	info, err := s.client.Lstat(target)
	if err != nil {
		return translate(err, false)
	}
	if info.IsDir() {
		children, err := s.client.ReadDir(target)
		if err != nil {
			return translate(err, false)
		}
		if len(children) > 0 {
			return Errorf(ErrNotEmpty, "%s is not empty", target)
		}
		if err := s.client.RemoveDirectory(target); err != nil {
			return translate(err, false)
		}
		return nil
	}
	if err := s.client.Remove(target); err != nil {
		return translate(err, false)
	}
	return nil
}

// entryFor stats a remote path and builds the wire entry.
func (s *SFTP) entryFor(target string) (api.FileEntry, error) {
	info, err := s.client.Lstat(target)
	if err != nil {
		return api.FileEntry{}, translate(err, false)
	}
	return newEntry(path.Base(target), target, info, info.Mode()&os.ModeSymlink != 0), nil
}

// parentRemote returns the parent directory, or "" at "/".
func parentRemote(p string) string {
	parent := path.Dir(p)
	if parent == p {
		return ""
	}
	return parent
}
