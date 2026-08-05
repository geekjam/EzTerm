package cli

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ensureDaemon verifies a daemon is healthy on port, spawning one if needed.
func ensureDaemon(port int, dataDir string, logLevel string) error {
	if newClient(port).checkHealth() {
		return nil
	}
	if err := spawnDaemon(port, dataDir, logLevel); err != nil {
		return fmt.Errorf("spawn daemon: %w", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if newClient(port).checkHealth() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("daemon on 127.0.0.1:%d did not become healthy in time", port)
}

// spawnDaemon starts a detached background daemon writing to a log file.
func spawnDaemon(port int, dataDir string, logLevel string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(executable), ".exe") {
		executable += ".exe"
	}
	args := []string{"daemon", "--port", strconv.Itoa(port), "--data-dir", dataDir, "--log-level", logLevel}
	cmd := exec.Command(executable, args...)

	if dataDir != "" {
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return fmt.Errorf("create data directory: %w", err)
		}
		logPath := filepath.Join(dataDir, "ezterm.log")
		logFile, openErr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if openErr != nil {
			return fmt.Errorf("open daemon log: %w", openErr)
		}
		defer logFile.Close()
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}

	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon process: %w", err)
	}
	_ = cmd.Process.Release()
	return nil
}

// tcpPortFree reports whether nothing is listening on 127.0.0.1:port.
func tcpPortFree(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return true
	}
	_ = conn.Close()
	return false
}
