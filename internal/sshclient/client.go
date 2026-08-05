// Package sshclient establishes remote SSH sessions for ezterm.
package sshclient

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Options selects the authentication method and connection parameters.
type Options struct {
	Host        string
	Port        int
	User        string
	Password    string
	KeyPath     string
	DialTimeout time.Duration
}

// RemoteSession is the minimal surface the daemon needs from a live SSH command.
type RemoteSession interface {
	Wait() <-chan struct{}
	ExitCode() int
	Stdin() io.Writer
	Resize(rows, cols int) error
	Terminate()
}

// remoteSession is a live SSH command channel with bridged streams.
type remoteSession struct {
	client   *ssh.Client
	session  *ssh.Session
	stdin    io.WriteCloser
	copyWG   sync.WaitGroup
	done     chan struct{}
	exitCode int
}

// NewClientConfig builds an ssh.ClientConfig from the supplied options.
func NewClientConfig(opts Options) (*ssh.ClientConfig, error) {
	if strings.TrimSpace(opts.Host) == "" {
		return nil, fmt.Errorf("sshclient: host is required")
	}
	user := strings.TrimSpace(opts.User)
	if user == "" {
		return nil, fmt.Errorf("sshclient: user is required")
	}
	if opts.Port == 0 {
		opts.Port = 22
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 15 * time.Second
	}

	var authMethods []ssh.AuthMethod
	if opts.Password != "" {
		authMethods = append(authMethods, ssh.Password(opts.Password))
	}
	if opts.KeyPath != "" {
		key, err := loadPrivateKey(opts.KeyPath)
		if err != nil {
			return nil, err
		}
		authMethods = append(authMethods, ssh.PublicKeys(key))
	}
	if len(authMethods) == 0 {
		return nil, fmt.Errorf("sshclient: no authentication method configured (password or key_path)")
	}

	return &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TODO: support known_hosts verification
		Timeout:         opts.DialTimeout,
	}, nil
}

// loadPrivateKey reads a PEM private key, with optional passphrase support.
func loadPrivateKey(path string) (ssh.Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sshclient: read key %s: %w", path, err)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("sshclient: parse key %s: %w", path, err)
	}
	return signer, nil
}

// Start dials the remote host and starts a command, optionally requesting a PTY.
// out receives the merged stdout/stderr stream; the caller owns closing it.
func Start(opts Options, command string, args []string, usePTY bool, rows, cols int, out io.Writer) (RemoteSession, error) {
	clientConfig, err := NewClientConfig(opts)
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(strings.TrimSpace(opts.Host), fmt.Sprintf("%d", opts.Port))
	client, err := ssh.Dial("tcp", addr, clientConfig)
	if err != nil {
		return nil, fmt.Errorf("sshclient: dial %s: %w", addr, err)
	}

	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("sshclient: new session: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("sshclient: stdin pipe: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("sshclient: stdout pipe: %w", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("sshclient: stderr pipe: %w", err)
	}

	if usePTY {
		if err := session.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{
			ssh.ECHO:          1,
			ssh.TTY_OP_ISPEED: 14400,
			ssh.TTY_OP_OSPEED: 14400,
		}); err != nil {
			_ = session.Close()
			_ = client.Close()
			return nil, fmt.Errorf("sshclient: request PTY: %w", err)
		}
	}

	remoteCommand := buildRemoteCommand(command, args, usePTY)
	if err := session.Start(remoteCommand); err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("sshclient: start command: %w", err)
	}

	rs := &remoteSession{client: client, session: session, stdin: stdin, done: make(chan struct{})}
	rs.copyWG.Add(2)
	go func() {
		defer rs.copyWG.Done()
		_, _ = io.Copy(out, stdout)
	}()
	go func() {
		defer rs.copyWG.Done()
		_, _ = io.Copy(out, stderr)
	}()

	go func() {
		defer close(rs.done)
		waitErr := session.Wait()
		if waitErr != nil {
			if exitErr, ok := waitErr.(*ssh.ExitError); ok {
				rs.exitCode = exitErr.ExitStatus()
			}
		}
		// Drain remaining streamed output before signalling completion.
		copyDone := make(chan struct{})
		go func() {
			rs.copyWG.Wait()
			close(copyDone)
		}()
		select {
		case <-copyDone:
		case <-time.After(500 * time.Millisecond):
		}
		_ = rs.session.Close()
		_ = rs.client.Close()
	}()
	return rs, nil
}

// buildRemoteCommand renders the command line sent to the remote shell.
func buildRemoteCommand(command string, args []string, usePTY bool) string {
	if strings.TrimSpace(command) == "" {
		if usePTY {
			return "exec $SHELL -l"
		}
		return "cat"
	}
	parts := make([]string, 0, 1+len(args))
	parts = append(parts, command)
	for _, arg := range args {
		parts = append(parts, quoteArg(arg))
	}
	return strings.Join(parts, " ")
}

func quoteArg(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// Wait returns a channel closed when the remote command exits.
func (r *remoteSession) Wait() <-chan struct{} {
	return r.done
}

// ExitCode returns the remote exit status after Wait closes.
func (r *remoteSession) ExitCode() int {
	return r.exitCode
}

// Stdin returns the channel stdin writer.
func (r *remoteSession) Stdin() io.Writer {
	return r.stdin
}

// Resize sends a window-change request for the remote PTY.
func (r *remoteSession) Resize(rows, cols int) error {
	return r.session.WindowChange(rows, cols)
}

// Terminate closes the session and client, ending the remote command.
func (r *remoteSession) Terminate() {
	_ = r.session.Close()
	_ = r.client.Close()
}
