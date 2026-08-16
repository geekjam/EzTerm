package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ezterm/internal/api"
	"ezterm/internal/configstore"
	"ezterm/internal/message"
	"ezterm/internal/session"
	"ezterm/internal/storage"
)

// testConfigHandler builds a handler whose config store is also returned, so
// the tests can verify the persisted side of an API write (e.g. a retained
// SSH password that the API never returns).
func testConfigHandler(t *testing.T) (*httptest.Server, *configstore.Store) {
	t.Helper()
	dir := t.TempDir()
	store := storage.New(dir)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	msgMgr := message.NewManager(store)
	cfgStore := configstore.NewStore(dir)
	mgr := session.NewManager(store, msgMgr, cfgStore)
	server := httptest.NewServer(NewHandler(mgr, cfgStore))
	t.Cleanup(server.Close)
	return server, cfgStore
}

func TestConfigCRUDEndpoints(t *testing.T) {
	server, cfgStore := testConfigHandler(t)
	client := server.Client()

	// Empty list.
	var listed struct {
		Configs []api.ConfigSummary `json:"configs"`
	}
	resp := doJSON(t, client, http.MethodGet, server.URL+"/api/configs", nil, &listed)
	if resp.StatusCode != http.StatusOK || len(listed.Configs) != 0 {
		t.Fatalf("empty list status=%d configs=%+v", resp.StatusCode, listed.Configs)
	}

	// Create a local config and read back its detail (which includes args).
	localReq := api.ConfigUpsertRequest{
		Type: "local", Command: "bash", Args: []string{"-l", "--color=auto"}, Mode: "pty",
	}
	var localDetail api.ConfigDetail
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/configs/dev", localReq, &localDetail)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create local status=%d", resp.StatusCode)
	}
	if localDetail.Type != "local" || localDetail.Name != "dev" || len(localDetail.Args) != 2 {
		t.Fatalf("unexpected local detail: %+v", localDetail)
	}
	var gotLocal api.ConfigDetail
	resp = doJSON(t, client, http.MethodGet, server.URL+"/api/configs/dev", nil, &gotLocal)
	if resp.StatusCode != http.StatusOK || gotLocal.Command != "bash" || strings.Join(gotLocal.Args, " ") != "-l --color=auto" {
		t.Fatalf("get local status=%d detail=%+v", resp.StatusCode, gotLocal)
	}

	// Create an SSH config with a password.
	sshReq := api.ConfigUpsertRequest{
		Type: "ssh", Host: "db.example.com", Port: 2222, User: "deploy",
		AuthMethod: "password", Password: "super-secret-value", Shell: "/bin/bash",
	}
	var sshDetail api.ConfigDetail
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/configs/prod", sshReq, &sshDetail)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create ssh status=%d", resp.StatusCode)
	}
	if sshDetail.Host != "db.example.com" || sshDetail.Shell != "/bin/bash" || sshDetail.AuthMethod != "password" {
		t.Fatalf("unexpected ssh detail: %+v", sshDetail)
	}

	// The password must never appear in the GET response body.
	gotResp, err := client.Get(server.URL + "/api/configs/prod")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(gotResp.Body)
	gotResp.Body.Close()
	if gotResp.StatusCode != http.StatusOK {
		t.Fatalf("get ssh status=%d", gotResp.StatusCode)
	}
	if strings.Contains(string(raw), "super-secret-value") {
		t.Fatalf("GET leaked the stored password: %s", raw)
	}
	// The response must not contain any property whose key is "password".
	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("decode get-ssh body: %v", err)
	}
	if _, ok := asMap["password"]; ok {
		t.Fatalf("GET contained a password key: %s", raw)
	}

	// The password is persisted on disk.
	profile, err := cfgStore.GetSSH("prod")
	if err != nil || profile.Password != "super-secret-value" {
		t.Fatalf("stored profile=%+v err=%v", profile, err)
	}

	// Updating without a password preserves the stored secret.
	sshUpdate := sshReq
	sshUpdate.Port = 2223 // change something else to prove the update ran
	sshUpdate.Password = ""
	var _ api.ConfigDetail
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/configs/prod", sshUpdate, &sshDetail)
	if resp.StatusCode != http.StatusOK || sshDetail.Port != 2223 {
		t.Fatalf("update ssh status=%d detail=%+v", resp.StatusCode, sshDetail)
	}
	profile, _ = cfgStore.GetSSH("prod")
	if profile.Port != 2223 || profile.Password != "super-secret-value" {
		t.Fatalf("password not preserved on update: %+v", profile)
	}

	// Cross-type conflict: "dev" already exists as local, so creating ssh dev 409s.
	cross := api.ConfigUpsertRequest{Type: "ssh", Host: "h", User: "u", AuthMethod: "key", KeyPath: "~/.ssh/id_a"}
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/configs/dev", cross, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("cross-type create status=%d, want 409", resp.StatusCode)
	}

	// New SSH config with password auth but no password -> 400.
	noSecret := api.ConfigUpsertRequest{Type: "ssh", Host: "h", User: "u", AuthMethod: "password"}
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/configs/newpw", noSecret, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing password status=%d, want 400", resp.StatusCode)
	}

	// Invalid type -> 400.
	badType := api.ConfigUpsertRequest{Type: "nope", Command: "ls"}
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/configs/bad", badType, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad type status=%d, want 400", resp.StatusCode)
	}

	// Get unknown config -> 404.
	resp = doJSON(t, client, http.MethodGet, server.URL+"/api/configs/nope", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get missing status=%d, want 404", resp.StatusCode)
	}

	// Delete and confirm it is gone.
	resp = doJSON(t, client, http.MethodDelete, server.URL+"/api/configs/dev", nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status=%d, want 204", resp.StatusCode)
	}
	resp = doJSON(t, client, http.MethodGet, server.URL+"/api/configs/dev", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete status=%d, want 404", resp.StatusCode)
	}
	resp = doJSON(t, client, http.MethodDelete, server.URL+"/api/configs/dev", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete missing status=%d, want 404", resp.StatusCode)
	}
}

func TestConfigWebAssets(t *testing.T) {
	server, _ := testConfigHandler(t)
	client := server.Client()

	cases := []struct {
		path        string
		contentType string
		marker      string
	}{
		{path: "/config", contentType: "text/html", marker: "config-form"},
		{path: "/config/app.js", contentType: "text/javascript", marker: "/api/configs/"},
		{path: "/config/style.css", contentType: "text/css", marker: "--background"},
	}
	for _, tc := range cases {
		resp, err := client.Get(server.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if resp.StatusCode != http.StatusOK ||
			!strings.Contains(resp.Header.Get("Content-Type"), tc.contentType) ||
			!strings.Contains(string(data), tc.marker) {
			t.Fatalf("asset %s status=%d type=%q body=%q", tc.path, resp.StatusCode, resp.Header.Get("Content-Type"), data)
		}
	}
}

func TestConfigUpsertJSON(t *testing.T) {
	// Sanity: the upsert request serializes the shell field as "shell".
	var req api.ConfigUpsertRequest
	_ = json.Unmarshal([]byte(`{"type":"ssh","host":"h","user":"u","auth_method":"key","key_path":"~/.ssh/id_a","shell":"/bin/bash"}`), &req)
	if req.Shell != "/bin/bash" {
		t.Fatalf("shell not decoded: %+v", req)
	}
}
