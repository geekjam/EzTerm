package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ezterm/internal/api"
	"ezterm/internal/message"
	"ezterm/internal/session"
	"ezterm/internal/sshconfig"
	"ezterm/internal/storage"
	"github.com/coder/websocket"
)

func TestDaemonHelperProcess(t *testing.T) {
	mode := daemonHelperMode()
	if mode == "" {
		t.Skip("not a helper subprocess")
	}
	switch mode {
	case "output":
		fmt.Fprintln(os.Stdout, "DAEMON-STDOUT")
		fmt.Fprintln(os.Stderr, "DAEMON-STDERR")
		os.Exit(0)
	case "interactive":
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			fmt.Fprintf(os.Stdout, "daemon-echo:%s\n", scanner.Text())
		}
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

func daemonHelperMode() string {
	for i := 0; i < len(os.Args)-1; i++ {
		if os.Args[i] == "--" {
			return os.Args[i+1]
		}
	}
	return ""
}

func daemonHelperArgs(mode string) []string {
	return []string{"-test.run=^TestDaemonHelperProcess$", "--", mode}
}

func testHandler(t *testing.T) (*httptest.Server, *session.Manager) {
	t.Helper()
	dir := t.TempDir()
	store := storage.New(dir)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	msgMgr := message.NewManager(store)
	sshStore := sshconfig.NewStore(dir)
	mgr := session.NewManager(store, msgMgr, sshStore)
	server := httptest.NewServer(NewHandler(mgr, sshStore))
	t.Cleanup(server.Close)
	return server, mgr
}

func doJSON(t *testing.T, client *http.Client, method, url string, request any, response any) *http.Response {
	t.Helper()
	var body io.Reader
	if request != nil {
		data, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if request != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response != nil && len(data) > 0 {
		if err := json.Unmarshal(data, response); err != nil {
			t.Fatalf("decode %s %s: %v; body=%q", method, url, err, data)
		}
	}
	return resp
}

func TestHealthAndSessionEndpoints(t *testing.T) {
	server, mgr := testHandler(t)
	client := server.Client()

	resp := doJSON(t, client, http.MethodGet, server.URL+"/health", nil, &api.HealthResponse{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", resp.StatusCode)
	}

	create := api.CreateSessionRequest{
		Command: os.Args[0],
		Args:    daemonHelperArgs("output"),
		Mode:    api.ModePipe,
		Name:    "http-output",
	}
	var created api.Session
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/sessions", create, &created)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	if created.ID == "" || created.Name != "http-output" {
		t.Fatalf("unexpected session: %+v", created)
	}

	var listed struct {
		Sessions []api.Session `json:"sessions"`
	}
	resp = doJSON(t, client, http.MethodGet, server.URL+"/api/sessions", nil, &listed)
	if resp.StatusCode != http.StatusOK || len(listed.Sessions) != 1 {
		t.Fatalf("list status=%d sessions=%+v", resp.StatusCode, listed.Sessions)
	}

	var output api.OutputResponse
	resp = doJSON(t, client, http.MethodGet, server.URL+"/api/sessions/"+created.ID+"/output?timeout=5", nil, &output)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("output status = %d", resp.StatusCode)
	}
	if !strings.Contains(output.Data, "DAEMON-STDOUT") || !strings.Contains(output.Data, "DAEMON-STDERR") {
		t.Fatalf("merged output = %q", output.Data)
	}

	var eof api.OutputResponse
	resp = doJSON(t, client, http.MethodGet, server.URL+"/api/sessions/"+created.ID+"/output?timeout=0", nil, &eof)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("EOF output status = %d", resp.StatusCode)
	}

	resp = doJSON(t, client, http.MethodGet, server.URL+"/api/sessions/no-such", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing session status = %d", resp.StatusCode)
	}

	// Let the naturally-exited session finish writing its message files before
	// the temporary data directory is removed by t.Cleanup.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.Get(created.ID).Info().Status != api.StatusRunning {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	mgr.Wait(created.ID)
}

func TestInputTerminateDeleteEndpoints(t *testing.T) {
	server, _ := testHandler(t)
	client := server.Client()
	create := api.CreateSessionRequest{
		Command: os.Args[0],
		Args:    daemonHelperArgs("interactive"),
		Mode:    api.ModePipe,
		Name:    "http-interactive",
	}
	var created api.Session
	resp := doJSON(t, client, http.MethodPost, server.URL+"/api/sessions", create, &created)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}

	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/sessions/"+created.ID+"/input", api.InputRequest{Text: "ping", PressEnter: true}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("input status = %d", resp.StatusCode)
	}

	var output api.OutputResponse
	resp = doJSON(t, client, http.MethodGet, server.URL+"/api/sessions/"+created.ID+"/output?timeout=5", nil, &output)
	if resp.StatusCode != http.StatusOK || !strings.Contains(output.Data, "daemon-echo:ping") {
		t.Fatalf("output status=%d data=%q", resp.StatusCode, output.Data)
	}

	var terminated api.TerminateResponse
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/sessions/"+created.ID+"/terminate?grace=1", nil, &terminated)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("terminate status = %d, body=%+v", resp.StatusCode, terminated)
	}
	if terminated.Session.Status != api.StatusTerminated {
		t.Fatalf("terminated status = %s", terminated.Session.Status)
	}

	resp = doJSON(t, client, http.MethodDelete, server.URL+"/api/sessions/"+created.ID, nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
}

// drainAttach feeds every chunk read from an attach stream into the returned
// channel until EOF; used to consume a stream asynchronously with deadlines.
func drainAttach(body io.Reader) <-chan string {
	ch := make(chan string, 16)
	go func() {
		defer close(ch)
		buf := make([]byte, 4096)
		for {
			n, err := body.Read(buf)
			if n > 0 {
				ch <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
}

// waitAttachText consumes an attach stream until text appears and returns all
// bytes read so far; it fails if the stream ends or the deadline passes first.
func waitAttachText(t *testing.T, ch <-chan string, text string) string {
	t.Helper()
	var sb strings.Builder
	deadline := time.After(15 * time.Second)
	for {
		select {
		case chunk, ok := <-ch:
			if !ok {
				t.Fatalf("attach stream ended before %q appeared; got %q", text, sb.String())
			}
			sb.WriteString(chunk)
			if strings.Contains(sb.String(), text) {
				return sb.String()
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q in attach stream; got %q", text, sb.String())
		}
	}
}

// waitAttachClosed consumes an attach stream until it reaches EOF.
func waitAttachClosed(t *testing.T, ch <-chan string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("attach stream did not close")
		}
	}
}

// waitForBufferText polls an attach reader (which replays from the start of
// retained output) until text lands in the session output buffer, e.g. a PTY
// echo of sent input. Replaying from the start avoids races where the echo
// lands between the send and the reader registration.
func waitForBufferText(t *testing.T, s *session.Session, text string) {
	t.Helper()
	rid, err := s.AttachReader()
	if err != nil {
		t.Fatal(err)
	}
	defer s.ReleaseReader(rid)
	var sb strings.Builder
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, _, readErr := s.ReadOutput(context.Background(), rid, 250*time.Millisecond, 0, false)
		if readErr != nil {
			t.Fatal(readErr)
		}
		sb.WriteString(data)
		if strings.Contains(sb.String(), text) {
			return
		}
	}
	t.Fatalf("timed out waiting for %q in session buffer; got %q", text, sb.String())
}

func TestAttachEndpoint(t *testing.T) {
	server, mgr := testHandler(t)
	client := server.Client()

	// Unknown session -> 404.
	resp := doJSON(t, client, http.MethodGet, server.URL+"/api/sessions/no-such/attach", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session attach status = %d, want 404", resp.StatusCode)
	}

	// Pipe-mode session -> 409 with a clear error.
	create := api.CreateSessionRequest{Command: os.Args[0], Args: daemonHelperArgs("interactive"), Mode: api.ModePipe}
	var pipeSession api.Session
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/sessions", create, &pipeSession)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create pipe session status = %d", resp.StatusCode)
	}
	resp = doJSON(t, client, http.MethodGet, server.URL+"/api/sessions/"+pipeSession.ID+"/attach", nil, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("pipe session attach status = %d, want 409", resp.StatusCode)
	}

	// PTY session: replay retained output, then live output, then EOF on end.
	ptyCreate := api.CreateSessionRequest{Command: os.Args[0], Args: daemonHelperArgs("interactive"), Mode: api.ModePTY, Rows: 24, Cols: 80}
	var ptySession api.Session
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/sessions", ptyCreate, &ptySession)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create pty session status = %d", resp.StatusCode)
	}
	s := mgr.Get(ptySession.ID)
	if s == nil {
		t.Fatal("manager missing created PTY session")
	}

	// Prime the buffer so the attach stream has something to replay.
	marker1 := "attach-marker-one"
	if err := s.SendInput(marker1, true); err != nil {
		t.Fatal(err)
	}
	waitForBufferText(t, s, marker1)

	url := server.URL + "/api/sessions/" + ptySession.ID + "/attach"

	// Two clients attach concurrently and must both receive the same stream.
	first := openAttach(t, client, url)
	defer first.Close()
	second := openAttach(t, client, url)
	defer second.Close()
	ch1 := drainAttach(first)
	ch2 := drainAttach(second)

	got1 := waitAttachText(t, ch1, marker1)
	got2 := waitAttachText(t, ch2, marker1)
	if !strings.Contains(got1, marker1) || !strings.Contains(got2, marker1) {
		t.Fatalf("attach replay missing marker: first=%q second=%q", got1, got2)
	}

	// Live output written after attach must reach every client.
	marker2 := "attach-marker-two"
	if err := s.SendInput(marker2, true); err != nil {
		t.Fatal(err)
	}
	if got := waitAttachText(t, ch1, marker2); !strings.Contains(got, marker2) {
		t.Fatalf("live output missing on client 1: %q", got)
	}
	if got := waitAttachText(t, ch2, marker2); !strings.Contains(got, marker2) {
		t.Fatalf("live output missing on client 2: %q", got)
	}

	// Client disconnect on a live session: the handler must return promptly.
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	discResp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	waitAttachClosed(t, drainAttach(discResp.Body))
	discResp.Body.Close()

	// Session end: both attach streams must reach EOF.
	mgr.Terminate(ptySession.ID, time.Second, true)
	waitAttachClosed(t, ch1)
	waitAttachClosed(t, ch2)

	mgr.Terminate(pipeSession.ID, time.Second, true)
}

// openAttach opens an attach stream and asserts a 200 response.
func openAttach(t *testing.T, client *http.Client, url string) io.ReadCloser {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("attach status = %d, body = %q", resp.StatusCode, body)
	}
	return resp.Body
}

func TestWebTerminalEndpoints(t *testing.T) {
	server, mgr := testHandler(t)
	client := server.Client()

	// Unknown sessions are hidden behind the normal not-found response.
	resp := doJSON(t, client, http.MethodGet, server.URL+"/web/no-such", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown web page status = %d, want 404", resp.StatusCode)
	}

	createWeb := api.CreateSessionRequest{
		Command: os.Args[0],
		Args:    daemonHelperArgs("interactive"),
		Mode:    api.ModePTY,
		Name:    "web-terminal",
		Rows:    24,
		Cols:    80,
		Web:     true,
	}
	var webSession api.Session
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/sessions", createWeb, &webSession)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create web session status = %d", resp.StatusCode)
	}
	if webSession.WebURL == "" || !strings.HasSuffix(webSession.WebURL, "/web/"+webSession.ID) {
		t.Fatalf("unexpected web URL: %q", webSession.WebURL)
	}

	pageResp, err := client.Get(server.URL + "/web/" + webSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	pageData, err := io.ReadAll(pageResp.Body)
	pageResp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if pageResp.StatusCode != http.StatusOK || !strings.Contains(string(pageData), "@xterm/xterm") {
		t.Fatalf("web page status=%d body=%q", pageResp.StatusCode, pageData)
	}

	for _, asset := range []struct {
		path        string
		contentType string
		marker      string
	}{
		{path: "/web/app.js", contentType: "text/javascript", marker: "new WebSocket"},
		{path: "/web/style.css", contentType: "text/css", marker: "#terminal"},
	} {
		assetResp, getErr := client.Get(server.URL + asset.path)
		if getErr != nil {
			t.Fatal(getErr)
		}
		data, readErr := io.ReadAll(assetResp.Body)
		assetResp.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if assetResp.StatusCode != http.StatusOK || !strings.Contains(assetResp.Header.Get("Content-Type"), asset.contentType) || !strings.Contains(string(data), asset.marker) {
			t.Fatalf("asset %s status=%d type=%q body=%q", asset.path, assetResp.StatusCode, assetResp.Header.Get("Content-Type"), data)
		}
	}

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/web/" + webSession.ID + "/ws"
	wsCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(wsCtx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial web terminal: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"resize","rows":31,"cols":101}`)); err != nil {
		t.Fatalf("send resize: %v", err)
	}
	resizeDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(resizeDeadline) {
		info := mgr.Get(webSession.ID).Info()
		if info.Rows == 31 && info.Cols == 101 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if info := mgr.Get(webSession.ID).Info(); info.Rows != 31 || info.Cols != 101 {
		t.Fatalf("resize not applied: rows=%d cols=%d", info.Rows, info.Cols)
	}

	marker := "web-terminal-marker"
	if err := conn.Write(context.Background(), websocket.MessageBinary, []byte(marker+"\n")); err != nil {
		t.Fatalf("send web input: %v", err)
	}
	readCtx, readCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer readCancel()
	var output strings.Builder
	for {
		typ, data, readErr := conn.Read(readCtx)
		if readErr != nil {
			t.Fatalf("read web output: %v; output=%q", readErr, output.String())
		}
		if typ == websocket.MessageBinary {
			output.Write(data)
			if strings.Contains(output.String(), marker) {
				break
			}
		}
	}

	// PTY sessions without --web are not exposed, while pipe sessions use a
	// conflict response because they cannot provide terminal semantics.
	createPlain := api.CreateSessionRequest{
		Command: os.Args[0],
		Args:    daemonHelperArgs("interactive"),
		Mode:    api.ModePTY,
	}
	var plainSession api.Session
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/sessions", createPlain, &plainSession)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create plain PTY status = %d", resp.StatusCode)
	}
	resp = doJSON(t, client, http.MethodGet, server.URL+"/web/"+plainSession.ID, nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("plain PTY web status = %d, want 404", resp.StatusCode)
	}

	createPipe := api.CreateSessionRequest{
		Command: os.Args[0],
		Args:    daemonHelperArgs("interactive"),
		Mode:    api.ModePipe,
	}
	var pipeSession api.Session
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/sessions", createPipe, &pipeSession)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create pipe status = %d", resp.StatusCode)
	}
	resp = doJSON(t, client, http.MethodGet, server.URL+"/web/"+pipeSession.ID, nil, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("pipe web status = %d, want 409", resp.StatusCode)
	}

	// --web with pipe mode is rejected at creation time.
	createInvalid := createPipe
	createInvalid.Web = true
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/sessions", createInvalid, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("web pipe creation status = %d, want 409", resp.StatusCode)
	}

	mgr.Terminate(webSession.ID, time.Second, true)
	mgr.Terminate(plainSession.ID, time.Second, true)
	mgr.Terminate(pipeSession.ID, time.Second, true)
}

func TestReaderAndSSHConfigEndpoints(t *testing.T) {
	server, mgr := testHandler(t)
	client := server.Client()
	create := api.CreateSessionRequest{Command: os.Args[0], Args: daemonHelperArgs("interactive"), Mode: api.ModePipe}
	var created api.Session
	resp := doJSON(t, client, http.MethodPost, server.URL+"/api/sessions", create, &created)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}

	var reader api.ReaderResponse
	resp = doJSON(t, client, http.MethodPost, server.URL+"/api/sessions/"+created.ID+"/readers", nil, &reader)
	if resp.StatusCode != http.StatusCreated || reader.ReaderID < 0 {
		t.Fatalf("reader status=%d reader=%+v", resp.StatusCode, reader)
	}

	var profiles struct {
		Profiles []api.SSHProfileSummary `json:"ssh_configs"`
	}
	resp = doJSON(t, client, http.MethodGet, server.URL+"/api/ssh-configs", nil, &profiles)
	if resp.StatusCode != http.StatusOK || len(profiles.Profiles) != 0 {
		t.Fatalf("SSH config status=%d profiles=%+v", resp.StatusCode, profiles.Profiles)
	}

	// Ensure the test manager has the same session the API created and clean it up.
	if mgr.Get(created.ID) == nil {
		t.Fatal("manager did not register created session")
	}
	_ = mgr.Terminate(created.ID, time.Second, true)
}
