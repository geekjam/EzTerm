#!/usr/bin/env bash
# End-to-end acceptance test for ezterm.
#
# Builds the binary, then exercises the full session lifecycle: auto-spawn,
# start/send/read/terminate/delete, PTY interactivity, --json stability, exit
# codes, and SSH config management. Works on Unix and Windows (Git Bash/MSYS).
#
# Usage: scripts/e2e.sh

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# On Windows the spawned daemon is located via os.Executable(), which requires
# the .exe extension; build the matching binary name.
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    BINNAME="ezterm.exe"
    ;;
  *)
    BINNAME="ezterm"
    ;;
esac
BIN="$ROOT/$BINNAME"
PORT="${EZTERM_E2E_PORT:-18799}"
DATA_DIR="$(mktemp -d)"
PASS=0
FAIL=0
PIPE_SID=""
PTY_SID=""
ATTACH_SID=""

cleanup() {
  if [ -n "$PIPE_SID" ]; then "$BIN" --port "$PORT" --data-dir "$DATA_DIR" terminate "$PIPE_SID" >/dev/null 2>&1 || true; fi
  if [ -n "$PTY_SID" ]; then "$BIN" --port "$PORT" --data-dir "$DATA_DIR" terminate "$PTY_SID" >/dev/null 2>&1 || true; fi
  if [ -n "$ATTACH_SID" ]; then "$BIN" --port "$PORT" --data-dir "$DATA_DIR" terminate "$ATTACH_SID" >/dev/null 2>&1 || true; fi
  # Kill any daemon spawned on our port so subsequent runs start clean.
  if command -v taskkill >/dev/null 2>&1; then
    taskkill //IM ezterm.exe //F >/dev/null 2>&1 || true
  fi
  rm -rf "$DATA_DIR" >/dev/null 2>&1 || true
}
trap cleanup EXIT

say()  { printf '\n== %s ==\n' "$*"; }
ok()   { printf '  PASS: %s\n' "$*"; PASS=$((PASS+1)); }
fail() { printf '  FAIL: %s\n' "$*"; FAIL=$((FAIL+1)); }

# check <description> <actual> <expected-substring>
check() {
  if printf '%s' "$2" | grep -qF -- "$3"; then
    ok "$1"
  else
    fail "$1 (got: $(printf '%s' "$2" | head -c 200))"
  fi
}

# validate_json <json>
validate_json() {
  if command -v jq >/dev/null 2>&1; then
    printf '%s' "$1" | jq empty >/dev/null 2>&1
  elif command -v python3 >/dev/null 2>&1; then
    printf '%s' "$1" | python3 -m json.tool >/dev/null 2>&1
  elif command -v python >/dev/null 2>&1; then
    printf '%s' "$1" | python -m json.tool >/dev/null 2>&1
  else
    return 1
  fi
}

# json_id <json> — extract the session id from a JSON start response.
json_id() {
  printf '%s' "$1" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'
}

# check_id <description> <id> — assert a session id was extracted and is a
# plausible 12-hex-char value (session ids contain no dashes).
check_id() {
  local id="$2"
  if printf '%s' "$id" | grep -qE '^[0-9a-f]{12}$'; then
    ok "$1"
  else
    fail "$1 (got: $(printf '%s' "$id" | head -c 200))"
  fi
}

echo_parts() {
  case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*)
      printf 'cmd\n//c\necho\nE2E-PIPE-MARKER\n'
      ;;
    *)
      printf 'sh\n-c\necho E2E-PIPE-MARKER\n'
      ;;
  esac
}

say "build"
( cd "$ROOT" && go build -o "$BINNAME" . )
ok "binary built ($BINNAME)"

say "auto-spawn: first command starts the daemon"
LIST_JSON="$("$BIN" --port "$PORT" --data-dir "$DATA_DIR" --json list)"
check "daemon is healthy after first CLI call" "$LIST_JSON" '"sessions"'
if validate_json "$LIST_JSON"; then ok "list output is valid JSON"; else fail "list output is not valid JSON: $LIST_JSON"; fi
HEALTH="$("$BIN" --port "$PORT" --data-dir "$DATA_DIR" health)"
check "health reports ok" "$HEALTH" "ok"

say "pipe session: start / read / status"
mapfile -t ECHO < <(echo_parts)
"$BIN" --data-dir "$DATA_DIR" config local --name e2e-pipe \
  --command "${ECHO[0]}" --args "${ECHO[1]}" --args "${ECHO[2]}" --args "${ECHO[3]}" --mode pipe >/dev/null
START_JSON="$("$BIN" --port "$PORT" --data-dir "$DATA_DIR" --json start --name e2e-pipe)"
PIPE_SID="$(json_id "$START_JSON")"
check_id "session id extracted" "$PIPE_SID"
if validate_json "$START_JSON"; then ok "start output is valid JSON"; else fail "start output is not valid JSON"; fi

READ_OUT="$("$BIN" --port "$PORT" --data-dir "$DATA_DIR" read "$PIPE_SID" --timeout 10)"
check "pipe output captured" "$READ_OUT" "E2E-PIPE-MARKER"
READ_JSON="$("$BIN" --port "$PORT" --data-dir "$DATA_DIR" --json read "$PIPE_SID" --timeout 1)"
check "read JSON contains eof" "$READ_JSON" '"eof"'

say "PTY session: interactive input round-trip"
"$BIN" --data-dir "$DATA_DIR" config local --name e2e-pty --mode pty >/dev/null
PTY_START="$("$BIN" --port "$PORT" --data-dir "$DATA_DIR" --json start --name e2e-pty)"
PTY_SID="$(json_id "$PTY_START")"
check_id "pty session id extracted" "$PTY_SID"
"$BIN" --port "$PORT" --data-dir "$DATA_DIR" send "$PTY_SID" --text 'echo E2E-PTY-MARKER' --press-enter >/dev/null
# Poll for the echoed command to appear (PTY input may echo).
PTY_READ=""
for _ in $(seq 1 20); do
  PTY_READ="$PTY_READ$("$BIN" --port "$PORT" --data-dir "$DATA_DIR" read "$PTY_SID" --timeout 1)"
  printf '%s' "$PTY_READ" | grep -qF "E2E-PTY-MARKER" && break
  sleep 0.5
done
check "pty echoed command appears in output" "$PTY_READ" "E2E-PTY-MARKER"

say "attach: replay final screen after session end"
"$BIN" --data-dir "$DATA_DIR" config local --name e2e-attach --mode pty >/dev/null
ATTACH_START="$("$BIN" --port "$PORT" --data-dir "$DATA_DIR" --json start --name e2e-attach)"
ATTACH_SID="$(json_id "$ATTACH_START")"
check_id "attach session id extracted" "$ATTACH_SID"
"$BIN" --port "$PORT" --data-dir "$DATA_DIR" send "$ATTACH_SID" --text 'echo E2E-ATTACH-MARKER' --press-enter >/dev/null
ATTACH_READ=""
for _ in $(seq 1 20); do
  ATTACH_READ="$ATTACH_READ$("$BIN" --port "$PORT" --data-dir "$DATA_DIR" read "$ATTACH_SID" --timeout 1)"
  printf '%s' "$ATTACH_READ" | grep -qF "E2E-ATTACH-MARKER" && break
  sleep 0.5
done
check "attach marker reached buffer" "$ATTACH_READ" "E2E-ATTACH-MARKER"
# End the session, then attach: the final screen is replayed and the stream EOFs.
"$BIN" --port "$PORT" --data-dir "$DATA_DIR" terminate "$ATTACH_SID" >/dev/null 2>&1 || true
ATTACH_OUT="$(curl -s --max-time 10 "http://127.0.0.1:$PORT/api/sessions/$ATTACH_SID/attach")"
check "attach replays the final screen" "$ATTACH_OUT" "E2E-ATTACH-MARKER"
# attach without a terminal exits 2 (stdin is /dev/null).
set +e
"$BIN" --port "$PORT" --data-dir "$DATA_DIR" attach "$ATTACH_SID" </dev/null >/dev/null 2>&1
RC=$?
set -e
if [ "$RC" -eq 2 ]; then ok "attach without a terminal exits 2"; else fail "attach non-tty exit code = $RC, want 2"; fi
"$BIN" --port "$PORT" --data-dir "$DATA_DIR" delete "$ATTACH_SID" >/dev/null 2>&1 || true
ATTACH_SID=""

say "terminate / delete"
TERM_JSON="$("$BIN" --port "$PORT" --data-dir "$DATA_DIR" --json terminate "$PIPE_SID")"
# The pipe session may already have exited naturally; both terminal states pass.
if printf '%s' "$TERM_JSON" | grep -qE '"status":"(terminated|exited)"'; then
  ok "terminate reports a final status"
else
  fail "terminate did not report a final status: $TERM_JSON"
fi
"$BIN" --port "$PORT" --data-dir "$DATA_DIR" delete "$PIPE_SID" >/dev/null
PIPE_SID=""
"$BIN" --port "$PORT" --data-dir "$DATA_DIR" terminate "$PTY_SID" >/dev/null 2>&1 || true
"$BIN" --port "$PORT" --data-dir "$DATA_DIR" delete "$PTY_SID" >/dev/null 2>&1 || true
PTY_SID=""
LIST2="$("$BIN" --port "$PORT" --data-dir "$DATA_DIR" list)"
check "deleted session is gone from list" "$LIST2" "no sessions"

say "exit codes"
set +e
"$BIN" --port "$PORT" --data-dir "$DATA_DIR" read no-such-session --timeout 0 >/dev/null 2>&1
RC=$?
set -e
if [ "$RC" -eq 1 ]; then ok "missing session exits 1"; else fail "missing session exit code = $RC, want 1"; fi
set +e
"$BIN" --port "$PORT" --data-dir "$DATA_DIR" attach no-such-session </dev/null >/dev/null 2>&1
RC=$?
set -e
if [ "$RC" -eq 1 ]; then ok "attach to unknown session exits 1"; else fail "attach unknown exit code = $RC, want 1"; fi

say "config management (no daemon interaction)"
SSH_INIT="$("$BIN" --data-dir "$DATA_DIR" config ssh --name e2eprofile --host 127.0.0.1 --user nobody --auth key --key-path /nonexistent/e2e_key)"
check "ssh config created" "$SSH_INIT" "e2eprofile"
SSH_LIST="$("$BIN" --data-dir "$DATA_DIR" config list)"
check "config listed" "$SSH_LIST" "e2eprofile"
LOCAL_INIT="$("$BIN" --data-dir "$DATA_DIR" config local --name e2elocal --command whoami --mode pipe)"
check "local config created" "$LOCAL_INIT" "e2elocal"
set +e
"$BIN" --port "$PORT" --data-dir "$DATA_DIR" start --name e2eprofile --timeout 2 >/dev/null 2>&1
RC=$?
set -e
if [ "$RC" -eq 2 ]; then ok "unreachable SSH host fails cleanly (exit 2)"; else fail "ssh failure exit code = $RC, want 2"; fi
"$BIN" --data-dir "$DATA_DIR" config delete --name e2eprofile >/dev/null
"$BIN" --data-dir "$DATA_DIR" config delete --name e2elocal >/dev/null

say "results: $PASS passed, $FAIL failed"
if [ "$FAIL" -gt 0 ]; then exit 1; fi
