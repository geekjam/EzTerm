package files

import (
	"path"
	"path/filepath"
	"strings"
)

// ResolveLocal maps a request path onto an absolute local path. An empty path
// resolves to root; "~" and "~/..." expand to root; a relative path is joined
// to root; an absolute path is kept. The result is cleaned with filepath.
func ResolveLocal(root, input string) (string, error) {
	if strings.ContainsRune(input, 0) {
		return "", Errorf(ErrInvalidPath, "path contains a NUL byte")
	}
	if root == "" {
		return "", Errorf(ErrInvalidPath, "local transfer root is unknown")
	}
	expanded, fromRoot, err := expandTilde(root, input)
	if err != nil {
		return "", err
	}
	if expanded == "" {
		return filepath.Clean(root), nil
	}
	if !fromRoot && !filepath.IsAbs(expanded) {
		expanded = filepath.Join(root, expanded)
	}
	return filepath.Clean(expanded), nil
}

// ResolveRemote maps a request path onto an absolute remote (POSIX) path. An
// empty path resolves to root; "~" and "~/..." expand to root; a relative path
// is joined to root; an absolute path is kept.
func ResolveRemote(root, input string) (string, error) {
	if strings.ContainsRune(input, 0) {
		return "", Errorf(ErrInvalidPath, "path contains a NUL byte")
	}
	if root == "" {
		root = "/"
	}
	expanded, fromRoot, err := expandTilde(root, input)
	if err != nil {
		return "", err
	}
	if expanded == "" {
		return path.Clean(root), nil
	}
	if !fromRoot && !strings.HasPrefix(expanded, "/") {
		expanded = path.Join(root, expanded)
	}
	return path.Clean(expanded), nil
}

// expandTilde expands a leading "~" or "~/..." to root and reports whether the
// result is already rooted at root (so the caller must not join it again).
// Other users' homes ("~name") are rejected; both separators are accepted so
// Windows callers can write "~\logs" or "~/logs".
func expandTilde(root, input string) (expanded string, fromRoot bool, err error) {
	if input == "" {
		return "", false, nil
	}
	if input[0] != '~' {
		return input, false, nil
	}
	if len(input) == 1 {
		return root, true, nil
	}
	next := input[1]
	if next != '/' && next != '\\' {
		return "", false, Errorf(ErrInvalidPath, "unsupported home directory %q", tildeName(input))
	}
	return root + input[1:], true, nil
}

// tildeName returns the user portion of a "~name" path for error messages.
func tildeName(input string) string {
	if i := strings.IndexAny(input, "/\\"); i >= 0 {
		return input[:i]
	}
	return input
}

// baseName returns the final element of a path that may use either separator,
// which keeps CLI defaults working for local and remote paths alike.
func baseName(p string) string {
	p = strings.TrimRight(p, "/\\")
	if i := strings.LastIndexAny(p, "/\\"); i >= 0 {
		return p[i+1:]
	}
	return p
}
