package session

import (
	"io"
	"time"

	"ezterm/internal/files"
	"ezterm/internal/sshclient"
)

// remoteProc adapts an sshclient.RemoteSession to the local proc interface.
type remoteProc struct {
	rs sshclient.RemoteSession
}

func (r *remoteProc) Stdin() io.Writer {
	return r.rs.Stdin()
}

func (r *remoteProc) Done() <-chan struct{} {
	return r.rs.Wait()
}

func (r *remoteProc) ExitCode() int {
	return r.rs.ExitCode()
}

func (r *remoteProc) Terminate(grace time.Duration, force bool) {
	r.rs.Terminate()
}

func (r *remoteProc) Close() error {
	r.rs.Terminate()
	return nil
}

func (r *remoteProc) Resize(rows, cols int) error {
	return r.rs.Resize(rows, cols)
}

func (r *remoteProc) PID() int {
	return 0 // remote PID is not exposed
}

// SFTP exposes the session's file transfer backend.
func (r *remoteProc) SFTP() (files.Backend, error) {
	return r.rs.SFTP()
}
