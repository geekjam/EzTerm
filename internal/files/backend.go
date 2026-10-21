package files

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"

	"ezterm/internal/api"
)

// Backend is the file-operation surface shared by local and SSH sessions. Both
// implementations have identical semantics apart from symlink handling: the
// local backend follows a final symlink (operating system semantics) while the
// SFTP backend refuses one. Remove never follows a symlink on either backend.
type Backend interface {
	// Root returns the session's transfer root: the local user's home or the
	// remote login directory.
	Root() (string, error)
	// List returns the entries of a directory, or a single entry when path
	// names a regular file or a symlink.
	List(path string) (*api.FileListResponse, error)
	// OpenRead opens a regular file for download and returns its metadata.
	OpenRead(path string) (io.ReadCloser, api.FileEntry, error)
	// Upload streams r into path, replacing it atomically.
	Upload(ctx context.Context, path string, r io.Reader, overwrite bool) (api.FileEntry, error)
	// Mkdir creates one directory; the parent must exist.
	Mkdir(path string) (api.FileEntry, error)
	// Remove deletes a regular file, a symlink, or an empty directory.
	Remove(path string) error
	// Close releases backend resources (a no-op for the local backend).
	Close() error
}

// sortEntries orders directory entries the way the Web page and CLI display
// them: directories first, then symlinks, then regular files, each group by
// name (case-insensitive, with the original name breaking ties).
func sortEntries(entries []api.FileEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if ra, rb := entryRank(a), entryRank(b); ra != rb {
			return ra < rb
		}
		la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if la != lb {
			return la < lb
		}
		return a.Name < b.Name
	})
}

// entryRank groups entries for sorting: 0 = directory, 1 = symlink, 2 = file.
func entryRank(e api.FileEntry) int {
	switch {
	case e.Dir:
		return 0
	case e.Symlink:
		return 1
	default:
		return 2
	}
}

// formatMode renders permission bits as four octal digits, e.g. "0644".
func formatMode(mode fs.FileMode) string {
	return fmt.Sprintf("%04o", mode.Perm())
}

// newEntry builds a wire entry from a directory name, its absolute path, and
// the metadata describing it.
func newEntry(name, path string, info fs.FileInfo, symlink bool) api.FileEntry {
	return api.FileEntry{
		Name:     name,
		Path:     path,
		Dir:      info.IsDir(),
		Symlink:  symlink,
		Size:     info.Size(),
		Mode:     formatMode(info.Mode()),
		Modified: info.ModTime().UTC(),
	}
}
