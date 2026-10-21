---
name: ezterm
description: >-
  Use when you need to connect to a specified server or terminal — start,
  drive, and read interactive sessions (REPLs, installers, servers, remote
  SSH shells) and file transfer through the ezterm HTTP API on 127.0.0.1:18766. 
---

# EzTerm — interactive terminal sessions over HTTP

`ezterm` runs interactive processes (locally or over SSH) as **sessions** and
lets you send input and read new output across multiple turns — like a real
terminal, but driven entirely over HTTP. Every response is JSON.

## Build the binary

```bash
go build .        # from the EzTerm source root -> ./ezterm (ezterm.exe on Windows)
```

**Prefer an existing compiled binary**: look in the `skills` directory (the
binary is normally placed next to `SKILL.md`) before compiling.

## Start the daemon

The daemon must be running before any call. Probe first, start only if it fails:

```bash
curl -s http://127.0.0.1:18766/health          # -> {"status":"ok"}
```

```bash
# Bash / Git Bash / macOS / Linux — start detached (use ./ezterm.exe on Windows)
(nohup ./ezterm daemon >/dev/null 2>&1 &)
```

```powershell
# PowerShell / Windows
Start-Process -WindowStyle Hidden ezterm.exe -ArgumentList 'daemon'
```

Default bind `127.0.0.1:18766`.
Request bodies are JSON (`Content-Type: application/json`, max 1 MiB). Errors
are `{"error":"..."}` with the matching status code.

## Configure terminal

# Use the following command to open the webpage for terminal configuration.
```bash
ezterm config web --open      # ensures the daemon runs, opens http://127.0.0.1:18766/config
```

(or open `http://127.0.0.1:18766/config` in a browser.)

## Endpoints

Notation: `?a=1&b=2` = query · `{...}` = JSON body · `?` suffix = optional ·
`->` = success response.

### health

```yaml
GET /health -> {status:"ok"}
```

### create session

```yaml
# local: command/args/mode | remote: ssh_config:"<profile>", command empty = profile default shell
# mode defaults to pty ; web defaults to true (PTY only) and fills Session.web_url
# web:false disables it ; omitted + pipe is ignored ; web:true + pipe = 409 ; 400 = create failed
# status may be awaiting_password: the session needs a password typed by the user in
# Session.web_url; 409 = that config needs a password prompt but web:false was sent
POST /api/sessions {command?,args?,mode?,name?,rows?,cols?,ssh_config?,web?,dial_timeout_seconds?} -> 201 Session
```

`Session` fields you rely on: `id`, `name`, `status`, `exit_code`, `mode`,
`ssh_config`, `web_url`, and `pending_prompt` (`{kind,label}` or absent).
`status` is one of `starting`, `running`, `awaiting_password`, `exited`,
`terminated`.

### list sessions

```yaml
GET /api/sessions -> {sessions:[Session]}
```

### get session

```yaml
GET /api/sessions/{id} -> Session          # 404 = unknown id
```

### write input

```yaml
# press_enter appends \n (Unix) / \r\n (Windows) ; key bytes: see Key bytes
POST /api/sessions/{id}/input {text,press_enter?} -> {ok:true}     # 409 = not running
```

### read output

```yaml
# timeout: block up to N seconds, 0 = non-blocking, cap 300 ; raw:true keeps ANSI
# eof:true = process exited and all retained output was consumed
GET /api/sessions/{id}/output ?reader_id=0&timeout=30&raw=false&max_bytes=0 -> {data,eof}
```

### new reader

```yaml
# independent cursor positioned at the current end of output
POST /api/sessions/{id}/readers -> 201 {reader_id}
```

### files

```yaml
# running sessions only
GET  /api/sessions/{id}/files ?path=           -> {path,parent,root,entries:[{name,path,dir,symlink,size,mode,modified}]}
POST /api/sessions/{id}/files {path}           -> 201 {file}       # mkdir; parent must exist
DELETE /api/sessions/{id}/files ?path=         -> 204             # file, link, or EMPTY dir only
GET  /api/sessions/{id}/files/content ?path=   -> raw bytes       # attachment, not JSON
POST /api/sessions/{id}/files/content ?path=&overwrite=true  body=raw octet-stream -> {file}
```

### resize

```yaml
POST /api/sessions/{id}/resize {rows,cols} -> Session
```

### terminate

```yaml
# graceful signal, then forced kill; idempotent
POST /api/sessions/{id}/terminate ?force=false&grace=5 -> {session}
```

### delete

```yaml
DELETE /api/sessions/{id} -> 204           # must be finished; 409 while running
```

### list configs

```yaml
GET /api/configs -> {configs:[...]}
```

### get config

```yaml
GET /api/configs/{name} -> ConfigDetail    # stored password is never returned
# has_password = a secret is stored ; password_prompt = ask in the Web terminal each start
```

### delete config

```yaml
DELETE /api/configs/{name} -> 204
```

## Key bytes

Send terminal keys as raw bytes in `input.text` with `press_enter:false` —
e.g. Ctrl+C to interrupt a REPL, arrows to drive a TUI menu. JSON escapes:

| key | JSON string | key | JSON string |
|---|---|---|---|
| enter | `"\r"` | backspace | `"\u007f"` |
| tab | `"\t"` | delete / insert | `"\u001b[3~"` / `"\u001b[2~"` |
| esc | `"\u001b"` | home / end | `"\u001b[H"` / `"\u001b[F"` |
| ctrl+a…ctrl+z | `"\u0001"`…`"\u001a"` (ctrl+c=`"\u0003"`, ctrl+d=`"\u0004"`) | pageup / pagedown | `"\u001b[5~"` / `"\u001b[6~"` |
| up / down / right / left | `"\u001b[A"` / `B` / `C` / `D` | f1–f4 | `"\u001bOP"`…`"\u001bOS"` |
| alt+x | `"\u001bx"` | f5 / f12 | `"\u001b[15~"` / `"\u001b[24~"` |
| ctrl+shift+up | `"\u001b[1;6A"` (modifier code: shift=2, alt=3, ctrl=5, ctrl+shift=6, ctrl+alt=7, all=8) | | |

`ctrl+c` is the C0 byte `0x03`; whether it interrupts depends on the program and
platform (console programs on Windows may ignore it) — fall back to `terminate`.

## Safety

- **Credential safety** — `~/.ezterm` (or a custom data dir) holds plaintext SSH
  passwords, private key paths, transcripts, and logs; treat it as secret.
  Never read, list, or open files there, and never print, repeat, or commit
  credentials. Use the API (`GET /api/configs`, `GET /api/sessions`, `output`)
  instead, and redact anything credential-looking.
- **Password prompts** — `status:"awaiting_password"` or a `pending_prompt`
  means only the human can supply the secret. Tell the user to open
  `Session.web_url` and type it there. Never ask for the password in chat and
  never send it via `input` or `/api/configs`. Then poll
  `GET /api/sessions/{id}` until `running`, `exited`, or `terminated` (300 s).
- **Entering credentials** — prefer a config that stores **no** password
  (`auth_method:"password"` with no `password`, i.e. `password_prompt: true`):
  the user types it into the Web terminal on every start. Otherwise have the
  user type the secret into the `/config` page so it stays off the command line
  and out of transcripts, and reference configs by name (`"ssh_config":"prod"`).
  Never send a password in `POST /api/configs/{name}` unless the user explicitly
  supplied it. Stored passwords are write-only and are never returned by any
  endpoint.
