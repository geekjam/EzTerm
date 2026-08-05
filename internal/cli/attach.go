package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

// detachKey is the key sequence that detaches the client from the session
// (Ctrl+] = 0x1D) while leaving the session running. The byte is consumed and
// never forwarded to the session.
const detachKey = 0x1D

var errDetached = errors.New("attach: detached")

// runAttach enters an interactive PTY session: raw-mode terminal, live byte
// streaming in both directions, window-resize propagation, and Ctrl+] detach.
// It returns a process exit code.
func runAttach(c *client, id string) int {
	// Resolve the session first so error precedence is deterministic: a
	// missing session reports exit code 1 even when stdin is not a terminal.
	if _, err := c.getSession(id); err != nil {
		printError(false, "attach: %v", err)
		return exitCodeFor(err)
	}

	// Match the PTY to the local terminal before entering raw mode so the
	// first screen is rendered at the correct size.
	if rows, cols, err := terminalSize(); err == nil && rows > 0 && cols > 0 {
		_ = c.resize(id, rows, cols)
	}

	ts, err := makeRaw()
	if err != nil {
		printError(false, "attach: %v", err)
		return 2
	}
	defer ts.Restore()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := c.attachStream(ctx, id)
	if err != nil {
		printError(false, "attach: %v", err)
		return exitCodeFor(err)
	}
	defer stream.Close()

	stopResize := watchResize(func() {
		if rows, cols, err := terminalSize(); err == nil && rows > 0 && cols > 0 {
			_ = c.resize(id, rows, cols)
		}
	})
	defer stopResize()

	outputDone := make(chan error, 1)
	go func() {
		// Copy raw PTY bytes straight to the terminal; EOF arrives when the
		// session exits and the daemon closes the stream.
		_, copyErr := io.Copy(os.Stdout, stream)
		outputDone <- copyErr
	}()

	inputDone := make(chan error, 1)
	go func() {
		inputDone <- pumpAttachInput(os.Stdin, func(data []byte) error {
			return c.sendRawInput(id, data)
		})
	}()

	select {
	case err := <-outputDone:
		// The session output stream ended (session exited or connection
		// broke); restore the terminal and exit cleanly.
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
			printError(false, "attach: %v", err)
			return 2
		}
		return 0
	case err := <-inputDone:
		if errors.Is(err, errDetached) {
			_ = ts.Restore()
			fmt.Fprintf(os.Stderr, "\r\ndetached from session %s\n", id)
			return 0
		}
		if err != nil {
			printError(false, "attach: %v", err)
			return 2
		}
		return 0
	}
}

// pumpAttachInput reads raw bytes from input and forwards them to the session
// via send. Ctrl+] (detachKey) detaches without forwarding the byte; bytes
// read before it in the same chunk are still forwarded. It returns
// errDetached on detach, send errors unchanged, and nil on input EOF.
func pumpAttachInput(input io.Reader, send func([]byte) error) error {
	buf := make([]byte, 4096)
	for {
		n, err := input.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if idx := bytes.IndexByte(chunk, detachKey); idx >= 0 {
				if idx > 0 {
					if sendErr := send(chunk[:idx]); sendErr != nil {
						return sendErr
					}
				}
				return errDetached
			}
			if sendErr := send(chunk); sendErr != nil {
				return sendErr
			}
		}
		if err != nil {
			return nil
		}
	}
}
