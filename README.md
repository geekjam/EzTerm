# EzTerm

<p align="center">
  <strong>Interactive Terminal Sessions for AI Agents</strong>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.25+-00ADD8.svg" alt="Go 1.25+">
  <img src="https://img.shields.io/badge/Platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey" alt="macOS / Linux / Windows">
  <img src="https://img.shields.io/badge/Skill-agentskills.io-blue.svg" alt="Skill">
  <img src="https://img.shields.io/badge/License-MIT-yellow.svg" alt="MIT License">
</p>

<p align="center">
  <a href="./README.zh.md"><img src="https://img.shields.io/badge/🌏-中文-blue.svg" alt="中文"></a>
</p>

<p align="center">
  <a href="./README.zh.md">中文</a> | <strong>English</strong>
</p>

---

`ezterm` is a small CLI + daemon that lets AI agents (and humans) run, drive,
and monitor **interactive terminal sessions** — REPLs, shells, installers,
servers, and remote SSH — across many turns of send-and-read.

## Quick Start

```bash
# Build the single binary (CLI + daemon in one).
go build -o ezterm .

# First command auto-spawns a background daemon (default port 18766,
# data dir ~/.ezterm).
./ezterm start --command python3 --name repl
./ezterm start --name dev --mode pty --web
./ezterm send <id> --text '2 + 2' --press-enter
./ezterm read <id> --timeout 30

./ezterm terminate <id>
./ezterm delete <id>
```

## Features

- **Interactive sessions** — start a PTY or piped process, send input, and read
  output blockingly or non-blockingly across turns.
- **Auto-spawning daemon** — the background server starts on first use and is
  surfaced as the `ezterm daemon` subcommand.
- **SSH profiles** — connect to remote hosts by a named profile (`password` or
  private-key auth) for interactive or one-shot remote sessions.
- **Stable `--json` output** — machine-readable results for skill/tool use.
- **Multi-reader output buffer** — independent read cursors, retained history,
  and capped blocking reads.
- **Persistence** — `sessions.json` and per-session message logs in the data
  directory; past sessions are restored as history on restart.
- **Cross-platform** — PTY via `creack/pty` on Unix and ConPTY on Windows; pipe
  mode everywhere.
- **Shared terminal attach** — `attach <id>` enters the live PTY session in raw
  mode (like `tmux attach`): keystrokes and resize are forwarded, output is
  streamed back, `Ctrl+]` detaches without stopping the session.
- **Precise key input** — `send --press-key` sends one standard terminal key or
  combination such as `ctrl+c`, `enter`, `ctrl+shift+up`, or `f5` without adding
  a newline; it works with both PTY and pipe sessions.
- **Local Web terminal** — `start --web` exposes an xterm.js browser view for
  an explicitly enabled PTY session, with live output, input, paste, and resize
  over WebSocket. The daemon remains local-only by default.

## Command Line

```
ezterm [global flags] <command> [flags]
```

| Command | Description |
|---|---|
| `start` | Start a session: `--command`, `--args`, `--mode pty\|pipe`, `--name`, `--rows`, `--cols`, `--ssh-config`, `--web` |
| `send <id>` | Send input: `--text`, `--press-enter`, or one key/combination with `--press-key` (for example `ctrl+c`, `enter`, `f5`) |
| `read <id>` | Read new output: `--reader`, `--timeout <sec>`, `--raw`, `--max-bytes` |
| `attach <id>` | Attach to a running PTY session in raw mode; `Ctrl+]` detaches |
| `terminate <id>` | Stop a session (graceful, then force) |
| `delete <id>` | Remove a finished session |
| `list` | List sessions |
| `ssh-config init\|list` | Create / list SSH profiles |
| `health` | Probe the daemon |
| `daemon` | Run the daemon in the foreground |
| `version` | Print the version |

Global flags (valid before or after the command): `--port` (18766), `--data-dir`
(`~/.ezterm`), `--json`, `--log-level`.

**Exit codes:** `0` success · `1` session not found · `2` other errors.

Full examples, SSH profiles, and the skill workflow are in [`SKILL.md`](./SKILL.md).

### Examples

```bash
# Interactive local PTY shell, then a one-shot piped command.
ezterm start --name dev --mode pty
ezterm start --command df --args -h --mode pipe

# Take over the running shell in your own terminal (Ctrl+] to detach).
ezterm attach <id>

# Send one terminal key without a trailing newline.
ezterm send <id> --press-key ctrl+c
# Other examples: ctrl+shift+up, left, enter, f5

# Start a PTY with a local browser terminal and open the printed URL.
ezterm start --name web-dev --mode pty --web

# SSH by profile.
ezterm ssh-config init prod --host db.example.com --user deploy --auth key --key-path ~/.ssh/id_ed25519
ezterm start --ssh-config prod --name db-shell

# Machine-readable.
ezterm --json list
ezterm --json read <id> --timeout 0
```

## Agent Skill

For agents, `ezterm` ships an [agentskills.io](https://agentskills.io)-compatible
skill in [`SKILL.md`](./SKILL.md) (usable by pi, Claude Code, and other
SKILL.md-aware tools). Point your tool at the repository root, then agents can
start/send/read/terminate sessions using the CLI.

## HTTP API

The daemon exposes a small JSON API (`/health`, `/api/sessions`,
`/api/sessions/{id}/output`, `/api/ssh-configs`, …) plus the opt-in Web terminal
page and WebSocket at `/web/{id}` and `/web/{id}/ws`. See
[`API.md`](./API.md).

## Project Structure & Conventions

- [`FileTree.md`](./FileTree.md) — directory layout and purpose of key files.
- [`Standards.md`](./Standards.md) — coding, testing, and documentation
  conventions.
- [`API.md`](./API.md) — HTTP API reference.

## License

MIT — see [`LICENSE`](./LICENSE).
