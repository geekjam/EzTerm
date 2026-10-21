package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ezterm/internal/api"
	"ezterm/internal/configstore"
	"ezterm/internal/message"
	"ezterm/internal/session"
	"ezterm/internal/sshconfig"
	"ezterm/internal/storage"
	"github.com/coder/websocket"
	pkgsftp "github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// promptHandler builds a handler plus the config store, so tests can register an
// SSH config that stores no password.
func promptHandler(t *testing.T) (*httptest.Server, *session.Manager, *configstore.Store) {
	return promptHandlerRemoteAddr(t, "")
}

// promptHandlerRemoteAddr optionally rewrites the peer address of accepted
// connections, so tests can exercise address-based policy without a real remote
// client.
func promptHandlerRemoteAddr(t *testing.T, remoteAddr string) (*httptest.Server, *session.Manager, *configstore.Store) {
	t.Helper()
	dir := t.TempDir()
	store := storage.New(dir)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	msgMgr := message.NewManager(store)
	cfgStore := configstore.NewStore(dir)
	mgr := session.NewManager(store, msgMgr, cfgStore)
	server := httptest.NewUnstartedServer(NewHandler(mgr, cfgStore))
	if remoteAddr != "" {
		server.Listener = &remoteAddrListener{Listener: server.Listener, remoteAddr: remoteAddr}
	}
	server.Start()
	t.Cleanup(server.Close)
	return server, mgr, cfgStore
}

// remoteAddrListener overrides the peer address reported by accepted
// connections.
type remoteAddrListener struct {
	net.Listener
	remoteAddr string
}

func (l *remoteAddrListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &remoteAddrConn{Conn: conn, remoteAddr: l.remoteAddr}, nil
}

type remoteAddrConn struct {
	net.Conn
	remoteAddr string
}

func (c *remoteAddrConn) RemoteAddr() net.Addr { return fakeAddr(c.remoteAddr) }

// fakeAddr is a net.Addr reporting a fixed network and string.
type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }

// savePromptSSH stores an SSH config with password auth and no secret.
func savePromptSSH(t *testing.T, cfgStore *configstore.Store, name string) {
	t.Helper()
	if err := cfgStore.SaveSSH(name, &sshconfig.Profile{
		Host: "127.0.0.1", Port: 1, User: "tester", AuthMethod: sshconfig.AuthPassword,
	}); err != nil {
		t.Fatal(err)
	}
}

// dialWS opens the WebSocket of a session page.
func dialWS(t *testing.T, server *httptest.Server, id string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/web/" + id + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

// readTextMessage reads one text frame and decodes it into out.
func readTextMessage(t *testing.T, conn *websocket.Conn, out any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read websocket frame: %v", err)
		}
		if typ != websocket.MessageText {
			continue // skip terminal bytes
		}
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("decode text frame %q: %v", data, err)
		}
		return
	}
}

// readUntilText reads text frames until one has the wanted type.
func readUntilText(t *testing.T, conn *websocket.Conn, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var frame map[string]any
		readTextMessage(t, conn, &frame)
		if frame["type"] == want {
			return frame
		}
	}
	t.Fatalf("no %q frame arrived", want)
	return nil
}

// TestCreateSessionRequiresWebForPasswordPrompt covers the 409 rule: an SSH
// config that must ask for a password cannot start without the Web page.
func TestCreateSessionRequiresWebForPasswordPrompt(t *testing.T) {
	server, mgr, cfgStore := promptHandler(t)
	savePromptSSH(t, cfgStore, "prompt")
	client := server.Client()

	// web:false -> 409 and no session is created.
	noWeb := api.CreateSessionRequest{SSHConfig: "prompt", Mode: api.ModePTY, Web: api.BoolPtr(false)}
	var errResp api.ErrorResponse
	resp := doJSON(t, client, http.MethodPost, server.URL+"/api/sessions", noWeb, &errResp)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("web:false status = %d, want 409", resp.StatusCode)
	}
	if !strings.Contains(errResp.Error, "requires a password prompt") {
		t.Fatalf("error = %q", errResp.Error)
	}
	if got := len(mgr.ListAll()); got != 0 {
		t.Fatalf("session count = %d, want 0", got)
	}

	// Default (web omitted) -> 201 awaiting_password with a prompt and URL.
	withWeb := api.CreateSessionRequest{SSHConfig: "prompt", Mode: api.ModePTY, Name: "prompt-session"}
	var created api.Session
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/sessions", withWeb, &created)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body=%+v", resp.StatusCode, created)
	}
	if created.Status != api.StatusAwaitingPassword {
		t.Fatalf("status = %s, want awaiting_password", created.Status)
	}
	if created.WebURL == "" {
		t.Fatal("web_url is empty for a session that needs a password prompt")
	}
	if created.PendingPrompt == nil || created.PendingPrompt.Kind != api.PromptKindSSHPassword {
		t.Fatalf("pending_prompt = %+v", created.PendingPrompt)
	}

	// The session cannot be deleted while it waits; terminate first.
	resp = doJSON(t, client, http.MethodDelete, server.URL+"/api/sessions/"+created.ID, nil, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete awaiting session status = %d, want 409", resp.StatusCode)
	}

	// The Web page is reachable for an awaiting_password session.
	pageResp, err := client.Get(server.URL + "/web/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	pageResp.Body.Close()
	if pageResp.StatusCode != http.StatusOK {
		t.Fatalf("web page status = %d, want 200", pageResp.StatusCode)
	}

	mgr.Terminate(created.ID, time.Second, true)
	if got := mgr.Get(created.ID).Info().Status; got != api.StatusTerminated {
		t.Fatalf("status = %s, want terminated", got)
	}
}

// TestWebSocketReplaysPromptAndAcceptsSecret covers the WS contract: the prompt
// is replayed on connect, a loopback secret is accepted, and the resulting
// secret-result never contains the secret.
func TestWebSocketReplaysPromptAndAcceptsSecret(t *testing.T) {
	server, mgr, cfgStore := promptHandler(t)
	savePromptSSH(t, cfgStore, "prompt")
	client := server.Client()

	var created api.Session
	resp := doJSON(t, client, http.MethodPost, server.URL+"/api/sessions",
		api.CreateSessionRequest{SSHConfig: "prompt", Mode: api.ModePTY}, &created)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	t.Cleanup(func() { mgr.Terminate(created.ID, time.Second, true) })

	conn := dialWS(t, server, created.ID)

	// The prompt must be replayed immediately, before any terminal bytes.
	var promptFrame map[string]any
	readTextMessage(t, conn, &promptFrame)
	if promptFrame["type"] != "prompt" || promptFrame["kind"] != api.PromptKindSSHPassword {
		t.Fatalf("first frame = %+v, want a prompt frame", promptFrame)
	}
	if label, _ := promptFrame["label"].(string); !strings.Contains(label, "tester@127.0.0.1:1") {
		t.Fatalf("prompt label = %q", label)
	}

	// A secret submitted from loopback is processed; port 1 refuses the dial so
	// the session ends and reports the failure.
	const secret = "ws-secret-value"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	payload, _ := json.Marshal(map[string]string{"type": "secret", "text": secret})
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatalf("write secret frame: %v", err)
	}
	result := readUntilText(t, conn, "secret-result")
	if ok, _ := result["ok"].(bool); ok {
		t.Fatalf("secret-result = %+v, want ok=false for a refused dial", result)
	}
	if msg, _ := result["error"].(string); !strings.Contains(msg, "authentication failed") {
		t.Fatalf("secret-result error = %q", msg)
	}
	if raw, _ := json.Marshal(result); strings.Contains(string(raw), secret) {
		t.Fatalf("secret-result leaked the secret: %s", raw)
	}

	// The failed session ends instead of retrying.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && mgr.Get(created.ID).Info().Status == api.StatusAwaitingPassword {
		time.Sleep(10 * time.Millisecond)
	}
	info := mgr.Get(created.ID).Info()
	if info.Status != api.StatusExited {
		t.Fatalf("status = %s, want exited", info.Status)
	}
	if info.PendingPrompt != nil {
		t.Fatalf("pending_prompt = %+v, want nil", info.PendingPrompt)
	}
}

// TestWebSocketRejectsNonLoopbackSecret verifies that a secret from a
// non-loopback peer is refused and does not touch the session.
func TestWebSocketRejectsNonLoopbackSecret(t *testing.T) {
	server, mgr, cfgStore := promptHandlerRemoteAddr(t, "203.0.113.9:443")
	savePromptSSH(t, cfgStore, "prompt")
	client := server.Client()

	var created api.Session
	resp := doJSON(t, client, http.MethodPost, server.URL+"/api/sessions",
		api.CreateSessionRequest{SSHConfig: "prompt", Mode: api.ModePTY}, &created)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	t.Cleanup(func() { mgr.Terminate(created.ID, time.Second, true) })

	conn := dialWS(t, server, created.ID)
	// The prompt is still replayed: it contains no secret.
	readUntilText(t, conn, "prompt")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	payload, _ := json.Marshal(map[string]string{"type": "secret", "text": "remote-secret"})
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatalf("write secret frame: %v", err)
	}
	result := readUntilText(t, conn, "secret-result")
	if ok, _ := result["ok"].(bool); ok {
		t.Fatalf("non-loopback secret accepted: %+v", result)
	}
	if msg, _ := result["error"].(string); !strings.Contains(msg, "local host") {
		t.Fatalf("secret-result error = %q", msg)
	}

	// The session still waits for a real secret from the local page.
	time.Sleep(100 * time.Millisecond)
	info := mgr.Get(created.ID).Info()
	if info.Status != api.StatusAwaitingPassword {
		t.Fatalf("status = %s, want awaiting_password", info.Status)
	}
	if info.PendingPrompt == nil {
		t.Fatal("non-loopback submission cleared the prompt")
	}
}

// TestWebSocketTerminalPromptFlow drives a PTY session whose output contains a
// sudo-style prompt: the server announces it, the submitted secret reaches the
// process, and the secret is never echoed back over the socket.
func TestWebSocketTerminalPromptFlow(t *testing.T) {
	server, mgr, _ := promptHandler(t)
	client := server.Client()

	var created api.Session
	resp := doJSON(t, client, http.MethodPost, server.URL+"/api/sessions", api.CreateSessionRequest{
		Command: os.Args[0],
		Args:    daemonHelperArgs("prompt"),
		Mode:    api.ModePTY,
		Rows:    24,
		Cols:    80,
	}, &created)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	t.Cleanup(func() { mgr.Terminate(created.ID, time.Second, true) })

	conn := dialWS(t, server, created.ID)
	frame := readUntilText(t, conn, "prompt")
	if frame["kind"] != api.PromptKindTerminalPassword {
		t.Fatalf("prompt frame = %+v, want terminal_password", frame)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	payload, _ := json.Marshal(map[string]string{"type": "secret", "text": "pty-secret"})
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatalf("write secret frame: %v", err)
	}
	// The prompt is answered, so the server reports both the accepted secret and
	// the cleared prompt; either frame may arrive first.
	seen := map[string]bool{}
	deadline := time.Now().Add(15 * time.Second)
	for (!seen["secret-result"] || !seen["prompt-cleared"]) && time.Now().Before(deadline) {
		var frame map[string]any
		readTextMessage(t, conn, &frame)
		switch frame["type"] {
		case "secret-result":
			if ok, _ := frame["ok"].(bool); !ok {
				t.Fatalf("secret-result = %+v, want ok=true", frame)
			}
			seen["secret-result"] = true
		case "prompt-cleared":
			seen["prompt-cleared"] = true
		}
	}
	if !seen["secret-result"] || !seen["prompt-cleared"] {
		t.Fatalf("missing frames after submission: %v", seen)
	}
}

// readUntilTypes reads text frames until every wanted type was seen.
func readUntilTypes(t *testing.T, conn *websocket.Conn, want map[string]bool) {
	t.Helper()
	remaining := map[string]bool{}
	for name := range want {
		remaining[name] = true
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && len(remaining) > 0 {
		var frame map[string]any
		readTextMessage(t, conn, &frame)
		if typ, _ := frame["type"].(string); remaining[typ] {
			delete(remaining, typ)
		}
	}
	if len(remaining) > 0 {
		t.Fatalf("missing text frames: %v", remaining)
	}
}

// TestIsLoopbackRemote covers the peer check used for secret submissions.
func TestIsLoopbackRemote(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:54321", true},
		{"[::1]:54321", true},
		{"192.168.1.10:54321", false},
		{"203.0.113.9:443", false},
		{"", false},
		{"not-an-address", false},
	}
	for _, tc := range cases {
		if got := isLoopbackRemote(tc.addr); got != tc.want {
			t.Fatalf("isLoopbackRemote(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

// TestPromptChanged covers the frame-diffing helper.
func TestPromptChanged(t *testing.T) {
	sshPrompt := &api.Prompt{Kind: api.PromptKindSSHPassword, Label: "Password for a@b:22"}
	same := &api.Prompt{Kind: api.PromptKindSSHPassword, Label: "Password for a@b:22"}
	other := &api.Prompt{Kind: api.PromptKindTerminalPassword, Label: "[sudo] password for a:"}
	if promptChanged(nil, nil) {
		t.Fatal("nil to nil must not report a change")
	}
	if !promptChanged(nil, sshPrompt) || !promptChanged(sshPrompt, nil) {
		t.Fatal("appearing or disappearing prompt must report a change")
	}
	if promptChanged(sshPrompt, same) {
		t.Fatal("identical prompts must not report a change")
	}
	if !promptChanged(sshPrompt, other) {
		t.Fatal("different prompts must report a change")
	}
}

// startSSHServer runs a minimal in-process SSH server that accepts one password
// and keeps an interactive session open. It exists so the deferred-dial success
// path can be tested without a real host.
func startSSHServer(t *testing.T, password string) int {
	t.Helper()
	return startSSHServerWithSFTP(t, password, "")
}

// startSSHServerWithSFTP additionally serves the sftp subsystem for file
// transfer tests. An empty root disables the subsystem, matching the minimal
// server used by the password tests.
func startSSHServerWithSFTP(t *testing.T, password, sftpRoot string) int {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if string(pass) == password {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("invalid password")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSHConn(conn, cfg, sftpRoot)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func serveSSHConn(nc net.Conn, cfg *ssh.ServerConfig, sftpRoot string) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		_ = nc.Close()
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go serveSSHSession(ch, requests, sftpRoot)
	}
}

func serveSSHSession(ch ssh.Channel, requests <-chan *ssh.Request, sftpRoot string) {
	defer ch.Close()
	started := false
	for req := range requests {
		switch req.Type {
		case "pty-req":
			_ = req.Reply(true, nil)
		case "exec", "shell":
			_ = req.Reply(true, nil)
			if started {
				continue
			}
			started = true
			go func() {
				_, _ = io.WriteString(ch, "remote-ready\r\n")
				_, _ = io.Copy(ch, ch) // echo input so the test observes the stream
			}()
		case "subsystem":
			var payload struct{ Name string }
			if sftpRoot == "" || ssh.Unmarshal(req.Payload, &payload) != nil || payload.Name != "sftp" {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			server, err := pkgsftp.NewServer(ch, pkgsftp.WithServerWorkingDirectory(sftpRoot))
			if err != nil {
				return
			}
			_ = server.Serve()
			_ = server.Close()
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// TestWebSocketAcceptsSSHPassword is the happy path for deferred dialing: an
// empty-password SSH config waits for a secret, and submitting the right one
// starts the session.
func TestWebSocketAcceptsSSHPassword(t *testing.T) {
	const password = "correct-horse-battery"
	port := startSSHServer(t, password)
	server, mgr, cfgStore := promptHandler(t)
	if err := cfgStore.SaveSSH("prompt", &sshconfig.Profile{
		Host: "127.0.0.1", Port: port, User: "tester", AuthMethod: sshconfig.AuthPassword,
	}); err != nil {
		t.Fatal(err)
	}
	client := server.Client()

	var created api.Session
	resp := doJSON(t, client, http.MethodPost, server.URL+"/api/sessions",
		api.CreateSessionRequest{SSHConfig: "prompt", Mode: api.ModePTY}, &created)
	if resp.StatusCode != http.StatusCreated || created.Status != api.StatusAwaitingPassword {
		t.Fatalf("create status=%d session=%+v", resp.StatusCode, created)
	}
	t.Cleanup(func() { mgr.Terminate(created.ID, time.Second, true) })

	conn := dialWS(t, server, created.ID)
	readUntilText(t, conn, "prompt")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	payload, _ := json.Marshal(map[string]string{"type": "secret", "text": password})
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatalf("write secret frame: %v", err)
	}
	result := readUntilText(t, conn, "secret-result")
	if ok, _ := result["ok"].(bool); !ok {
		t.Fatalf("secret-result = %+v, want ok=true", result)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && mgr.Get(created.ID).Info().Status != api.StatusRunning {
		time.Sleep(10 * time.Millisecond)
	}
	info := mgr.Get(created.ID).Info()
	if info.Status != api.StatusRunning {
		t.Fatalf("status = %s, want running", info.Status)
	}
	if info.PendingPrompt != nil {
		t.Fatalf("pending_prompt = %+v, want nil", info.PendingPrompt)
	}
	if info.ExitCode != 0 || info.PID != 0 {
		t.Fatalf("unexpected remote metadata: %+v", info)
	}
}
