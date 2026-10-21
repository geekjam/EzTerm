package files

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"ezterm/internal/api"
)

// Local implements Backend on the filesystem of the machine running the
// daemon. Its root defaults to the current user's home directory. Symlinks are
// followed for listing, reading, and uploading (operating system semantics);
// Remove deletes the link itself. Paths inside denyRoots (the daemon data
// directory) are rejected and hidden from listings.
type Local struct {
	root      string
	denyRoots []string
	denyReal  []string

	rootOnce sync.Once
	rootPath string
	rootErr  error
}

// NewLocal builds a local backend rooted at root. An empty root means the
// current user's home directory. denyRoots are absolute paths that must never
// be listed, read, written, or deleted.
func NewLocal(root string, denyRoots ...string) *Local {
	l := &Local{root: root}
	for _, deny := range denyRoots {
		deny = strings.TrimSpace(deny)
		if deny == "" {
			continue
		}
		clean := filepath.Clean(deny)
		l.denyRoots = append(l.denyRoots, clean)
		if real, err := filepath.EvalSymlinks(clean); err == nil {
			l.denyReal = append(l.denyReal, real)
		} else {
			l.denyReal = append(l.denyReal, clean)
		}
	}
	return l
}

// Root returns the transfer root, resolving the user's home directory once.
func (l *Local) Root() (string, error) {
	l.rootOnce.Do(func() {
		root := l.root
		if root == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				l.rootErr = Errorf(ErrPermission, "cannot resolve home directory: %v", err)
				return
			}
			root = home
		}
		l.rootPath = filepath.Clean(root)
	})
	return l.rootPath, l.rootErr
}

// Close is a no-op for the local backend.
func (l *Local) Close() error { return nil }

// List returns direct children of a directory, or a single entry when path
// names a regular file or symlink.
func (l *Local) List(input string) (*api.FileListResponse, error) {
	root, err := l.Root()
	if err != nil {
		return nil, err
	}
	logical, realPath, err := l.resolve(input)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return nil, translate(err, true)
	}
	symlink, _ := l.isSymlink(logical)
	switch {
	case info.IsDir():
		dirEntries, err := os.ReadDir(realPath)
		if err != nil {
			return nil, translate(err, true)
		}
		entries := make([]api.FileEntry, 0, len(dirEntries))
		for _, de := range dirEntries {
			childLogical := filepath.Join(logical, de.Name())
			childReal := filepath.Join(realPath, de.Name())
			if l.denied(childLogical) || l.denied(childReal) {
				continue
			}
			linkInfo, err := de.Info() // Lstat semantics for symlinks
			if err != nil {
				continue // entry vanished between ReadDir and here
			}
			if linkInfo.Mode()&os.ModeSymlink != 0 {
				if target, err := os.Stat(childReal); err == nil {
					if realTarget, err := filepath.EvalSymlinks(childReal); err == nil && l.denied(realTarget) {
						continue
					}
					entries = append(entries, newEntry(de.Name(), childLogical, target, true))
				} else {
					entries = append(entries, newEntry(de.Name(), childLogical, linkInfo, true))
				}
				continue
			}
			entries = append(entries, newEntry(de.Name(), childLogical, linkInfo, false))
		}
		sortEntries(entries)
		return &api.FileListResponse{
			Path:    logical,
			Parent:  parentOf(logical),
			Root:    root,
			Entries: entries,
		}, nil
	case info.Mode().IsRegular():
		return &api.FileListResponse{
			Path:    logical,
			Parent:  parentOf(logical),
			Root:    root,
			Entries: []api.FileEntry{newEntry(filepath.Base(logical), logical, info, symlink)},
		}, nil
	default:
		return nil, Errorf(ErrSpecial, "%s is not a regular file or directory", logical)
	}
}

// OpenRead opens a regular file for download, following a final symlink.
func (l *Local) OpenRead(input string) (io.ReadCloser, api.FileEntry, error) {
	logical, realPath, err := l.resolve(input)
	if err != nil {
		return nil, api.FileEntry{}, err
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return nil, api.FileEntry{}, translate(err, true)
	}
	if info.IsDir() {
		return nil, api.FileEntry{}, Errorf(ErrIsDir, "%s is a directory", logical)
	}
	if !info.Mode().IsRegular() {
		return nil, api.FileEntry{}, Errorf(ErrSpecial, "%s is not a regular file", logical)
	}
	symlink, _ := l.isSymlink(logical)
	f, err := os.Open(realPath)
	if err != nil {
		return nil, api.FileEntry{}, translate(err, true)
	}
	return f, newEntry(filepath.Base(logical), logical, info, symlink), nil
}

// Upload streams r into path using a temporary file in the target directory
// followed by an atomic rename. A final symlink is followed (the target is
// replaced, the link stays); a dangling symlink is rejected.
func (l *Local) Upload(ctx context.Context, input string, r io.Reader, overwrite bool) (api.FileEntry, error) {
	logical, realPath, err := l.resolve(input)
	if err != nil {
		return api.FileEntry{}, err
	}
	var existing fs.FileInfo
	info, statErr := os.Lstat(realPath)
	switch {
	case statErr == nil && info.IsDir():
		return api.FileEntry{}, Errorf(ErrIsDir, "%s is a directory", logical)
	case statErr == nil && !info.Mode().IsRegular():
		return api.FileEntry{}, Errorf(ErrSpecial, "%s is not a regular file", logical)
	case statErr == nil && !overwrite:
		return api.FileEntry{}, Errorf(ErrExists, "%s already exists", logical)
	case statErr == nil:
		existing = info
	case !errors.Is(statErr, fs.ErrNotExist):
		return api.FileEntry{}, translate(statErr, true)
	}

	dir := filepath.Dir(realPath)
	tempPath := filepath.Join(dir, tempName())
	temp, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return api.FileEntry{}, translate(err, true)
	}
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
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
				return api.FileEntry{}, translate(writeErr, true)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			cleanup()
			return api.FileEntry{}, translate(readErr, true)
		}
	}
	_ = temp.Sync()
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return api.FileEntry{}, translate(err, true)
	}
	mode := fs.FileMode(0o644)
	if existing != nil {
		mode = existing.Mode().Perm()
	}
	if err := os.Chmod(tempPath, mode); err != nil {
		_ = os.Remove(tempPath)
		return api.FileEntry{}, translate(err, true)
	}
	if err := os.Rename(tempPath, realPath); err != nil {
		_ = os.Remove(tempPath)
		return api.FileEntry{}, translate(err, true)
	}
	return l.entryFor(logical, realPath)
}

// Mkdir creates one directory; the parent must already exist and the target
// must not exist (including as a symlink).
func (l *Local) Mkdir(input string) (api.FileEntry, error) {
	logical, err := l.resolvePathOnly(input)
	if err != nil {
		return api.FileEntry{}, err
	}
	if _, statErr := os.Lstat(logical); statErr == nil {
		return api.FileEntry{}, Errorf(ErrExists, "%s already exists", logical)
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return api.FileEntry{}, translate(statErr, true)
	}
	parentReal, err := filepath.EvalSymlinks(filepath.Dir(logical))
	if err != nil {
		return api.FileEntry{}, translate(err, true)
	}
	targetReal := filepath.Join(parentReal, filepath.Base(logical))
	if l.denied(logical) || l.denied(targetReal) {
		return api.FileEntry{}, Errorf(ErrDenied, "%s is not available for transfer", logical)
	}
	if err := os.Mkdir(targetReal, 0o755); err != nil {
		return api.FileEntry{}, translate(err, true)
	}
	return l.entryFor(logical, targetReal)
}

// Remove deletes a regular file, a symlink (the link itself), or an empty
// directory. Root directories and the daemon data directory are protected.
func (l *Local) Remove(input string) error {
	logical, err := l.resolvePathOnly(input)
	if err != nil {
		return err
	}
	root, err := l.Root()
	if err != nil {
		return err
	}
	if filepath.Dir(logical) == logical {
		return Errorf(ErrRootDelete, "%s is a filesystem root", logical)
	}
	if filepath.Clean(logical) == filepath.Clean(root) {
		return Errorf(ErrRootDelete, "%s is the session root", logical)
	}
	if realRoot, err := filepath.EvalSymlinks(root); err == nil {
		if real, err := filepath.EvalSymlinks(logical); err == nil && filepath.Clean(real) == filepath.Clean(realRoot) {
			return Errorf(ErrRootDelete, "%s is the session root", logical)
		}
	}
	if l.denied(logical) {
		return Errorf(ErrDenied, "%s is not available for transfer", logical)
	}
	parentReal, err := filepath.EvalSymlinks(filepath.Dir(logical))
	if err != nil {
		return translate(err, true)
	}
	targetReal := filepath.Join(parentReal, filepath.Base(logical))
	if l.denied(targetReal) {
		return Errorf(ErrDenied, "%s is not available for transfer", logical)
	}
	info, err := os.Lstat(targetReal)
	if err != nil {
		return translate(err, true)
	}
	if info.IsDir() {
		children, err := os.ReadDir(targetReal)
		if err != nil {
			return translate(err, true)
		}
		if len(children) > 0 {
			return Errorf(ErrNotEmpty, "%s is not empty", logical)
		}
		if err := os.Remove(targetReal); err != nil {
			return translate(err, true)
		}
		return nil
	}
	if err := os.Remove(targetReal); err != nil {
		return translate(err, true)
	}
	return nil
}

// resolve maps input to a logical absolute path and the real path that must be
// used for filesystem access. A final symlink is followed; a missing target
// resolves its parent so a new file can be created there.
func (l *Local) resolve(input string) (logical, realPath string, err error) {
	logical, err = l.resolvePathOnly(input)
	if err != nil {
		return "", "", err
	}
	lstat, statErr := os.Lstat(logical)
	switch {
	case statErr == nil && lstat.Mode()&os.ModeSymlink != 0:
		realPath, err = filepath.EvalSymlinks(logical)
		if err != nil {
			return "", "", translate(err, true)
		}
	case statErr == nil:
		realPath = logical
	case errors.Is(statErr, fs.ErrNotExist):
		parentReal, parentErr := filepath.EvalSymlinks(filepath.Dir(logical))
		if parentErr != nil {
			return "", "", translate(parentErr, true)
		}
		realPath = filepath.Join(parentReal, filepath.Base(logical))
	default:
		return "", "", translate(statErr, true)
	}
	if l.denied(logical) || l.denied(realPath) {
		return "", "", Errorf(ErrDenied, "%s is not available for transfer", logical)
	}
	return logical, realPath, nil
}

// resolvePathOnly expands and validates input without touching a final symlink
// target; used by mkdir/remove, which operate on the link itself.
func (l *Local) resolvePathOnly(input string) (string, error) {
	root, err := l.Root()
	if err != nil {
		return "", err
	}
	logical, err := ResolveLocal(root, input)
	if err != nil {
		return "", err
	}
	if l.denied(logical) {
		return "", Errorf(ErrDenied, "%s is not available for transfer", logical)
	}
	return logical, nil
}

// entryFor stats a path after a write and builds the wire entry. logical is the
// path reported to the client; realPath is where the bytes actually landed.
func (l *Local) entryFor(logical, realPath string) (api.FileEntry, error) {
	info, err := os.Stat(realPath)
	if err != nil {
		return api.FileEntry{}, translate(err, true)
	}
	symlink, _ := l.isSymlink(logical)
	return newEntry(filepath.Base(logical), logical, info, symlink), nil
}

// isSymlink reports whether the final path component is a symlink.
func (l *Local) isSymlink(p string) (bool, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return false, err
	}
	return info.Mode()&os.ModeSymlink != 0, nil
}

// denied reports whether p lies inside a protected directory, checking both
// the path as written and its symlink-resolved form.
func (l *Local) denied(p string) bool {
	clean := filepath.Clean(p)
	for _, deny := range l.denyRoots {
		if within(deny, clean) {
			return true
		}
	}
	if real, err := filepath.EvalSymlinks(clean); err == nil {
		for _, deny := range l.denyReal {
			if within(deny, real) {
				return true
			}
		}
	}
	return false
}

// within reports whether p equals root or is a descendant of it.
func within(root, p string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// parentOf returns the parent directory, or "" at a filesystem root.
func parentOf(p string) string {
	parent := filepath.Dir(p)
	if parent == p {
		return ""
	}
	return parent
}

// tempName builds a unique hidden name in the target directory so the rename
// that publishes an upload stays on the same filesystem.
func tempName() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ".ezterm-upload.part"
	}
	return ".ezterm-upload-" + hex.EncodeToString(raw[:]) + ".part"
}
