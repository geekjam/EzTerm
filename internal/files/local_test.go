package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// newTestLocal builds a local backend rooted at a temp directory that also
// hosts a protected "deny" directory.
func newTestLocal(t *testing.T) (*Local, string, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "home")
	deny := filepath.Join(base, "data")
	for _, dir := range []string{root, deny} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return NewLocal(root, deny), root, deny
}

func TestLocalRoot(t *testing.T) {
	l, root, _ := newTestLocal(t)
	got, err := l.Root()
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("Root() = %q, want %q", got, root)
	}
}

func TestLocalListSortsAndHidesDenyDir(t *testing.T) {
	l, root, deny := newTestLocal(t)
	writeFile(t, filepath.Join(root, "zeta.txt"), "z")
	writeFile(t, filepath.Join(root, "Alpha.txt"), "a")
	writeFile(t, filepath.Join(root, ".hidden"), "h")
	if err := os.Mkdir(filepath.Join(root, "beta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(deny, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlink in the root pointing into the protected directory must be
	// hidden as well. Symlinks may need extra privileges on Windows.
	_ = os.Symlink(deny, filepath.Join(root, "data-link"))

	resp, err := l.List("")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Root != root {
		t.Fatalf("root = %q, want %q", resp.Root, root)
	}
	var got []string
	for _, e := range resp.Entries {
		got = append(got, e.Name)
	}
	want := []string{"beta", ".hidden", "Alpha.txt", "zeta.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	if !resp.Entries[0].Dir {
		t.Fatalf("first entry should be the directory: %+v", resp.Entries[0])
	}
}

func TestLocalUploadDownloadRoundTrip(t *testing.T) {
	l, root, _ := newTestLocal(t)
	ctx := context.Background()
	content := []byte("hello ezterm\nsecond line\n")

	if _, err := l.Upload(ctx, "sub/file.txt", bytes.NewReader(content), true); !errors.Is(err, ErrNotExist) {
		t.Fatalf("upload without parent = %v, want ErrNotExist", err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	entry, err := l.Upload(ctx, "sub/file.txt", bytes.NewReader(content), true)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Size != int64(len(content)) || entry.Name != "file.txt" || entry.Dir {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if runtime.GOOS != "windows" && entry.Mode != "0644" {
		t.Fatalf("new file mode = %q, want 0644", entry.Mode)
	}

	body, meta, err := l.OpenRead("sub/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("downloaded %q, want %q", got, content)
	}
	if meta.Size != int64(len(content)) {
		t.Fatalf("meta size = %d", meta.Size)
	}

	// No temporary file may survive a successful upload.
	leftovers, err := filepath.Glob(filepath.Join(root, "sub", ".ezterm-upload-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

func TestLocalUploadOverwritePreservesMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go maps Windows modes to 0666/0444; permission preservation is POSIX-only")
	}
	l, root, _ := newTestLocal(t)
	ctx := context.Background()
	target := filepath.Join(root, "script.sh")
	writeFile(t, target, "old")
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := l.Upload(ctx, "script.sh", strings.NewReader("new"), false); !errors.Is(err, ErrExists) {
		t.Fatalf("overwrite=false error = %v, want ErrExists", err)
	}
	entry, err := l.Upload(ctx, "script.sh", strings.NewReader("new"), true)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Mode != "0755" {
		t.Fatalf("mode after overwrite = %q, want 0755", entry.Mode)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("content = %q, want new", got)
	}
}

func TestLocalUploadFailures(t *testing.T) {
	l, root, _ := newTestLocal(t)
	ctx := context.Background()
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Upload(ctx, "dir", strings.NewReader("x"), true); !errors.Is(err, ErrIsDir) {
		t.Fatalf("upload onto directory = %v, want ErrIsDir", err)
	}
	if _, err := l.Upload(ctx, "missing/file.txt", strings.NewReader("x"), true); !errors.Is(err, ErrNotExist) {
		t.Fatalf("upload into missing dir = %v, want ErrNotExist", err)
	}
	failing := &failingReader{data: []byte("partial"), err: errors.New("boom")}
	if _, err := l.Upload(ctx, "partial.txt", failing, true); err == nil {
		t.Fatal("expected upload error")
	}
	if _, err := os.Stat(filepath.Join(root, "partial.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial target should not exist, stat err = %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(root, ".ezterm-upload-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.Upload(cancelled, "cancelled.txt", strings.NewReader("x"), true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled upload = %v, want context.Canceled", err)
	}
}

func TestLocalMkdirAndRemove(t *testing.T) {
	l, root, _ := newTestLocal(t)
	entry, err := l.Mkdir("newdir")
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Dir || entry.Name != "newdir" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if _, err := l.Mkdir("newdir"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate mkdir = %v, want ErrExists", err)
	}
	if _, err := l.Mkdir("nope/child"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("mkdir with missing parent = %v, want ErrNotExist", err)
	}

	writeFile(t, filepath.Join(root, "newdir", "keep.txt"), "x")
	if err := l.Remove("newdir"); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("remove non-empty dir = %v, want ErrNotEmpty", err)
	}
	if err := l.Remove("newdir/keep.txt"); err != nil {
		t.Fatal(err)
	}
	if err := l.Remove("newdir"); err != nil {
		t.Fatal(err)
	}
	if err := l.Remove("newdir"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("remove missing = %v, want ErrNotExist", err)
	}
	if err := l.Remove(""); !errors.Is(err, ErrRootDelete) {
		t.Fatalf("remove root = %v, want ErrRootDelete", err)
	}
	if err := l.Remove("/"); !errors.Is(err, ErrRootDelete) {
		t.Fatalf("remove filesystem root = %v, want ErrRootDelete", err)
	}
}

func TestLocalDenyRoot(t *testing.T) {
	l, root, deny := newTestLocal(t)
	writeFile(t, filepath.Join(deny, "secret.txt"), "top-secret")

	if _, err := l.List(deny); !errors.Is(err, ErrDenied) {
		t.Fatalf("list deny root = %v, want ErrDenied", err)
	}
	if _, _, err := l.OpenRead(filepath.Join(deny, "secret.txt")); !errors.Is(err, ErrDenied) {
		t.Fatalf("read deny file = %v, want ErrDenied", err)
	}
	if _, err := l.Upload(context.Background(), filepath.Join(deny, "x.txt"), strings.NewReader("x"), true); !errors.Is(err, ErrDenied) {
		t.Fatalf("write deny file = %v, want ErrDenied", err)
	}
	if err := l.Remove(filepath.Join(deny, "secret.txt")); !errors.Is(err, ErrDenied) {
		t.Fatalf("remove deny file = %v, want ErrDenied", err)
	}

	// A symlink that escapes into the protected directory must be rejected too.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(deny, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	if _, err := l.List("escape"); !errors.Is(err, ErrDenied) {
		t.Fatalf("list via escape symlink = %v, want ErrDenied", err)
	}
	if _, _, err := l.OpenRead("escape/secret.txt"); !errors.Is(err, ErrDenied) {
		t.Fatalf("read via escape symlink = %v, want ErrDenied", err)
	}
}

func TestLocalSymlinkFollowing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need developer mode or elevation on Windows")
	}
	l, root, _ := newTestLocal(t)
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(target, "inside.txt"), "inside")
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	resp, err := l.List("")
	if err != nil {
		t.Fatal(err)
	}
	var found, linkIsDir, linkIsSymlink bool
	for _, e := range resp.Entries {
		if e.Name == "link" {
			found, linkIsDir, linkIsSymlink = true, e.Dir, e.Symlink
		}
	}
	if !found || !linkIsSymlink || !linkIsDir {
		t.Fatalf("symlink entry should report symlink=true dir=true, found=%v dir=%v symlink=%v", found, linkIsDir, linkIsSymlink)
	}

	if _, err := l.List("link"); err != nil {
		t.Fatalf("list through symlinked dir: %v", err)
	}
	body, meta, err := l.OpenRead("link/inside.txt")
	if err != nil {
		t.Fatalf("read through symlinked dir: %v", err)
	}
	got, _ := io.ReadAll(body)
	body.Close()
	if string(got) != "inside" || !meta.Symlink {
		t.Fatalf("read = %q symlink=%v", got, meta.Symlink)
	}

	if err := l.Remove("link"); err != nil {
		t.Fatalf("remove symlink: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "link")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("link should be gone, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "inside.txt")); err != nil {
		t.Fatalf("removing a link must not touch its target: %v", err)
	}

	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "gone"), dangling); err != nil {
		t.Fatal(err)
	}
	if _, err := l.List("dangling"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("list dangling link = %v, want ErrNotExist", err)
	}
	if err := l.Remove("dangling"); err != nil {
		t.Fatalf("remove dangling link: %v", err)
	}
}

func TestLocalSpecialFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("FIFOs are Unix-only")
	}
	l, root, _ := newTestLocal(t)
	fifo := filepath.Join(root, "pipe")
	if err := mkfifo(fifo); err != nil {
		t.Skipf("cannot create fifo: %v", err)
	}
	if _, _, err := l.OpenRead("pipe"); !errors.Is(err, ErrSpecial) {
		t.Fatalf("download fifo = %v, want ErrSpecial", err)
	}
	if _, err := l.Upload(context.Background(), "pipe", strings.NewReader("x"), true); !errors.Is(err, ErrSpecial) {
		t.Fatalf("upload onto fifo = %v, want ErrSpecial", err)
	}
	if err := l.Remove("pipe"); err != nil {
		t.Fatalf("remove fifo: %v", err)
	}
}

func TestLocalConcurrentUploads(t *testing.T) {
	l, root, _ := newTestLocal(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for _, name := range []string{"a.bin", "b.bin"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			if _, err := l.Upload(ctx, name, bytes.NewReader([]byte(name)), true); err != nil {
				t.Errorf("upload %s: %v", name, err)
			}
		}(name)
	}
	wg.Wait()
	for _, name := range []string{"a.bin", "b.bin"} {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != name {
			t.Fatalf("%s = %q", name, got)
		}
	}
}

// failingReader yields some bytes and then fails, exercising the upload
// rollback path.
type failingReader struct {
	data []byte
	err  error
	read bool
}

func (f *failingReader) Read(p []byte) (int, error) {
	if !f.read {
		f.read = true
		n := copy(p, f.data)
		return n, nil
	}
	return 0, f.err
}

// writeFile writes a small test file.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
