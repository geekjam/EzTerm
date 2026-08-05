# FileTree — EzTerm Project Structure

This document describes the repository structure of `ezterm` and the purpose
of key files and directories.

> Chinese: [`FileTree.zh.md`](./FileTree.zh.md)

---

## Top Level

```
.
├── main.go                       # Entry point: unified binary (CLI + daemon dispatch)
├── SKILL.md                      # agentskills.io-compatible skill
├── go.mod                        # Go module (ezterm, go 1.25+)
├── go.sum                        # Dependency checksums
├── LICENSE                       # MIT license
├── README.md                     # Project overview, quick start, CLI usage
├── README.zh.md                  # Chinese version of README.md
├── FileTree.md / FileTree.zh.md  # This file (repository structure)
├── Standards.md / Standards.zh.md # Development conventions
├── API.md / API.zh.md            # HTTP API reference
├── .gitignore                    # Binaries, data dirs, private keys
├── internal/                     # Private implementation packages
├── scripts/
│   ├── e2e.sh                    # End-to-end acceptance (Bash / Unix + Git Bash)
│   └── e2e.ps1                   # End-to-end acceptance (PowerShell / Windows)
└── testdata/                     # Test fixtures
```

## internal/ — Implementation Packages

```
internal/
├── ansi/
│   ├── strip.go                  # ANSI escape removal (CSI/OSC/charset)
│   └── compact.go                # Terminal noise compression: CRLF→LF, \r overwrites, blank-line dedup
├── api/
│   └── types.go                  # JSON wire types shared by daemon and CLI
├── buffer/
│   └── buffer.go                 # Append-only output log with multi-reader cursors + prefix trimming
├── cli/
│   ├── cli.go                    # Global flag parsing and subcommand dispatch
│   ├── commands.go               # start/send/read/attach/terminate/delete/list implementations
│   ├── keys.go                   # --press-key parser and VT/xterm key encodings
│   ├── keys_test.go              # Table-driven key parser tests
│   ├── client.go                 # Daemon HTTP client and exit-code mapping
│   ├── attach.go                 # Interactive attach loop (raw-mode pumps, Ctrl+] detach)
│   ├── attach_term.go            # Terminal save/restore + size via golang.org/x/term
│   ├── attach_unix.go            # SIGWINCH-driven terminal resize watcher (Unix)
│   ├── attach_windows.go         # Console-size polling resize watcher (Windows)
│   ├── spawn.go                  # Auto-spawn daemon (probe /health → background launch)
│   ├── spawn_{windows,unix}.go   # Platform-specific process detachment
│   ├── env.go                    # ~/.ezterm resolution and ~ expansion
│   └── sshconfig_cmd.go          # ssh-config init/list local management
├── config/
│   └── config.go                 # Defaults, validation, ~ expansion
├── daemon/
│   ├── daemon.go                 # HTTP server, flags, graceful shutdown
│   ├── handlers.go               # REST/JSON handlers, query parsing, attach stream
│   ├── web.go                    # Embedded Web terminal page + WebSocket bridge
│   └── web/                      # xterm.js page assets (embedded via go:embed)
├── message/
│   └── message.go                # Per-session message index + content files
├── session/
│   ├── session.go                # Session model, state machine, I/O, termination
│   ├── manager.go                # Session registry + persistence + change notification
│   ├── proc_local.go             # Local process abstraction (pty/pipe)
│   ├── proc_remote.go            # Remote SSH process adapter
│   ├── pty_unix.go               # Unix PTY (creack/pty)
│   ├── pty_windows.go            # Windows ConPTY (charmbracelet/x/conpty)
│   └── terminate_{unix,windows}.go # Platform-specific graceful termination
├── sshclient/
│   └── client.go                 # SSH dial, PTY request, stream bridging
├── sshconfig/
│   ├── config.go                 # Profile model and validation
│   └── store.go                  # Profile persistence (data-dir/ssh_configs)
└── storage/
    └── store.go                  # Atomic JSON persistence (temp + fsync + rename)
```

Dependency direction (no cycles): `cli` → `daemon`/`sshconfig`;
`daemon` → `session`/`sshconfig`/`message`/`storage`;
`session` → `buffer`/`ansi`/`message`/`storage`/`sshclient`/`sshconfig`.

Attach flow: the CLI `attach` command opens `GET /api/sessions/{id}/attach`
(daemon streams the raw PTY byte stream, replaying the retained screen first),
forwards keystrokes over `POST /api/sessions/{id}/input` with
`press_enter=false`, and propagates window-size changes via
`POST /api/sessions/{id}/resize`; `Ctrl+]` detaches locally without stopping
the session.

The Web terminal (`start --web`) reuses the same attach primitives
(`AttachReader`/`ReadOutput` for output, `SendInput`/`Resize` for input) over a
WebSocket at `/web/{id}/ws`, so browser tabs share the same PTY screen as
`attach` clients. It is only enabled for sessions started with `--web` and only
in PTY mode.

---

## Data Directory (default `~/.ezterm`)

```
~/.ezterm/
├── sessions.json                 # Session list (atomic rewrite on every change)
├── messages/<session_id>/
│   ├── index.json                # Message index
│   └── messages/<msg_id>.json    # Individual message (input/output/system)
├── ssh_configs/<name>/config.json # SSH profiles (mode 0600)
└── ezterm.log                   # Auto-spawned daemon log
```
