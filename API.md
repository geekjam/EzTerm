# API — EzTerm HTTP API Reference

The `ezterm` daemon exposes a small JSON API on `127.0.0.1:<port>` (default
`18766`). The CLI is the primary consumer; all endpoints are also usable
directly over HTTP.

> Chinese: [`API.zh.md`](./API.zh.md)

## Conventions

- Base URL: `http://127.0.0.1:<port>`
- Request bodies are JSON (`Content-Type: application/json`); request bodies
  are capped at 1 MiB.
- Responses are JSON objects unless noted. Errors use
  `{"error": "<message>"}` with an appropriate status code.
- Times are RFC 3339 UTC.

## Endpoints

### `GET /health`

Probe endpoint used by the CLI to detect a live daemon.

```json
{"status": "ok"}
```

### `GET /api/sessions`

List all sessions (including finished history), ordered by creation time.

```json
{"sessions": [ {"id": "a1b2c3d45678", "name": "dev", "status": "exited", …} ]}
```

### `GET /api/sessions/{id}`

Get one session. Returns `404` if unknown.

```json
{"id": "a1b2c3d45678", "name": "dev", "command": "", "args": null,
 "mode": "pty", "status": "running", "pid": 1234, "exit_code": 0,
 "rows": 24, "cols": 80, "ssh_config": "",
 "web_url": "http://127.0.0.1:18766/web/a1b2c3d45678",
 "created_at": "2026-08-02T10:00:00Z",
 "updated_at": "2026-08-02T10:00:00Z", "finished_at": null}
```

| Field | Type | Notes |
|---|---|---|
| `id` | string | 12-char lowercase hexadecimal session ID |
| `name` | string | defaults to `session-<id>` |
| `command` / `args` | string / string[] | process to run; empty = default shell |
| `mode` | `"pty"` \| `"pipe"` | PTY (echoes input) or plain pipes |
| `status` | `"starting"` \| `"running"` \| `"awaiting_password"` \| `"exited"` \| `"terminated"` | lifecycle state |
| `pid` | int | process ID (`0` for remote sessions and while awaiting a password) |
| `exit_code` | int | meaningful once exited/terminated |
| `rows` / `cols` | int | PTY dimensions |
| `ssh_config` | string | `""`/`"internal"` = local; else profile name |
| `web_url` | string | Web terminal URL (`/web/<id>`); empty when the page is disabled or the session is `pipe` mode |
| `pending_prompt` | object, omitted | active password prompt; see below |
| `created_at` / `updated_at` / `finished_at` | string / null | timestamps |

`pending_prompt` is present only while the session waits for a secret. It
never contains the secret itself:

```json
{"kind": "ssh_password", "label": "Password for deploy@db.example.com:22"}
{"kind": "terminal_password", "label": "[sudo] password for deploy:"}
```

- `ssh_password` — the SSH config has password authentication but no stored
  password; the session dials only after the secret is submitted.
- `terminal_password` — the session output currently shows a password prompt
  (`sudo`, `su`, `passwd`, nested `ssh`, …).

### `POST /api/sessions`

Create and start a session. Returns `201` with the session, `400` for general
creation errors (e.g. SSH dial failure or missing profile), or `409` when an
explicit `web: true` is combined with pipe mode, or when the SSH config needs a
password prompt but the Web terminal is disabled.

The Web terminal is **enabled by default**: omit `web` to serve `/web/{id}` for
a PTY session. An omitted `web` is silently dropped for pipe sessions (which
cannot provide terminal semantics), while an explicit `web: false` disables the
page for any mode.

When `ssh_config` names a password-auth profile with **no stored password**, the
session is created in `awaiting_password` state: the response is still `201`
with `web_url` and `pending_prompt`, but no connection is dialed until the
password is submitted over the WebSocket (see below). Such a config with
`web: false` (or a pipe mode) returns `409` — there would be no way to supply
the secret.

```json
{"command": "python3", "args": ["-i"], "mode": "pty",
 "name": "repl", "rows": 24, "cols": 80,
 "ssh_config": "internal", "web": true, "dial_timeout_seconds": 15}
```

| Field | Type | Notes |
|---|---|---|
| `command` | string | executable; empty with `ssh_config` uses the profile default shell |
| `args` | string[] | command arguments |
| `mode` | string | `"pty"` (default) or `"pipe"` |
| `name` | string | optional session name |
| `rows` / `cols` | int | PTY size (defaults 24×80) |
| `ssh_config` | string | `""` or `"internal"` = local host; otherwise a profile name |
| `web` | bool | Web terminal page; **defaults to `true`**. PTY only: omitted + pipe is ignored, explicit `true` + pipe returns `409`, `false` disables the page (and `409` if a password prompt would be needed) |
| `dial_timeout_seconds` | int | SSH dial timeout (default 15) |

### `POST /api/sessions/{id}/input`

Write input to a session. Returns `{"ok": true}`; `409` if the session is no
longer running.

```json
{"text": "2 + 2", "press_enter": true}
```

`press_enter: true` appends `\n` (Unix) or `\r\n` (Windows).

`--press-key` bytes from the CLI (key names, control bytes, CSI sequences)
are transported in `text` with `press_enter: false`; the protocol is
unchanged.

### `GET /api/sessions/{id}/output`

Read new output for a reader. Query parameters:

| Param | Default | Notes |
|---|---|---|
| `reader_id` | `0` | reader cursor; `0` is the default CLI reader |
| `timeout` | `30` | block up to N seconds (float); `0` = non-blocking; capped at 300 |
| `raw` | `false` | keep ANSI escape sequences |
| `max_bytes` | `0` | limit returned bytes; `0` = unlimited |

```json
{"data": "4\n", "eof": false}
```

`eof: true` means the session exited and all retained output was consumed.

### `GET /api/sessions/{id}/attach`

Stream the raw PTY output of a session to an attach client. The stream starts
by replaying everything retained in the output buffer (restoring the current
screen), then follows live output until the session ends (stream EOF) or the
client disconnects.

| Condition | Response |
|---|---|
| session not found | `404` |
| session is `pipe` mode | `409` (attach requires a PTY session) |
| success | `200`, `Content-Type: application/octet-stream` |

This is not JSON: the body is the raw terminal byte stream (including ANSI
escape sequences), flushed per chunk. The CLI `attach` command consumes this
endpoint and sends keystrokes via `POST /input` with `press_enter: false`.
Multiple clients can attach to the same session concurrently; all share the
same screen. Attaching to an ended session replays the final screen and then
closes.

### `POST /api/sessions/{id}/readers`

Allocate a new reader positioned at the current end of output. Returns `201`:

```json
{"reader_id": 2}
```

### `POST /api/sessions/{id}/terminate`

Stop a session (graceful signal, then forced kill). Query parameters:
`force=true` (skip grace) and `grace=<seconds>` (default 5). An
`awaiting_password` session has no process yet: terminating it ends the wait
immediately (the output records that no password was supplied).

```json
{"session": {"id": "a1b2c3d45678", "name": "dev", "status": "terminated", …}}
```

### `DELETE /api/sessions/{id}`

Remove a finished session. Returns `204`; `409` if the session is still
running or `awaiting_password` (terminate it first).

### `POST /api/sessions/{id}/resize`

Resize the PTY.

```json
{"rows": 40, "cols": 120}
```

Returns the updated session. The attach client resizes the session to match
its local terminal on connect and whenever the window size changes.

### `GET /web/{id}`

Serve the embedded Web terminal page for a session with the page enabled (the
default for PTY sessions; disabled by `web: false`).
The page loads xterm.js from a CDN at runtime (no build step) and connects to
`/web/{id}/ws`. It is only reachable from the machine the daemon binds to
(default `127.0.0.1`); there is no authentication.

| Condition | Response |
|---|---|
| session not found | `404` |
| session is `pipe` mode | `409` (web terminal requires a PTY session) |
| session was created with `web: false` | `404` |
| success | `200`, `text/html` |

Closing the browser tab does not stop the session; it keeps running exactly as
with `attach`. Multiple browser tabs are treated like multiple attach clients
and share the same PTY screen.

The page reports disconnects with an in-page modal dialog: "Session
disconnected" when the process ended (the `session-end` frame below), and
"Connection lost" when the WebSocket broke while the session kept running, in
which case the page reconnects automatically and restores the terminal.

When the session is waiting for a password (either kind of `pending_prompt`),
the page shows a password dialog instead. The secret is typed by the user,
sent over the WebSocket, and never stored: it is not written to messages, to
the log, or back to the terminal.

### `GET /web/{id}/ws`

WebSocket endpoint used by the embedded page. The same access rules as
`GET /web/{id}` apply. Accepts the request host's origin by default and only
listens on the daemon bind address.

Protocol (one bidirectional connection):

- Server → client **binary frames** carry raw PTY output bytes (ANSI
  included), starting with a replay of the retained screen.
- Client → server **binary frames** carry raw input bytes (keystrokes,
  function keys, paste) and are written to the session stdin as-is.
- Client → server **text frames** carry JSON control messages: `resize` and
  `secret`.
- Server → client **text frames** carry JSON status messages: `prompt`,
  `prompt-cleared`, `secret-result`, and `session-end`.

Client → server:

```json
{"type": "resize", "rows": 40, "cols": 120}
{"type": "secret", "text": "<password>"}
```

- `resize` updates the PTY size. For an `awaiting_password` session it is
  recorded and succeeds, so opening the page does not fail before dialing.
- `secret` submits a password. It is only honored when the connection comes
  from a loopback address; from any other peer the server replies with
  `secret-result{ok:false}` and ignores the payload. The text is never
  logged, never written as an input message, and never echoed back.

Server → client:

```json
{"type": "prompt", "kind": "ssh_password", "label": "Password for u@h:22"}
{"type": "prompt-cleared"}
{"type": "secret-result", "ok": true}
{"type": "secret-result", "ok": false, "error": "ssh: authentication failed: …"}
{"type": "session-end"}
```

- `prompt` announces an active password prompt. It is replayed immediately
  after the connection opens if one is already pending, so a reconnect (or a
  page opened later) still shows the dialog.
- `prompt-cleared` reports that the prompt disappeared: it was answered, or
  the program printed a newline and moved on.
- `secret-result` reports the outcome of a `secret` frame. On `ok: false` the
  page shows the error and keeps the dialog; an `ssh_password` failure has
  already ended the session, so `session-end` follows.
- `session-end` is sent once, after the process exits and all retained output
  has been sent, immediately before the server closes the connection with a
  normal closure. A transport failure (network drop, daemon restart, page
  unload) never produces this frame, so a client can tell "the session
  finished" apart from "the connection broke".

When the session ends and all retained output has been sent, the server closes
the connection with a normal closure.

### `GET /api/configs`

List launch config summaries (non-secret fields) for both local and SSH configs.

```json
{"configs": [
  {"name": "dev", "type": "local", "command": "bash", "mode": "pty"},
  {"name": "prod", "type": "ssh", "host": "db.example.com", "port": 22,
   "user": "deploy", "auth_method": "key", "default_shell": ""}
]}
```

### `GET /api/configs/{name}`

Get one config's full non-secret detail (a local config includes its `args`;
SSH configs never include the stored password). Returns `404` if unknown.

```json
{"name": "dev", "type": "local", "command": "bash", "args": ["-l"], "mode": "pty"}

{"name": "prod", "type": "ssh", "host": "db.example.com", "port": 22,
 "user": "deploy", "auth_method": "password", "key_path": "", "shell": "",
 "has_password": true, "password_prompt": false}
```

`has_password` reports whether a password is stored (the value itself is never
returned). `password_prompt` means the profile has password authentication but
no stored password: the Web terminal asks for it on every start.

### `POST /api/configs/{name}`

Create or update a config. The body's `type` (`local` or `ssh`) decides which
store is written. Saving over an existing config of the same type overwrites
it; reusing a name owned by the other type returns `409`. A local pipe config
requires a non-empty `command`.

```json
{"type": "local", "command": "bash", "args": ["-l"], "mode": "pty"}

{"type": "ssh", "host": "db.example.com", "port": 22, "user": "deploy",
 "auth_method": "password", "password": "...", "key_path": "", "shell": "/bin/bash"}

{"type": "ssh", "host": "db.example.com", "port": 22, "user": "deploy",
 "auth_method": "password", "password_prompt": true}
```

The SSH `password` is **write-only**: it is never returned by any endpoint. On
an update to an existing password-auth SSH config, leaving `password` empty
preserves the stored value. Sending `password_prompt: true` clears any stored
password and selects "ask in the Web terminal on every start"; a new
password-auth config with neither a password nor a stored value is valid and
also means the password is asked each time. A key-auth config with no
`key_path` returns `400`. Config names match `[A-Za-z0-9_-]+`.

| Condition | Response |
|---|---|
| success (create or update) | `200` with the saved `ConfigDetail` |
| missing `command` for a local pipe config | `400` |
| cross-type name already in use | `409` |
| invalid type, missing key path | `400` |

### `DELETE /api/configs/{name}`

Delete a config by name (either type). Returns `204`; `404` if unknown.

### `GET /config`

The embedded, dark-themed web configuration page. It lists configs and uses
the endpoints above to create, edit, and delete local and SSH configs, served
from the same binary (no build step; `config.js` and `config.css` are at
`/config/app.js` and `/config/style.css`). Like the Web terminal and all `/api/*`
routes, it is reachable only on the daemon bind address (default `127.0.0.1`)
and has no authentication. Closing the page does not affect running sessions.

## Session Lifecycle

```
POST /api/sessions          → starting → running
POST /api/sessions (empty-password SSH) → awaiting_password
  ├─ secret submitted, dial ok        → running
  ├─ secret submitted, dial/auth fail → exited (exit code 1)
  ├─ no secret within 300 s           → terminated (timeout note in output)
  └─ POST .../terminate               → terminated
process exits naturally     → exited
POST .../terminate          → terminated (then exited semantics on teardown)
DELETE /api/sessions/{id}   → removed from list (must be finished)
```

Sessions that exit naturally stay in the list until deleted. On daemon restart,
previously running (and `awaiting_password`) sessions are restored as `exited`
history records; messages remain readable from the data directory. A restored
session never keeps a pending password prompt.
