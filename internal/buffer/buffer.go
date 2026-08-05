package buffer

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

var (
	// ErrClosed indicates that no more output can be appended.
	ErrClosed = errors.New("buffer: closed")
	// ErrReader indicates that a reader ID is not registered.
	ErrReader = errors.New("buffer: invalid reader ID")
)

type readerState struct {
	readPos int64
}

// Buffer is a thread-safe append-only output log with independent reader cursors.
type Buffer struct {
	mu                sync.Mutex
	master            []byte
	readers           map[int]*readerState
	nextID            int
	closed            bool
	notify            chan struct{}
	compactThreshold  int
	compactMinAdvance int
}

// New creates an empty output buffer.
func New() *Buffer {
	b := &Buffer{
		readers:           make(map[int]*readerState),
		notify:            make(chan struct{}),
		compactThreshold:  8 << 20,
		compactMinAdvance: 1 << 20,
	}
	return b
}

// NewReader registers a reader at the current end of the retained output.
func (b *Buffer) NewReader() (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, ErrClosed
	}
	return b.newReaderLocked(int64(len(b.master))), nil
}

// NewReaderFromStart registers a reader at the beginning of retained output.
// Unlike NewReader, registration is allowed on a closed buffer so attach
// clients can replay the final screen state: the reader drains any retained
// data and then reports io.EOF.
func (b *Buffer) NewReaderFromStart() (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.newReaderLocked(0), nil
}

func (b *Buffer) newReaderLocked(position int64) int {
	id := b.nextID
	b.nextID++
	b.readers[id] = &readerState{readPos: position}
	return id
}

// Unregister removes a reader and may allow old output to be compacted.
func (b *Buffer) Unregister(id int) {
	b.mu.Lock()
	delete(b.readers, id)
	b.maybeCompactLocked()
	b.notifyLocked()
	b.mu.Unlock()
}

// ReaderCount reports how many readers are currently registered; useful for
// observing that detached attach clients release their readers.
func (b *Buffer) ReaderCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.readers)
}

// Write appends output and wakes readers waiting for data.
func (b *Buffer) Write(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrClosed
	}
	b.master = append(b.master, data...)
	b.maybeCompactLocked()
	b.notifyLocked()
	return nil
}

// notifyLocked wakes readers waiting for a state change. The channel is
// replaced so readers that arrive after this notification wait on a fresh
// generation instead of consuming a one-shot signal.
func (b *Buffer) notifyLocked() {
	close(b.notify)
	b.notify = make(chan struct{})
}

func (b *Buffer) maybeCompactLocked() {
	if len(b.master) <= b.compactThreshold {
		return
	}
	minPos := int64(len(b.master))
	for _, reader := range b.readers {
		if reader.readPos < minPos {
			minPos = reader.readPos
		}
	}
	if minPos < int64(b.compactMinAdvance) {
		return
	}
	b.master = append([]byte(nil), b.master[minPos:]...)
	for _, reader := range b.readers {
		reader.readPos -= minPos
	}
}

// Read waits for output up to timeout and advances the selected reader.
// A zero or negative timeout performs a non-blocking read. maxBytes limits the
// returned chunk; zero means all currently available output.
func (b *Buffer) Read(ctx context.Context, readerID int, timeout time.Duration, maxBytes int) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	b.mu.Lock()
	reader, ok := b.readers[readerID]
	if !ok {
		b.mu.Unlock()
		return nil, ErrReader
	}
	if data := b.drainLocked(reader, maxBytes); data != nil {
		b.mu.Unlock()
		return data, nil
	}
	if b.closed {
		b.mu.Unlock()
		return nil, io.EOF
	}
	if timeout <= 0 {
		b.mu.Unlock()
		return nil, nil
	}

	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			b.mu.Unlock()
			return nil, err
		}
		if data := b.drainLocked(reader, maxBytes); data != nil {
			b.mu.Unlock()
			return data, nil
		}
		if b.closed {
			b.mu.Unlock()
			return nil, io.EOF
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			b.mu.Unlock()
			return nil, nil
		}

		// Capture the notification generation while locked. A notification that
		// happens before the select is still observed because the channel closes.
		notify := b.notify
		b.mu.Unlock()

		timer := time.NewTimer(remaining)
		select {
		case <-notify:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}

		b.mu.Lock()
		var stillRegistered bool
		reader, stillRegistered = b.readers[readerID]
		if !stillRegistered {
			b.mu.Unlock()
			return nil, ErrReader
		}
	}
}

func (b *Buffer) drainLocked(reader *readerState, maxBytes int) []byte {
	end := int64(len(b.master))
	if reader.readPos >= end {
		return nil
	}
	endPos := end
	if maxBytes > 0 && reader.readPos+int64(maxBytes) < endPos {
		endPos = reader.readPos + int64(maxBytes)
	}
	data := append([]byte(nil), b.master[reader.readPos:endPos]...)
	reader.readPos = endPos
	b.maybeCompactLocked()
	return data
}

// HasMore reports whether a reader has unread output.
func (b *Buffer) HasMore(readerID int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	reader, ok := b.readers[readerID]
	return ok && reader.readPos < int64(len(b.master))
}

// Len returns the number of retained bytes.
func (b *Buffer) Len() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return int64(len(b.master))
}

// Close prevents future writes while allowing readers to drain retained data.
func (b *Buffer) Close() {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		b.notifyLocked()
	}
	b.mu.Unlock()
}

// IsClosed reports whether the buffer has been closed.
func (b *Buffer) IsClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}
