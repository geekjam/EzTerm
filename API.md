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
 "rows": 24, "cols": 80, "ssh_config": "", "web_url": "",
 "created_at": "2026-08-02T10:00:00Z",
 "updated_at": "2026-08-02T10:00:00Z", "finished_at": null}
```

| Field | Type | Notes |
|---|---|---|
| `id` | string | 12-char lowercase hexadecimal session ID |
| `name` | string | defaults to `session-<id>` |
| `command` / `args` | string / string[] | process to run; empty = default shell |
| `mode` | `"pty"` \| `"pipe"` | PTY (echoes input) or plain pipes |
| `status` | `"starting"` \| `"running"` \| `"exited"` \| `"terminated"` | lifecycle state |
| `pid` | int | process ID (`0` for remote sessions) |
| `exit_code` | int | meaningful once exited/terminated |
| `rows` / `cols` | int | PTY dimensions |
| `ssh_config` | string | `""`/`"internal"` = local; else profile name |
| `web_url` | string | Web terminal URL (`/web/<id>`); empty when `--web` was not used |
| `created_at` / `updated_at` / `finished_at` | string / null | timestamps |

### `POST /api/sessions`

Create and start a session. Returns `201` with the session, `400` for general
creation errors (e.g. SSH dial failure or missing profile), or `409` when
`web` is combined with pipe mode.

```json
{"command": "python3", "args": ["-i"], "mode": "pty",
 "name": "repl", "rows": 24, "cols": 80,
 "ssh_config": "internal", "web": false, "dial_timeout_seconds": 15}
```

| Field | Type | Notes |
|---|---|---|
| `command` | string | executable; empty with `ssh_config` uses the profile default shell |
| `args` | string[] | command arguments |
| `mode` | string | `"pty"` (default) or `"pipe"` |
| `name` | string | optional session name |
| `rows` / `cols` | int | PTY size (defaults 24×80) |
| `ssh_config` | string | `""` or `"internal"` = local host; otherwise a profile name |
| `web` | bool | enable the Web terminal page; PTY mode only (`409` for pipe) |
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
`force=true` (skip grace) and `grace=<seconds>` (default 5).

```json
{"session": {"id": "a1b2c3d45678", "name": "dev", "status": "terminated", …}}
```

### `DELETE /api/sessions/{id}`

Remove a finished session. Returns `204`; `409` if the session is still
running (terminate it first).

### `POST /api/sessions/{id}/resize`

Resize the PTY.

```json
{"rows": 40, "cols": 120}
```

Returns the updated session. The attach client resizes the session to match
its local terminal on connect and whenever the window size changes.

### `GET /web/{id}`

Serve the embedded Web terminal page for a session created with `web: true`.
The page loads xterm.js from a CDN at runtime (no build step) and connects to
`/web/{id}/ws`. It is only reachable from the machine the daemon binds to
(default `127.0.0.1`); there is no authentication.

| Condition | Response |
|---|---|
| session not found | `404` |
| session is `pipe` mode | `409` (web terminal requires a PTY session) |
| session was created without `web: true` | `404` |
| success | `200`, `text/html` |

Closing the browser tab does not stop the session; it keeps running exactly as
with `attach`. Multiple browser tabs are treated like multiple attach clients
and share the same PTY screen.

### `GET /web/{id}/ws`

WebSocket endpoint used by the embedded page. The same access rules as
`GET /web/{id}` apply. Accepts the request host's origin by default and only
listens on the daemon bind address.

Protocol (one bidirectional connection):

- Server → client **binary frames** carry raw PTY output bytes (ANSI
  included), starting with a replay of the retained screen.
- Client → server **binary frames** carry raw input bytes (keystrokes,
  function keys, paste) and are written to the session stdin as-is.
- Client → server **text frames** carry resize JSON:

```json
{"type": "resize", "rows": 40, "cols": 120}
```

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
 "user": "deploy", "auth_method": "password", "key_path": "", "shell": ""}
```

### `POST /api/configs/{name}`

Create or update a config. The body's `type` (`local` or `ssh`) decides which
store is written. Saving over an existing config of the same type overwrites
it; reusing a name owned by the other type returns `409`. A local pipe config
requires a non-empty `command`.

```json
{"type": "local", "command": "bash", "args": ["-l"], "mode": "pty"}

{"type": "ssh", "host": "db.example.com", "port": 22, "user": "deploy",
 "auth_method": "password", "password": "...", "key_path": "", "shell": "/bin/bash"}
```

The SSH `password` is **write-only**: it is never returned by any endpoint. On
an update to an existing password-auth SSH config, leaving `password` empty
preserves the stored value. A new password-auth config with no password, or a
key-auth config with no `key_path`, returns `400`. Config names match
`[A-Za-z0-9_-]+`.

| Condition | Response |
|---|---|
| success (create or update) | `200` with the saved `ConfigDetail` |
| missing `command` for a local pipe config | `400` |
| cross-type name already in use | `409` |
| invalid type, missing SSH credential | `400` |

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
process exits naturally     → exited
POST .../terminate          → terminated (then exited semantics on teardown)
DELETE /api/sessions/{id}   → removed from list (must be finished)
```

Sessions that exit naturally stay in the list until deleted. On daemon restart,
previously running sessions are restored as `exited` history records; messages
remain readable from the data directory.
