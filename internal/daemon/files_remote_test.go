package daemon

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"ezterm/internal/api"
	"ezterm/internal/config"
	"ezterm/internal/configstore"
	"ezterm/internal/message"
	"ezterm/internal/session"
	"ezterm/internal/storage"
)

// TestFilesAgainstRemoteServer exercises the file endpoints against a real SSH
// host. It is skipped unless EZTERM_TEST_SSH_CONFIG names an existing config
// that stores its password (or uses a key). The test never prints credentials.
func TestFilesAgainstRemoteServer(t *testing.T) {
	name := strings.TrimSpace(os.Getenv("EZTERM_TEST_SSH_CONFIG"))
	if name == "" {
		t.Skip("set EZTERM_TEST_SSH_CONFIG to an SSH config name to run the remote file test")
	}

	realDataDir := strings.TrimSpace(os.Getenv("EZTERM_TEST_DATA_DIR"))
	if realDataDir == "" {
		var err error
		realDataDir, err = config.DefaultDataDir()
		if err != nil {
			t.Fatal(err)
		}
	}
	profile, err := configstore.NewStore(realDataDir).GetSSH(name)
	if err != nil {
		t.Fatalf("load SSH config %q: %v", name, err)
	}

	// Copy the profile into a throwaway data directory so this test never
	// rewrites the running daemon's sessions or transcripts.
	dataDir := t.TempDir()
	store := storage.New(dataDir)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	cfgStore := configstore.NewStore(dataDir)
	if err := cfgStore.SaveSSH(name, profile); err != nil {
		t.Fatalf("copy SSH config %q: %v", name, err)
	}
	msgMgr := message.NewManager(store)
	mgr := session.NewManager(store, msgMgr, cfgStore)
	server := httptest.NewServer(NewHandler(mgr, cfgStore))
	t.Cleanup(server.Close)
	client := server.Client()

	s, err := mgr.Create(session.Config{
		SSHConfig: name, Mode: api.ModePTY, Rows: 24, Cols: 80, WebEnabled: false,
	})
	if err != nil {
		t.Fatalf("create session with config %q: %v", name, err)
	}
	t.Cleanup(func() { _ = mgr.Terminate(s.ID, time.Second, true) })
	if got := s.Info().Status; got == api.StatusAwaitingPassword {
		t.Fatalf("config %q has no stored password; use a config that stores one or uses a key", name)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && s.Info().Status != api.StatusRunning {
		time.Sleep(20 * time.Millisecond)
	}
	if got := s.Info().Status; got != api.StatusRunning {
		t.Fatalf("session status = %s, want running", got)
	}

	base := filesURL(server, s.ID, "")

	// Resolve the remote home through the API.
	resp, raw := doFiles(t, client, http.MethodGet, base, nil, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list home: status=%d body=%q", resp.StatusCode, raw)
	}
	home := decodeFileList(t, raw).Root
	if home == "" {
		t.Fatal("empty remote root")
	}

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	dir := path.Join(home, ".ezterm-files-test-"+hex.EncodeToString(suffix))
	mkdirBody, _ := json.Marshal(api.MkdirRequest{Path: dir})
	resp, raw = doFiles(t, client, http.MethodPost, base, bytes.NewReader(mkdirBody), "application/json", nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("mkdir: status=%d body=%q", resp.StatusCode, raw)
	}
	t.Cleanup(func() {
		// Best effort cleanup; failures are reported by the main assertions.
		_ = deleteRemoteFile(client, base, path.Join(dir, "text.txt"))
		_ = deleteRemoteFile(client, base, path.Join(dir, "random.bin"))
		_ = deleteRemoteFile(client, base, path.Join(dir, "text.txt.tmp"))
		_ = deleteRemoteFile(client, base, dir)
	})

	// Text upload, listing, download.
	textTarget := path.Join(dir, "text.txt")
	resp, raw = doFiles(t, client, http.MethodPost, withPath(base+"/content", textTarget), strings.NewReader("hello from the integration test"), "application/octet-stream", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload text: status=%d body=%q", resp.StatusCode, raw)
	}
	resp, raw = doFiles(t, client, http.MethodGet, withPath(base+"/content", textTarget), nil, "", nil)
	if resp.StatusCode != http.StatusOK || string(raw) != "hello from the integration test" {
		t.Fatalf("download text: status=%d body=%q", resp.StatusCode, raw)
	}

	// Binary payload larger than one SFTP packet.
	payload := make([]byte, 1<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	binTarget := path.Join(dir, "random.bin")
	resp, raw = doFiles(t, client, http.MethodPost, withPath(base+"/content", binTarget), bytes.NewReader(payload), "application/octet-stream", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload binary: status=%d body=%q", resp.StatusCode, raw)
	}
	resp, raw = doFiles(t, client, http.MethodGet, withPath(base+"/content", binTarget), nil, "", nil)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(raw, payload) {
		t.Fatalf("download binary: status=%d bytes=%d", resp.StatusCode, len(raw))
	}

	// Listing sees both files, and overwrite=false refuses an existing one.
	resp, raw = doFiles(t, client, http.MethodGet, withPath(base, dir), nil, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list temp dir: status=%d body=%q", resp.StatusCode, raw)
	}
	if got := len(decodeFileList(t, raw).Entries); got != 2 {
		t.Fatalf("temp dir entries = %d, want 2", got)
	}
	resp, raw = doFiles(t, client, http.MethodPost, withPath(base+"/content", textTarget)+"&overwrite=false", strings.NewReader("x"), "application/octet-stream", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("overwrite=false: status=%d body=%q", resp.StatusCode, raw)
	}

	// Delete the files, then the directory.
	for _, target := range []string{textTarget, binTarget} {
		resp, raw = doFiles(t, client, http.MethodDelete, withPath(base, target), nil, "", nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("delete %s: status=%d body=%q", path.Base(target), resp.StatusCode, raw)
		}
	}
	resp, raw = doFiles(t, client, http.MethodDelete, withPath(base, dir), nil, "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete temp dir: status=%d body=%q", resp.StatusCode, raw)
	}
	t.Logf("remote file transfer round trip passed (config %q, 1 MiB payload)", name)
}

// deleteRemoteFile removes a remote path, ignoring failures during cleanup.
func deleteRemoteFile(client *http.Client, base, target string) error {
	req, err := http.NewRequest(http.MethodDelete, withPath(base, target), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}
