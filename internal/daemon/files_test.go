package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"ezterm/internal/api"
	"ezterm/internal/configstore"
	"ezterm/internal/message"
	"ezterm/internal/session"
	"ezterm/internal/sshconfig"
	"ezterm/internal/storage"
)

// fileTestHandler builds a daemon over a caller-provided data directory so a
// test can assert that protected files stay private.
func fileTestHandler(t *testing.T, dataDir string) (*httptest.Server, *session.Manager) {
	t.Helper()
	store := storage.New(dataDir)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	msgMgr := message.NewManager(store)
	cfgStore := configstore.NewStore(dataDir)
	mgr := session.NewManager(store, msgMgr, cfgStore)
	server := httptest.NewServer(NewHandler(mgr, cfgStore))
	t.Cleanup(server.Close)
	return server, mgr
}

// newLocalFilesSession starts a long-lived local pipe session whose transfer
// root is root (the manager also adds the daemon data directory to the deny
// list).
func newLocalFilesSession(t *testing.T, mgr *session.Manager, root string) *session.Session {
	t.Helper()
	s, err := mgr.Create(session.Config{
		Command:  os.Args[0],
		Args:     daemonHelperArgs("interactive"),
		Mode:     api.ModePipe,
		Name:     "local-files",
		FileRoot: root,
	})
	if err != nil {
		t.Fatalf("create local session: %v", err)
	}
	waitForStatus(t, s, api.StatusRunning)
	t.Cleanup(func() { _ = mgr.Terminate(s.ID, time.Second, true) })
	return s
}

// waitForStatus polls a session until it reaches want or the test times out.
func waitForStatus(t *testing.T, s *session.Session, want api.Status) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s.Info().Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %s status = %s, want %s", s.ID, s.Info().Status, want)
}

// filesURL builds a session files URL, with an optional query suffix.
func filesURL(server *httptest.Server, id, suffix string) string {
	return server.URL + "/api/sessions/" + url.PathEscape(id) + "/files" + suffix
}

// withPath appends an encoded path query to a files URL.
func withPath(base, path string) string {
	return base + "?path=" + url.QueryEscape(path)
}

// doFiles performs a raw request and returns the response and body.
func doFiles(t *testing.T, client *http.Client, method, target string, body io.Reader, contentType string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, raw
}

// decodeFileResponse decodes the {"file": {...}} envelope.
func decodeFileResponse(t *testing.T, raw []byte) api.FileResponse {
	t.Helper()
	var out api.FileResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode file response: %v; body=%q", err, raw)
	}
	return out
}

// decodeFileList decodes a listing response.
func decodeFileList(t *testing.T, raw []byte) api.FileListResponse {
	t.Helper()
	var out api.FileListResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode file list: %v; body=%q", err, raw)
	}
	return out
}

func TestFilesLocalSessionCRUD(t *testing.T) {
	dataDir := t.TempDir()
	server, mgr := fileTestHandler(t, dataDir)
	client := server.Client()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := newLocalFilesSession(t, mgr, root)
	base := filesURL(server, s.ID, "")

	// Listing the root: directories sort first and the daemon data directory
	// (outside this root) never appears.
	resp, raw := doFiles(t, client, http.MethodGet, base, nil, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d body=%s", resp.StatusCode, raw)
	}
	listing := decodeFileList(t, raw)
	if listing.Root != root {
		t.Fatalf("root = %q, want %q", listing.Root, root)
	}
	if len(listing.Entries) != 2 || listing.Entries[0].Name != "sub" || !listing.Entries[0].Dir {
		t.Fatalf("unexpected entries: %+v", listing.Entries)
	}

	// Listing a single file returns one entry.
	resp, raw = doFiles(t, client, http.MethodGet, withPath(base, filepath.Join(root, "hello.txt")), nil, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list file status = %d body=%s", resp.StatusCode, raw)
	}
	single := decodeFileList(t, raw)
	if len(single.Entries) != 1 || single.Entries[0].Dir || single.Entries[0].Size != 5 {
		t.Fatalf("unexpected single entry: %+v", single.Entries)
	}

	// Upload a larger payload and verify it lands byte-for-byte.
	payload := bytes.Repeat([]byte("0123456789abcdef"), 64*1024/16)
	target := filepath.Join(root, "sub", "upload.bin")
	resp, raw = doFiles(t, client, http.MethodPost, withPath(base+"/content", target), bytes.NewReader(payload), "application/octet-stream", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d body=%s", resp.StatusCode, raw)
	}
	uploaded := decodeFileResponse(t, raw)
	if uploaded.File.Size != int64(len(payload)) || uploaded.File.Path != target {
		t.Fatalf("unexpected upload response: %+v", uploaded.File)
	}
	onDisk, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, payload) {
		t.Fatal("uploaded bytes differ from the request body")
	}

	// overwrite=false must refuse an existing target.
	resp, raw = doFiles(t, client, http.MethodPost,
		withPath(base+"/content", target)+"&overwrite=false", strings.NewReader("x"), "application/octet-stream", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("overwrite=false status = %d body=%s", resp.StatusCode, raw)
	}

	// Download returns the original bytes and attachment headers.
	resp, raw = doFiles(t, client, http.MethodGet, withPath(base+"/content", target), nil, "", nil)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(raw, payload) {
		t.Fatalf("download status=%d len=%d body=%q", resp.StatusCode, len(raw), raw[:minInt(len(raw), 120)])
	}
	if got := resp.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff = %q", got)
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Fatalf("Content-Disposition = %q", got)
	}

	// HEAD returns headers without a body.
	resp, raw = doFiles(t, client, http.MethodHead, withPath(base+"/content", target), nil, "", nil)
	if resp.StatusCode != http.StatusOK || len(raw) != 0 {
		t.Fatalf("HEAD status=%d body=%q", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Content-Length"); got != "65536" {
		t.Fatalf("HEAD Content-Length = %q", got)
	}

	// A directory cannot be downloaded.
	resp, raw = doFiles(t, client, http.MethodGet, withPath(base+"/content", filepath.Join(root, "sub")), nil, "", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("download directory status = %d body=%s", resp.StatusCode, raw)
	}

	// Missing path is 404.
	resp, raw = doFiles(t, client, http.MethodGet, withPath(base, filepath.Join(root, "missing")), nil, "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing path status = %d body=%s", resp.StatusCode, raw)
	}

	// mkdir creates a directory, a duplicate is a conflict.
	mkdirBody, _ := json.Marshal(api.MkdirRequest{Path: filepath.Join(root, "newdir")})
	resp, raw = doFiles(t, client, http.MethodPost, base, bytes.NewReader(mkdirBody), "application/json", nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("mkdir status = %d body=%s", resp.StatusCode, raw)
	}
	created := decodeFileResponse(t, raw)
	if !created.File.Dir || created.File.Name != "newdir" {
		t.Fatalf("unexpected mkdir response: %+v", created.File)
	}
	resp, raw = doFiles(t, client, http.MethodPost, base, bytes.NewReader(mkdirBody), "application/json", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate mkdir status = %d body=%s", resp.StatusCode, raw)
	}

	// Deleting a non-empty directory is a conflict; an empty one succeeds.
	resp, raw = doFiles(t, client, http.MethodDelete, withPath(base, filepath.Join(root, "sub")), nil, "", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete non-empty status = %d body=%s", resp.StatusCode, raw)
	}
	resp, raw = doFiles(t, client, http.MethodDelete, withPath(base, filepath.Join(root, "newdir")), nil, "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete empty dir status = %d body=%s", resp.StatusCode, raw)
	}

	// The session root and filesystem root are protected.
	for _, protected := range []string{root, "/"} {
		resp, raw = doFiles(t, client, http.MethodDelete, withPath(base, protected), nil, "", nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("delete %q status = %d body=%s", protected, resp.StatusCode, raw)
		}
	}

	// Uploading onto a directory is a conflict.
	resp, raw = doFiles(t, client, http.MethodPost, withPath(base+"/content", filepath.Join(root, "sub")), strings.NewReader("x"), "application/octet-stream", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("upload onto dir status = %d body=%s", resp.StatusCode, raw)
	}

	// The upload endpoint only accepts raw octet-stream bodies.
	resp, raw = doFiles(t, client, http.MethodPost, withPath(base+"/content", filepath.Join(root, "text.txt")), strings.NewReader("x"), "text/plain", nil)
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain status = %d body=%s", resp.StatusCode, raw)
	}
	resp, raw = doFiles(t, client, http.MethodPost, withPath(base+"/content", filepath.Join(root, "text.txt")), strings.NewReader("x"), "", nil)
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("missing content type status = %d body=%s", resp.StatusCode, raw)
	}

	// File contents never enter the message log.
	assertNoMessageContent(t, dataDir, "0123456789abcdef")

	// Once the session is no longer running, transfer is unavailable.
	if err := mgr.Terminate(s.ID, time.Second, true); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, s, api.StatusTerminated)
	resp, raw = doFiles(t, client, http.MethodGet, base, nil, "", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("terminated session status = %d body=%s", resp.StatusCode, raw)
	}
}

// AssertNoMessageContent walks the message store and fails if needle appears.
func assertNoMessageContent(t *testing.T, dataDir, needle string) {
	t.Helper()
	root := filepath.Join(dataDir, "messages")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if bytes.Contains(data, []byte(needle)) {
			t.Fatalf("message file %s contains uploaded content", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk messages: %v", err)
	}
}

func TestFilesLocalSymlinkFollow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need developer mode or elevation on Windows")
	}
	server, mgr := fileTestHandler(t, t.TempDir())
	root := t.TempDir()
	real := filepath.Join(root, "real.txt")
	if err := os.WriteFile(real, []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	s := newLocalFilesSession(t, mgr, root)
	base := filesURL(server, s.ID, "")
	client := server.Client()

	resp, raw := doFiles(t, client, http.MethodGet, withPath(base+"/content", link), nil, "", nil)
	if resp.StatusCode != http.StatusOK || string(raw) != "target" {
		t.Fatalf("download through symlink status=%d body=%q", resp.StatusCode, raw)
	}

	// Uploading through the link must replace the target and keep the link.
	resp, raw = doFiles(t, client, http.MethodPost, withPath(base+"/content", link), strings.NewReader("new"), "application/octet-stream", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload through symlink status = %d body=%s", resp.StatusCode, raw)
	}
	if data, err := os.ReadFile(real); err != nil || string(data) != "new" {
		t.Fatalf("target = %q err=%v", data, err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link was replaced: info=%v err=%v", info, err)
	}

	// Deleting the link must not touch its target.
	resp, raw = doFiles(t, client, http.MethodDelete, withPath(base, link), nil, "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete link status = %d body=%s", resp.StatusCode, raw)
	}
	if _, err := os.Stat(real); err != nil {
		t.Fatalf("target should still exist: %v", err)
	}
}

func TestFilesLocalDenyDataDir(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	server, mgr := fileTestHandler(t, dataDir)
	s := newLocalFilesSession(t, mgr, base)
	baseURL := filesURL(server, s.ID, "")
	client := server.Client()

	resp, raw := doFiles(t, client, http.MethodGet, baseURL, nil, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d body=%s", resp.StatusCode, raw)
	}
	listing := decodeFileList(t, raw)
	for _, entry := range listing.Entries {
		if entry.Name == "data" {
			t.Fatalf("daemon data directory leaked into listing: %+v", entry)
		}
	}

	secret := filepath.Join(dataDir, "secret.txt")
	resp, raw = doFiles(t, client, http.MethodGet, withPath(baseURL+"/content", secret), nil, "", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read data dir status = %d body=%s", resp.StatusCode, raw)
	}
	resp, raw = doFiles(t, client, http.MethodPost, withPath(baseURL+"/content", secret), strings.NewReader("x"), "application/octet-stream", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("write data dir status = %d body=%s", resp.StatusCode, raw)
	}
}

func TestFilesSSHSessionCRUD(t *testing.T) {
	remoteRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(remoteRoot, "remote.txt"), []byte("remote"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(remoteRoot, "rdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	port := startSSHServerWithSFTP(t, "secret", remoteRoot)
	server, mgr, cfgStore := promptHandler(t)
	if err := cfgStore.SaveSSH("files", &sshconfig.Profile{
		Host: "127.0.0.1", Port: port, User: "tester",
		AuthMethod: sshconfig.AuthPassword, Password: "secret",
	}); err != nil {
		t.Fatal(err)
	}
	s, err := mgr.Create(session.Config{
		SSHConfig: "files", Mode: api.ModePTY, Rows: 24, Cols: 80, WebEnabled: false,
	})
	if err != nil {
		t.Fatalf("create ssh session: %v", err)
	}
	waitForStatus(t, s, api.StatusRunning)
	t.Cleanup(func() { _ = mgr.Terminate(s.ID, time.Second, true) })
	client := server.Client()
	base := filesURL(server, s.ID, "")

	resp, raw := doFiles(t, client, http.MethodGet, base, nil, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d body=%s", resp.StatusCode, raw)
	}
	listing := decodeFileList(t, raw)
	if len(listing.Entries) < 2 {
		t.Fatalf("unexpected remote listing: %+v", listing.Entries)
	}

	payload := []byte("sftp payload\n")
	resp, raw = doFiles(t, client, http.MethodPost, withPath(base+"/content", "~"), bytes.NewReader(payload), "application/octet-stream", nil)
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusConflict {
		// "~" is a directory; uploads must be refused, not written.
		t.Fatalf("upload onto root status = %d body=%s", resp.StatusCode, raw)
	}

	resp, raw = doFiles(t, client, http.MethodPost, withPath(base+"/content", "upload.txt"), bytes.NewReader(payload), "application/octet-stream", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d body=%s", resp.StatusCode, raw)
	}
	if data, err := os.ReadFile(filepath.Join(remoteRoot, "upload.txt")); err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("remote file = %q err=%v", data, err)
	}

	resp, raw = doFiles(t, client, http.MethodGet, withPath(base+"/content", "upload.txt"), nil, "", nil)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(raw, payload) {
		t.Fatalf("download status=%d body=%q", resp.StatusCode, raw)
	}

	mkdirBody, _ := json.Marshal(api.MkdirRequest{Path: "made"})
	resp, raw = doFiles(t, client, http.MethodPost, base, bytes.NewReader(mkdirBody), "application/json", nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("mkdir status = %d body=%s", resp.StatusCode, raw)
	}
	resp, raw = doFiles(t, client, http.MethodDelete, withPath(base, "made"), nil, "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete dir status = %d body=%s", resp.StatusCode, raw)
	}

	// Symlinks are listed but never followed.
	if runtime.GOOS != "windows" {
		link := filepath.Join(remoteRoot, "remote-link")
		if err := os.Symlink(filepath.Join(remoteRoot, "remote.txt"), link); err == nil {
			resp, raw = doFiles(t, client, http.MethodGet, withPath(base+"/content", "remote-link"), nil, "", nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("download symlink status = %d body=%s", resp.StatusCode, raw)
			}
			resp, raw = doFiles(t, client, http.MethodPost, withPath(base+"/content", "remote-link"), strings.NewReader("x"), "application/octet-stream", nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("upload through symlink status = %d body=%s", resp.StatusCode, raw)
			}
			resp, raw = doFiles(t, client, http.MethodGet, base, nil, "", nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("list after symlink status = %d body=%s", resp.StatusCode, raw)
			}
			var found bool
			for _, entry := range decodeFileList(t, raw).Entries {
				if entry.Name == "remote-link" {
					found = true
					if !entry.Symlink || entry.Dir {
						t.Fatalf("symlink entry = %+v", entry)
					}
				}
			}
			if !found {
				t.Fatal("symlink missing from listing")
			}
			resp, raw = doFiles(t, client, http.MethodDelete, withPath(base, "remote-link"), nil, "", nil)
			if resp.StatusCode != http.StatusNoContent {
				t.Fatalf("delete symlink status = %d body=%s", resp.StatusCode, raw)
			}
			if _, err := os.Stat(filepath.Join(remoteRoot, "remote.txt")); err != nil {
				t.Fatalf("deleting a link must not touch its target: %v", err)
			}
		}
	}
}

func TestFilesAwaitingPasswordRejected(t *testing.T) {
	port := startSSHServerWithSFTP(t, "secret", t.TempDir())
	server, mgr, cfgStore := promptHandler(t)
	if err := cfgStore.SaveSSH("await", &sshconfig.Profile{
		Host: "127.0.0.1", Port: port, User: "tester", AuthMethod: sshconfig.AuthPassword,
	}); err != nil {
		t.Fatal(err)
	}
	s, err := mgr.Create(session.Config{SSHConfig: "await", Mode: api.ModePTY, WebEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Terminate(s.ID, time.Second, true) })
	if got := s.Info().Status; got != api.StatusAwaitingPassword {
		t.Fatalf("status = %s, want awaiting_password", got)
	}
	client := server.Client()
	resp, raw := doFiles(t, client, http.MethodGet, filesURL(server, s.ID, ""), nil, "", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("awaiting_password status = %d body=%s", resp.StatusCode, raw)
	}
}

func TestFilesAccessBoundary(t *testing.T) {
	// A non-loopback peer cannot transfer and cannot probe session existence.
	remoteServer, remoteMgr, _ := promptHandlerRemoteAddr(t, "203.0.113.9:5555")
	remoteSession := newLocalFilesSession(t, remoteMgr, t.TempDir())
	if resp, raw := doFiles(t, remoteServer.Client(), http.MethodGet, filesURL(remoteServer, remoteSession.ID, ""), nil, "", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-loopback status = %d body=%s", resp.StatusCode, raw)
	}
	if resp, raw := doFiles(t, remoteServer.Client(), http.MethodGet, filesURL(remoteServer, "doesnotexist", ""), nil, "", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-loopback unknown session status = %d body=%s", resp.StatusCode, raw)
	}

	// A cross-site browser Origin is rejected; same-origin and no Origin pass.
	server, mgr := fileTestHandler(t, t.TempDir())
	s := newLocalFilesSession(t, mgr, t.TempDir())
	client := server.Client()
	target := filesURL(server, s.ID, "")
	resp, raw := doFiles(t, client, http.MethodGet, target, nil, "", map[string]string{"Origin": "http://evil.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d body=%s", resp.StatusCode, raw)
	}
	resp, raw = doFiles(t, client, http.MethodGet, target, nil, "", map[string]string{"Origin": server.URL})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("same-origin status = %d body=%s", resp.StatusCode, raw)
	}
	resp, raw = doFiles(t, client, http.MethodGet, target, nil, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("no-origin status = %d body=%s", resp.StatusCode, raw)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
