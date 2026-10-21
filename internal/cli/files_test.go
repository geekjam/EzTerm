package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ezterm/internal/api"
)

// filesStub serves the file endpoints and records the requests it receives.
type filesStub struct {
	server      *httptest.Server
	lastPath    string
	lastQuery   url.Values
	lastMethod  string
	lastBody    []byte
	lastContent string
	status      int
	body        string
	listing     api.FileListResponse
}

func newFilesStub(t *testing.T) *filesStub {
	t.Helper()
	stub := &filesStub{status: http.StatusOK}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.lastMethod = r.Method
		stub.lastQuery = r.URL.Query()
		stub.lastPath = r.URL.Path
		stub.lastContent = r.Header.Get("Content-Type")
		data, _ := io.ReadAll(r.Body)
		stub.lastBody = data
		w.Header().Set("Content-Type", "application/json")
		if stub.status != http.StatusOK && stub.status != http.StatusNoContent {
			w.WriteHeader(stub.status)
			_ = json.NewEncoder(w).Encode(api.ErrorResponse{Error: stub.body})
			return
		}
		if stub.status == http.StatusNoContent {
			w.WriteHeader(stub.status)
			return
		}
		if stub.body != "" {
			_, _ = w.Write([]byte(stub.body))
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/files/content") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte("downloaded-bytes"))
		case strings.HasSuffix(r.URL.Path, "/files") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(stub.listing)
		case strings.HasSuffix(r.URL.Path, "/files") && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(api.FileResponse{File: api.FileEntry{Name: "made", Path: "/home/made", Dir: true}})
		default:
			_ = json.NewEncoder(w).Encode(api.FileResponse{File: api.FileEntry{Name: "upload.txt", Path: "/home/upload.txt", Size: int64(len(data))}})
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *filesStub) client() *client {
	return &client{baseURL: s.server.URL, httpClient: s.server.Client()}
}

func TestFilesListDefaultPath(t *testing.T) {
	stub := newFilesStub(t)
	stub.listing = api.FileListResponse{
		Path: "/home/tester", Root: "/home/tester", Parent: "/home",
		Entries: []api.FileEntry{
			{Name: "dir", Path: "/home/tester/dir", Dir: true, Modified: time.Now().UTC()},
			{Name: "a.txt", Path: "/home/tester/a.txt", Size: 12, Modified: time.Now().UTC()},
		},
	}
	out := captureStdout(t, func() {
		if code := cmdFiles(stub.client(), false, []string{"ls", "abc123abc123"}); code != 0 {
			t.Fatalf("files ls exit = %d", code)
		}
	})
	if stub.lastQuery.Get("path") != "" {
		t.Fatalf("default path query = %q, want empty", stub.lastQuery.Get("path"))
	}
	if !strings.Contains(out, "dir/") || !strings.Contains(out, "a.txt") {
		t.Fatalf("listing output = %q", out)
	}

	jsonOut := captureStdout(t, func() {
		if code := cmdFiles(stub.client(), true, []string{"ls", "abc123abc123", "/home"}); code != 0 {
			t.Fatalf("files ls --json exit = %d", code)
		}
	})
	var decoded api.FileListResponse
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("decode json output %q: %v", jsonOut, err)
	}
	if stub.lastQuery.Get("path") != "/home" || decoded.Root != "/home/tester" {
		t.Fatalf("path=%q root=%q", stub.lastQuery.Get("path"), decoded.Root)
	}
}

func TestFilesUploadDefaults(t *testing.T) {
	stub := newFilesStub(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "local.bin")
	if err := os.WriteFile(source, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := cmdFiles(stub.client(), true, []string{"upload", "abc123abc123", source}); code != 0 {
		t.Fatalf("files upload exit = %d", code)
	}
	if stub.lastQuery.Get("path") != "local.bin" {
		t.Fatalf("default remote path = %q, want local.bin", stub.lastQuery.Get("path"))
	}
	if stub.lastContent != "application/octet-stream" {
		t.Fatalf("content type = %q", stub.lastContent)
	}
	if string(stub.lastBody) != "payload" {
		t.Fatalf("uploaded body = %q", stub.lastBody)
	}
	if stub.lastQuery.Get("overwrite") != "true" {
		t.Fatalf("overwrite = %q, want true", stub.lastQuery.Get("overwrite"))
	}

	if code := cmdFiles(stub.client(), true, []string{"upload", "abc123abc123", source, "target.bin", "--no-overwrite"}); code != 0 {
		t.Fatalf("files upload --no-overwrite exit = %d", code)
	}
	if stub.lastQuery.Get("path") != "target.bin" || stub.lastQuery.Get("overwrite") != "false" {
		t.Fatalf("path=%q overwrite=%q", stub.lastQuery.Get("path"), stub.lastQuery.Get("overwrite"))
	}

	// A trailing separator appends the local file name.
	if code := cmdFiles(stub.client(), true, []string{"upload", "abc123abc123", source, "/remote/dir/"}); code != 0 {
		t.Fatalf("files upload to directory exit = %d", code)
	}
	if got := stub.lastQuery.Get("path"); got != "/remote/dir/local.bin" {
		t.Fatalf("directory upload path = %q, want /remote/dir/local.bin", got)
	}
}

func TestFilesUploadStdinRejectsDirectoryTarget(t *testing.T) {
	stub := newFilesStub(t)
	if code := cmdFiles(stub.client(), true, []string{"upload", "abc123abc123", "-", "/remote/dir/"}); code != 2 {
		t.Fatalf("stdin upload to directory exit = %d, want 2", code)
	}
}

func TestFilesUploadStdinRequiresTarget(t *testing.T) {
	stub := newFilesStub(t)
	if code := cmdFiles(stub.client(), true, []string{"upload", "abc123abc123", "-"}); code != 2 {
		t.Fatalf("upload from stdin without target exit = %d, want 2", code)
	}
}

func TestFilesDownloadToStdout(t *testing.T) {
	stub := newFilesStub(t)
	out := captureStdout(t, func() {
		if code := cmdFiles(stub.client(), false, []string{"download", "abc123abc123", "/home/a.txt", "-"}); code != 0 {
			t.Fatalf("files download - exit = %d", code)
		}
	})
	if out != "downloaded-bytes" {
		t.Fatalf("stdout = %q", out)
	}
	if stub.lastQuery.Get("path") != "/home/a.txt" {
		t.Fatalf("download path = %q", stub.lastQuery.Get("path"))
	}
}

func TestFilesDownloadLocalDefaultsAndForce(t *testing.T) {
	stub := newFilesStub(t)
	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	if code := cmdFiles(stub.client(), true, []string{"download", "abc123abc123", "/remote/path/report.txt"}); code != 0 {
		t.Fatalf("files download exit = %d", code)
	}
	data, err := os.ReadFile(filepath.Join(dir, "report.txt"))
	if err != nil {
		t.Fatalf("expected report.txt: %v", err)
	}
	if string(data) != "downloaded-bytes" {
		t.Fatalf("content = %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, ".ezterm-download-")); err == nil {
		t.Fatal("temporary download file was not renamed")
	}

	// A trailing separator appends the remote file name.
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := cmdFiles(stub.client(), true, []string{"download", "abc123abc123", "/remote/path/other.txt", sub + string(filepath.Separator)}); code != 0 {
		t.Fatalf("download to directory exit = %d", code)
	}
	if data, err := os.ReadFile(filepath.Join(sub, "other.txt")); err != nil || string(data) != "downloaded-bytes" {
		t.Fatalf("directory download = %q err=%v", data, err)
	}

	// A second download without --force must refuse.
	if code := cmdFiles(stub.client(), true, []string{"download", "abc123abc123", "/remote/path/report.txt"}); code != 2 {
		t.Fatalf("existing target exit = %d, want 2", code)
	}
	// With --force it overwrites.
	if code := cmdFiles(stub.client(), true, []string{"download", "abc123abc123", "/remote/path/report.txt", "--force"}); code != 0 {
		t.Fatalf("--force exit = %d", code)
	}
}

func TestFilesMkdirAndRemove(t *testing.T) {
	stub := newFilesStub(t)
	jsonOut := captureStdout(t, func() {
		if code := cmdFiles(stub.client(), true, []string{"mkdir", "abc123abc123", "/home/made"}); code != 0 {
			t.Fatalf("files mkdir exit = %d", code)
		}
	})
	var created api.FileResponse
	if err := json.Unmarshal([]byte(jsonOut), &created); err != nil {
		t.Fatalf("decode mkdir output %q: %v", jsonOut, err)
	}
	if created.File.Name != "made" {
		t.Fatalf("mkdir response = %+v", created.File)
	}

	stub.status = http.StatusNoContent
	out := captureStdout(t, func() {
		if code := cmdFiles(stub.client(), false, []string{"rm", "abc123abc123", "/home/made"}); code != 0 {
			t.Fatalf("files rm exit = %d", code)
		}
	})
	if !strings.Contains(out, "removed /home/made") {
		t.Fatalf("rm output = %q", out)
	}
	if stub.lastQuery.Get("path") != "/home/made" || stub.lastMethod != http.MethodDelete {
		t.Fatalf("rm request = %s path=%q", stub.lastMethod, stub.lastQuery.Get("path"))
	}
}

func TestFilesErrorExitCodes(t *testing.T) {
	stub := newFilesStub(t)
	stub.status = http.StatusNotFound
	stub.body = "session not found"
	if code := cmdFiles(stub.client(), true, []string{"ls", "abc123abc123"}); code != 1 {
		t.Fatalf("missing session exit = %d, want 1", code)
	}
	stub.status = http.StatusNotFound
	stub.body = "files: path does not exist: no such file"
	if code := cmdFiles(stub.client(), true, []string{"ls", "abc123abc123", "/missing"}); code != 2 {
		t.Fatalf("missing path exit = %d, want 2", code)
	}
	stub.status = http.StatusConflict
	stub.body = "file transfer requires a running session"
	if code := cmdFiles(stub.client(), true, []string{"ls", "abc123abc123"}); code != 2 {
		t.Fatalf("conflict exit = %d, want 2", code)
	}
	if code := cmdFiles(stub.client(), true, []string{"bogus"}); code != 2 {
		t.Fatalf("unknown subcommand exit = %d, want 2", code)
	}
	if code := cmdFiles(stub.client(), true, nil); code != 2 {
		t.Fatalf("missing subcommand exit = %d, want 2", code)
	}
}
