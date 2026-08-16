---
name: ezterm
description: >-
  Use when you need to run, control, and interact with long-running or
  interactive processes: REPLs, interactive installers, servers, terminal
  programs, or remote SSH shells. ezterm manages terminal sessions through a
  small CLI and background daemon, supporting start / send / read / attach / terminate /
  delete / list operations, SSH profiles, and machine-readable --json output.
  Never reads or exposes stored SSH credentials (passwords or private keys).
---

# EzTerm — interactive terminal sessions for agents

`ezterm` starts interactive processes (locally or over SSH) and lets you send
input and read output across multiple turns — like a real terminal, but from a
command line that agents can drive. The daemon auto-starts on first use.

## When to use this skill

- A command needs **input after launch** (REPL, `npm create`, `ssh`, `mysql`,
  `python -i`, debuggers, installers with prompts).
- You need to **keep a process alive between turns** and read its output later.
- You need an **interactive shell on a remote host** without writing one-shot
  SSH command lines.

For one-shot commands that return immediately, prefer a normal `bash` tool.

## Install & first run

```bash
# Compile the binary if it does not exist
go build .                 # from the EzTerm SKILL source code root

# Don't waste time conducting overly in-depth searches.
# Recommend compiling the binary files into the SKILL directory for easy retrieval.

# Everything below auto-starts the daemon on first use (default port 18766,
# data dir ~/.ezterm). Point at a custom data dir with --data-dir.
```

## Command reference

Global flags (can appear before or after the subcommand):
`--port <n>` (default 18766), `--data-dir <dir>` (default `~/.ezterm`),
`--json` (machine-readable output), `--log-level <debug|info|warn|error>`.

| Command | Purpose |
|---|---|
| `ezterm start --name <config> [--web] [--rows 24] [--cols 80] [--timeout s]` | Start a session from a saved config (created with `config local/ssh`). Default mode is from the config; local pipe configs keep their mode. `--web` exposes a local browser terminal (PTY only) and defaults off. |
| `ezterm send <id> --text <text> [--press-enter]` or `ezterm send <id> --press-key <key>` | Write input to a session. Use `--press-key` to send one terminal key/combination such as `ctrl+c`, `enter`, or `f5` (see Press-key input). |
| `ezterm read <id> [--reader 0] [--timeout 30] [--raw] [--max-bytes n]` | Read new output since the last read. Blocks up to `--timeout` seconds (0 = non-blocking). Strips ANSI unless `--raw`. Returns EOF when the process exited and output is drained. |
| `ezterm attach <id>` | Enter a running PTY in raw mode. Output is rendered live; keystrokes and resize are forwarded; `Ctrl+]` detaches without stopping the session. |
| `ezterm terminate <id>` | Stop a session (graceful, then force). |
| `ezterm delete <id>` | Remove a finished session from the list. |
| `ezterm list` | List sessions (ID / name / mode / status / ssh / exit / command). |
| `ezterm config local --name <name> [--command <cmd>] [--args a] [--mode pty\|pipe]` | Create/update a local launch config. `--name` is the config name (also selected by `start --name`). `--command` empty = default shell. `--mode` defaults to `pty`. |
| `ezterm config ssh --name <name> --host <host> --user <u> --auth password --password <p> [--port 22] [--shell <s>]` or `... --auth key --key-path <path>` | Create/update an SSH launch config; choose exactly one auth mode and provide its credential parameter. |
| `ezterm config list [--type local\|ssh]` | List saved configs. |
| `ezterm config delete --name <name>` | Delete a saved config by name. |
| `ezterm health` / `ezterm version` | Probe the daemon / print version. |

Exit codes: `0` success, `1` session not found, `2` other errors (daemon
unreachable, invalid input, conflicts).

## Typical workflow

```bash
# 1. Define a local config and start it (recommend --web for a browser view).
ezterm config local --name dev --command bash --mode pty
# → local config "dev" saved
ezterm start --name dev --web
# → session a1b2c3d45678 created
#   web terminal: http://127.0.0.1:18766/web/a1b2c3d45678
# You must provide users with the URL of the web terminal is launching.

# 2. Send a command.
ezterm send a1b2c3d45678 --text 'python -i' --press-enter

# 3. Read output (block up to 30s for new output).
ezterm read a1b2c3d45678 --timeout 30
# → Python 3.12.5 ... >>>

# 4. Keep interacting across turns.
ezterm send a1b2c3d45678 --text '2 + 2' --press-enter
ezterm read a1b2c3d45678 --timeout 10
# → 4

# 5. Clean up.
ezterm terminate a1b2c3d45678
ezterm delete a1b2c3d45678
```

## Attach Workflow

Use `attach` when a human or agent needs a real terminal view of a running PTY
session. It replays the retained ANSI screen, then follows live output. Input
is forwarded byte-for-byte, including Ctrl+C, function keys, Tab, and paste.
`Ctrl+]` detaches and leaves the session running; process exit closes the
attach stream and restores the local terminal.

```bash
# Start a PTY session and capture its generated ID.
ezterm config local --name dev --mode pty
ezterm start --name dev

# In another terminal, connect to the same session.
ezterm attach <id>

# Resize follows the local terminal automatically. To detach, press Ctrl+].
# The session remains available for another attach client.
```

Attach is supported only for PTY sessions. `--json` is not supported because
attach is an interactive raw-terminal command.

## Web terminal

When connecting to a server or starting a long-running interactive session,
**recommend enabling a Web terminal** (`start --web`). Tell the user it opens a
browser view with live output and interaction, and ask whether they want it
enabled unless their workflow clearly doesn't need it. It gives a browser tab
that behaves like `attach` (live output, input, paste, resize) without
holding a terminal open.

```bash
# Start a PTY session with a browser view.
ezterm config local --name dev --mode pty
ezterm start --name dev --web
# → session a1b2c3d45678 created
#   web terminal: http://127.0.0.1:18766/web/a1b2c3d45678

# Open the printed URL in a browser to watch and take over.
# Closing the tab does not stop the session; it keeps running in the background.
```

- **Local-only** — the daemon binds to `127.0.0.1` by default, so the URL is
  only reachable from the same machine. No authentication is added.
- **PTY only** — `--web` with `--mode pipe` returns an error.
- **Opt-in per session** — only sessions started with `--web` are exposed;
  existing sessions and sessions without this flag are unchanged. Prefer
  starting server connections and interactive sessions with `--web`.
  `start --web` prints the URL, and `--json` returns it in the session's
  `web_url` field.
- **Interaction** — the browser page uses xterm.js from a CDN (needs network on
  the machine opening it). There is no `Ctrl+]` detach in the browser; just
  close or switch tabs, and reconnect whenever you like. The session keeps
  running either way.

For non-interactive one-shot output use `--mode pipe` to avoid terminal echo:

```bash
ezterm config local --name one-shot --command sh --args -c --args 'echo hello' --mode pipe
ezterm start --name one-shot
ezterm read <id> --timeout 5
# → hello
```

## Press-key input

Use `send --press-key` to send a single terminal key or combination instead of
typed text — for example to send Ctrl+C to a REPL, navigate a TUI menu, or
press a function key. Each invocation sends exactly one key and never appends
a newline.

```bash
ezterm send <id> --press-key ctrl+c            # interrupt (C0 byte 0x03)
ezterm send <id> --press-key enter             # carriage return (CR), no newline
ezterm send <id> --press-key ctrl+shift+up     # modifier combination
ezterm send <id> --press-key f5                 # function key
ezterm send <id> --press-key left               # arrow key
```

- **Key names** — lowercase: `enter`, `tab`, `esc`, `space`, `backspace`,
  `delete`, `insert`, `home`, `end`, `pageup`, `pagedown`,
  `up`/`down`/`left`/`right`, `f1`–`f12`, or a plain single character
  (`a`, `1`, `?`). Names and modifiers are case-insensitive.
- **Modifiers** — combine with `+`: `ctrl`, `alt`, `shift` (`ctrl+c`,
  `ctrl+shift+up`, `alt+left`). `ctrl`+letter sends the C0 control byte;
  `alt` wraps the key with an ESC prefix; `ctrl`/`shift` on navigation and
  function keys send xterm modified CSI sequences (`ctrl+shift+up` →
  `ESC[1;6A`).
- **Exclusive** — `--press-key` conflicts with `--text` and `--press-enter`
  (exit code 2). It never appends a newline; `enter` sends CR (0x0D).
- **Both modes** — works with PTY and pipe sessions; the effect is chosen by
  the receiving program.
- Unknown keys (`foo`, `f13`), duplicate modifiers (`ctrl+ctrl`), or
  undefined combinations (`ctrl+1`, `shift+1`) print a clear error and exit
  with code 2.

## Saved local and SSH configs

Create a config once, then start sessions by config name. Config names are
unique across local and SSH types:

```bash
# Local interactive shell.
ezterm config local --name dev --command bash --mode pty
ezterm start --name dev --web

# One-shot local command.
ezterm config local --name disk --command df --args -h --mode pipe
ezterm start --name disk

# Remote SSH shell.
ezterm config ssh --name prod --host db.example.com --user deploy --auth key --key-path ~/.ssh/id_ed25519
# Credentials are finalized out-of-band by the user in the stored config;
# do not read or edit files under ~/.ezterm/configs/ (see Credential safety).
ezterm start --name prod

ezterm config list
```

Sessions use the profile's credentials (password or private key); private keys
are never embedded in the repository or config template.

## Credential safety — read this before using SSH

The default data directory `~/.ezterm` (or any custom `--data-dir`) contains
**sensitive material**: SSH configs with **plaintext passwords** and private
key paths (`configs/ssh.json`, mode 0600), session transcripts
that may include password prompts and typed secrets (`messages/`), and a daemon
log. Treat every file there as a secret.

**Never** do any of the following:

- **Do not read, `cat`, `type`, `head`, `tail`, or open any file under
  `~/.ezterm`** — not `configs/`, not `sessions.json`, not `messages/`,
  not `ezterm.log`. This includes listing the directory with `ls`/`find`.
- **Do not print, paste, quote, or repeat passwords or private keys** in tool
  calls, replies, logs, diffs, or any file.
- **Do not repeat credential-looking output.** If a `read` returns a password
  prompt or a secret that was typed into a session (PTY echo), redact it —
  never quote it back.
- **Never pass a password through `send` into a session** unless the user
  explicitly asked; the typed password would then be stored in the transcript
  under `messages/`.
- **Never commit credentials to the repository** or include them in any
  generated file or patch.

Instead:

- Use `ezterm config list` (non-secret summaries) and `ezterm list` —
  never raw file reads.
- Reference configs by **name only**: `ezterm start --name prod`.
  The daemon loads SSH credentials itself.
- To create or fix a config's credentials, ask the user to run
  `ezterm config ssh --name prod ...`; do not read the config file afterward.
- `config ssh --password` takes a password on the command line; prefer it
  only when the user supplied the value, and never echo it later.

## JSON output

Add `--json` for stable, parseable output (no human decoration):

```json
{"session": {"id": "a1b2c3d45678", "name": "dev", "mode": "pty", "status": "running",
 "command": "", "args": null, "ssh_config": "", "web_url": "", "pid": 1234, "rows": 24, "cols": 80,
 "created_at": "2026-08-02T10:00:00Z", "updated_at": "2026-08-02T10:00:00Z", "finished_at": null}}
{"data": "hello\n", "eof": false}
{"sessions": [...]}
{"error": "..."}
```

## Notes & pitfalls

- **PTY mode echoes input** — what you `send` appears in `read` output; strip it
  mentally or use `--mode pipe` for clean capture.
- **First `read` replays from the start** of the session (prompt, banner).
  Subsequent reads return only new output. Use `--reader` for independent
  cursors when consuming output from multiple places.
- **Blocking reads** — `read` without `--timeout 0` blocks until output arrives
  or the timeout expires. Prefer short timeouts and poll.
- **ANSI escapes are stripped** by default for clean text; keep them with
  `--raw` if you must render a real terminal screen.
- **Attach is PTY-only** — pipe sessions return a conflict error. Multiple
  attach clients share one PTY screen and receive the same output. `attach`
  always keeps ANSI bytes because it renders the actual PTY screen.
- **Web terminal is PTY-only and opt-in** — `--web` is enabled only for the
  session it was passed to and only in PTY mode; the URL is local-only and
  closing the tab leaves the session running.
- **`--press-key` sends one key only** — one key/combination per invocation,
  no trailing newline, mutually exclusive with `--text` and `--press-enter`;
  see Press-key input for the key names and encodings.
- **Fast commands** — `start` returns immediately; the process may exit before
  your first `read`. Reads still return all retained output, then `eof: true`.
- **Termination** sends a graceful signal, then forces after a short grace;
  `terminate` is idempotent.
- **Data directory** holds `sessions.json`, `messages/`, and `configs/`
  (`configs/ssh.json` stores **plaintext passwords**, mode 0600). Never read
  files under the data dir directly — use `ezterm list` / `ezterm read` /
  `ezterm config list` — and follow the **Credential safety** rules above.
  Restarting the daemon restores past (finished) sessions as history.
