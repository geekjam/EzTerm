# End-to-end acceptance test for ezterm (PowerShell).
#
# Builds the binary, then exercises the full session lifecycle: auto-spawn,
# start/send/read/terminate/delete, PTY interactivity, --json stability, exit
# codes, and SSH config management.
#
# Usage: powershell -ExecutionPolicy Bypass -File scripts/e2e.ps1

# Windows PowerShell treats native stderr as an error under "Stop"; use
# "Continue" and rely on explicit $LASTEXITCODE checks instead.
$ErrorActionPreference = "Continue"

$Root = Split-Path -Parent $PSScriptRoot
$Bin = Join-Path $Root "ezterm.exe"
$Port = if ($env:EZTERM_E2E_PORT) { $env:EZTERM_E2E_PORT } else { "18799" }
$DataDir = Join-Path ([IO.Path]::GetTempPath()) ("ezterm-e2e-" + [guid]::NewGuid().ToString("N"))

$script:PASS = 0
$script:FAIL = 0
function Say($m) { Write-Host "`n== $m ==" }
function Ok($m) { Write-Host "  PASS: $m"; $script:PASS++ }
function Fail($m) { Write-Host "  FAIL: $m"; $script:FAIL++ }

# ---- build ----
Say "build"
Push-Location $Root
& go build -o ezterm.exe .
if ($LASTEXITCODE -ne 0) { throw "build failed" }
Pop-Location
Ok "binary built"

# ---- auto-spawn ----
Say "auto-spawn: first command starts the daemon"
$list = & $Bin --port $Port --data-dir $DataDir --json list | Out-String
if ($list -match '"sessions"') { Ok "daemon healthy after first CLI call" } else { Fail "list output missing sessions key: $list" }
try {
    $null = $list | ConvertFrom-Json
    Ok "list output is valid JSON"
} catch {
    Fail "list output is not valid JSON"
}

# ---- pipe session ----
Say "pipe session: start / read"
& $Bin --data-dir $DataDir config local --name e2e-pipe --command cmd --args /c --args echo --args E2E-PIPE-MARKER --mode pipe | Out-Null
if ($LASTEXITCODE -ne 0) { throw "config local (pipe) failed" }
$startJson = & $Bin --port $Port --data-dir $DataDir --json start --name e2e-pipe | Out-String
$sid = ($startJson | ConvertFrom-Json).session.id
if (-not $sid) { throw "could not extract session id from: $startJson" }
Ok "session id extracted"
$readOut = & $Bin --port $Port --data-dir $DataDir read $sid --timeout 10 | Out-String
if ($readOut -match "E2E-PIPE-MARKER") { Ok "pipe output captured" } else { Fail "pipe output missing: $readOut" }

# ---- PTY session ----
Say "PTY session: interactive input round-trip"
& $Bin --data-dir $DataDir config local --name e2e-pty --mode pty | Out-Null
if ($LASTEXITCODE -ne 0) { throw "config local (pty) failed" }
$ptyStart = & $Bin --port $Port --data-dir $DataDir --json start --name e2e-pty | Out-String
$psid = ($ptyStart | ConvertFrom-Json).session.id
if (-not $psid) { throw "could not extract PTY session id" }
& $Bin --port $Port --data-dir $DataDir send $psid --text 'echo E2E-PTY-MARKER' --press-enter | Out-Null
$ptyOut = ""
for ($i = 0; $i -lt 20; $i++) {
    $chunk = & $Bin --port $Port --data-dir $DataDir read $psid --timeout 1 2>$null
    $ptyOut += $chunk
    if ($ptyOut -match "E2E-PTY-MARKER") { break }
    Start-Sleep -Milliseconds 500
}
if ($ptyOut -match "E2E-PTY-MARKER") { Ok "pty echoed command appears in output" } else { Fail "pty echo missing: $ptyOut" }

# ---- attach ----
Say "attach: replay final screen after session end"
& $Bin --data-dir $DataDir config local --name e2e-attach --mode pty | Out-Null
if ($LASTEXITCODE -ne 0) { throw "config local (attach) failed" }
$attachStart = & $Bin --port $Port --data-dir $DataDir --json start --name e2e-attach | Out-String
$attachSid = ($attachStart | ConvertFrom-Json).session.id
if (-not $attachSid) { throw "could not extract attach session id" }
Ok "attach session id extracted"
& $Bin --port $Port --data-dir $DataDir send $attachSid --text 'echo E2E-ATTACH-MARKER' --press-enter | Out-Null
$attachRead = ""
for ($i = 0; $i -lt 20; $i++) {
    $attachRead += & $Bin --port $Port --data-dir $DataDir read $attachSid --timeout 1 2>$null
    if ($attachRead -match "E2E-ATTACH-MARKER") { break }
    Start-Sleep -Milliseconds 500
}
if ($attachRead -match "E2E-ATTACH-MARKER") { Ok "attach marker reached buffer" } else { Fail "attach marker missing: $attachRead" }
& $Bin --port $Port --data-dir $DataDir terminate $attachSid | Out-Null
# The ended session replays its final screen through the attach stream, then EOF.
$attachOut = ""
try {
    $attachResp = Invoke-WebRequest -Uri "http://127.0.0.1:$Port/api/sessions/$attachSid/attach" -TimeoutSec 10 -UseBasicParsing
    $attachOut = $attachResp.Content
    if ($attachOut -is [byte[]]) { $attachOut = [Text.Encoding]::UTF8.GetString($attachOut) }
} catch {
    $attachOut = ""
}
if ($attachOut -match "E2E-ATTACH-MARKER") { Ok "attach replays the final screen" } else { Fail "attach replay missing: $attachOut" }
& $Bin --port $Port --data-dir $DataDir delete $attachSid | Out-Null

# ---- terminate / delete ----
Say "terminate / delete"
& $Bin --port $Port --data-dir $DataDir terminate $sid | Out-Null
& $Bin --port $Port --data-dir $DataDir delete $sid | Out-Null
& $Bin --port $Port --data-dir $DataDir terminate $psid | Out-Null
& $Bin --port $Port --data-dir $DataDir delete $psid | Out-Null
$list2 = & $Bin --port $Port --data-dir $DataDir list | Out-String
if ($list2 -match "no sessions") { Ok "sessions removed from list" } else { Fail "sessions remain: $list2" }

# ---- exit codes ----
Say "exit codes"
& $Bin --port $Port --data-dir $DataDir read no-such-session --timeout 0 2>$null | Out-Null
if ($LASTEXITCODE -eq 1) { Ok "missing session exits 1" } else { Fail "missing session exit = $LASTEXITCODE, want 1" }
& $Bin --port $Port --data-dir $DataDir attach no-such-session 2>$null | Out-Null
if ($LASTEXITCODE -eq 1) { Ok "attach to unknown session exits 1" } else { Fail "attach unknown exit = $LASTEXITCODE, want 1" }

# ---- config ----
Say "config management"
& $Bin --data-dir $DataDir config ssh --name e2eprofile --host 127.0.0.1 --user nobody --auth key --key-path C:\nonexistent\e2e_key | Out-Null
if ($LASTEXITCODE -ne 0) { throw "config ssh failed" }
$sshList = & $Bin --data-dir $DataDir config list | Out-String
if ($sshList -match "e2eprofile") { Ok "ssh config listed" } else { Fail "ssh config missing: $sshList" }
& $Bin --data-dir $DataDir config local --name e2elocal --command whoami --mode pipe | Out-Null
if ($LASTEXITCODE -ne 0) { throw "config local failed" }
& $Bin --port $Port --data-dir $DataDir start --name e2eprofile --timeout 2 2>$null | Out-Null
if ($LASTEXITCODE -eq 2) { Ok "unreachable SSH host fails cleanly (exit 2)" } else { Fail "ssh failure exit = $LASTEXITCODE, want 2" }
& $Bin --data-dir $DataDir config delete --name e2eprofile | Out-Null
& $Bin --data-dir $DataDir config delete --name e2elocal | Out-Null

# ---- cleanup ----
& taskkill /IM ezterm.exe /F 2>$null | Out-Null
Remove-Item -Recurse -Force $DataDir -ErrorAction SilentlyContinue

Write-Host "`nresults: $($script:PASS) passed, $($script:FAIL) failed"
if ($script:FAIL -gt 0) { exit 1 }
