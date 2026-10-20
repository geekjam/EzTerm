# Standards — EzTerm Development Conventions

This document defines the coding style, engineering conventions, and interface
specifications for the `ezterm` project.

> Chinese: [`Standards.zh.md`](./Standards.zh.md)

---

## 1. Go Coding Standards

### Formatting

- **`gofmt` is mandatory.** All Go source files must be `gofmt`-clean; run
  `gofmt -l .` and fix any reported file before committing.
- `go vet ./...` must pass.
- Imports follow the standard `gofmt` grouping: stdlib first, then external
  modules, then `ezterm/...`.

### Naming

- Follow [Go naming conventions](https://go.dev/doc/effective_go#names):
  exported identifiers get doc comments; unexported helpers are lowercase.
- Package names are short, lowercase, single-word (`session`, `buffer`,
  `sshclient`).
- Acronyms are upper-cased (`SSH`, `PTY`, `ID`, `URL`).
- Tests use `_test.go` suffix; test helpers call `t.Helper()`.

### Error handling

- Wrap errors with context: `fmt.Errorf("create session: %w", err)`; never
  swallow errors silently — log them at minimum.
- Use `errors.Is` / `errors.As` for sentinel errors
  (`buffer.ErrClosed`, `storage.ErrCorrupt`, `sshconfig.ErrNotFound`).
- Return sentinel errors at package boundaries where callers branch.

### Concurrency

- Session state writes are serialized: stdin goes through `stdinMu`; status and
  exit code are written exactly once via `sync.Once` (see `exitOnce`).
- Termination is idempotent: `terminateOnce` (sync.Once) guards `Terminate`.
- Any new shared state must be protected by its own mutex or `sync.Once`, with
  the ownership of each lock documented in a comment.
- Prefer `context.Context` for cancellation over ad-hoc goroutine flags.

### Platform-specific code

- PTY implementations live behind the `ptySession` interface
  (`pty_unix.go` / `pty_windows.go`) with `//go:build` tags.
- Termination differs per platform (`terminate_unix.go` /
  `terminate_windows.go`); keep the public `proc` interface platform-neutral.
- Windows specifics (ConPTY, `CREATE_NO_WINDOW`) are isolated in tagged files.

---

## 2. Testing Standards

- **Table-driven tests** are the default for pure logic.
- New functionality ships with tests covering the happy path, error path, and
  (where relevant) concurrency.
- Session tests use a helper subprocess (`--` argument mode) so the same
  harness works through the HTTP API and cross-platform.
- `go test ./...` must pass, including `-race` for the internal packages.
- `testdata/` is for fixtures only; never write runtime state there.

---

## 3. Commit Conventions

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <subject>
```

- **Types:** `feat`, `fix`, `docs`, `refactor`, `chore`, `test`, `ci`.
- **Scope** (optional): package or area, e.g. `session`, `cli`, `daemon`,
  `sshconfig`, `storage`.
- **Subject:** imperative, lowercase; Chinese is acceptable for internal-facing
  notes but prefix with the English type.
- Each commit is a single logical change.

Examples:

```
feat(session): add PTY lifecycle with platform ptySession adapters
fix(cli): accept flags after positional session IDs
docs: add HTTP API reference
```

---

## 4. Documentation Conventions

- **English is the default language.** Chinese versions use the `.zh.md`
  suffix (`README.zh.md`, `FileTree.zh.md`, `Standards.zh.md`, `API.zh.md`).
- Every bilingual file links to its counterpart at the top.
- Root-level project docs: `README.md`, `FileTree.md`, `Standards.md`,
  `API.md`.
- Keep a single source of truth: link instead of duplicating facts.

---

## 5. Interface Specifications

- The complete HTTP API is specified in [`API.md`](./API.md); every daemon
  endpoint must be documented there.
- `internal/api/types.go` is the single source of truth for JSON wire types.
- The CLI is the primary consumer; exit codes are stable: `0` success, `1`
  session not found, `2` other errors. `--json` output is stable and has no
  human decoration.
- `send --press-key` accepts one case-insensitive key expression per
  invocation. The CLI emits VT/xterm bytes through `InputRequest.Text` with
  `PressEnter=false`; it is mutually exclusive with `--text` and
  `--press-enter`, and invalid expressions return exit code `2`.
- Backward compatibility: removing or renaming an endpoint or CLI flag is a
  breaking change; coordinate it with a version bump.
- **WebSocket frames:** binary frames carry raw terminal bytes; text frames
  carry JSON control messages. The complete list is `{"type":"resize",…}` and
  `{"type":"secret",…}` from the browser, and `{"type":"prompt",…}`,
  `{"type":"prompt-cleared"}`, `{"type":"secret-result",…}`, and
  `{"type":"session-end"}` from the server. Any new control message must be
  documented in `API.md`.
- **Secret handling (`secret` frames):** a submitted password is only accepted
  from a loopback peer and is written straight to the SSH dial or the PTY. The
  daemon itself never records it: no input message, no `slog` entry, no HTTP
  response, no `sessions.json` field, and no frame generated by the daemon. (A
  program that echoes its input can still print the secret into the terminal
  stream; password prompts normally disable echo.) Do not add a CLI or HTTP
  path that carries a password, and do not read a secret from process arguments
  or environment variables. Terminal prompt detection lives in
  `internal/prompt` and must stay a pure, testable package (rule table + tail
  detector, no I/O).
- **Web UI copy is English**, matching the CLI output and the API docs; the
  embedded pages have no build step, so keep them dependency-light.
- **Security:** never commit credentials, keys, or private material. SSH
  profiles may contain passwords/keys in the user's `~/.ezterm` data dir but
  must never ship in the repository or the profile skeleton. Prefer a profile
  with no stored password (the Web terminal asks on every start) when writing
  docs or examples.

## 6. Dependencies

- WebSocket support uses `github.com/coder/websocket` (context-cancellable, no
  transitive dependencies).
- The Web terminal page is served from `go:embed`-ed assets under
  `internal/daemon/web/`; there is no front-end build step. xterm.js is loaded
  from a CDN at runtime and is not vendored.
