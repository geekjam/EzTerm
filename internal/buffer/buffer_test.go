package buffer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestWriteAndRead(t *testing.T) {
	b := New()
	rid, err := b.NewReaderFromStart()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := b.Write([]byte(" world")); err != nil {
		t.Fatal(err)
	}
	data, err := b.Read(context.Background(), rid, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "hello world"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNewReaderOnlySeesNewOutput(t *testing.T) {
	b := New()
	if err := b.Write([]byte("before")); err != nil {
		t.Fatal(err)
	}
	rid, _ := b.NewReader() // starts from the end
	if err := b.Write([]byte("after")); err != nil {
		t.Fatal(err)
	}
	data, _ := b.Read(context.Background(), rid, 0, 0)
	if got, want := string(data), "after"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestReadTimeoutReturnsEmpty(t *testing.T) {
	b := New()
	rid, _ := b.NewReader()
	start := time.Now()
	data, err := b.Read(context.Background(), rid, 50*time.Millisecond, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("expected no data, got %q", data)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("read returned too early: %v", elapsed)
	}
}

// TestConsecutiveTimeoutsOnIdleBuffer guards against the lost-wakeup that
// made a timed read on an otherwise-idle buffer wait past its deadline (a
// subsequent "second read" that never returns). Every read must return within
// timeout+margin even when no writes or other readers ever wake it.
func TestConsecutiveTimeoutsOnIdleBuffer(t *testing.T) {
	b := New()
	rid, _ := b.NewReader()
	const timeout = 15 * time.Millisecond
	const margin = 50 * time.Millisecond
	for i := 0; i < 250; i++ {
		start := time.Now()
		data, err := b.Read(context.Background(), rid, timeout, 0)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("read %d error: %v", i, err)
		}
		if len(data) != 0 {
			t.Fatalf("read %d unexpectedly returned %q", i, data)
		}
		if elapsed > timeout+margin {
			t.Fatalf("read %d exceeded timeout: elapsed=%v timeout=%v", i, elapsed, timeout)
		}
	}
}

func TestReadBlocksUntilData(t *testing.T) {
	b := New()
	rid, _ := b.NewReader()
	go func() {
		time.Sleep(60 * time.Millisecond)
		_ = b.Write([]byte("arrived"))
	}()
	start := time.Now()
	data, err := b.Read(context.Background(), rid, 2*time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "arrived" {
		t.Fatalf("got %q", got)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("returned before writer: %v", elapsed)
	}
}

func TestEOFAfterClose(t *testing.T) {
	b := New()
	rid, _ := b.NewReader()
	_ = b.Write([]byte("data"))
	b.Close()
	data, err := b.Read(context.Background(), rid, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "data" {
		t.Fatalf("got %q", data)
	}
	_, err = b.Read(context.Background(), rid, 0, 0)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestWriteAfterClose(t *testing.T) {
	b := New()
	b.Close()
	if err := b.Write([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

func TestInvalidReader(t *testing.T) {
	b := New()
	_, err := b.Read(context.Background(), 999, 0, 0)
	if !errors.Is(err, ErrReader) {
		t.Fatalf("expected ErrReader, got %v", err)
	}
}

func TestUnregister(t *testing.T) {
	b := New()
	rid, _ := b.NewReader()
	b.Unregister(rid)
	_, err := b.Read(context.Background(), rid, 0, 0)
	if !errors.Is(err, ErrReader) {
		t.Fatalf("expected ErrReader after unregister, got %v", err)
	}
}

func TestNewReaderFromStartAfterClose(t *testing.T) {
	b := New()
	if err := b.Write([]byte("final screen")); err != nil {
		t.Fatal(err)
	}
	b.Close()
	// Attach replay needs to register a reader on an ended session's buffer.
	rid, err := b.NewReaderFromStart()
	if err != nil {
		t.Fatalf("NewReaderFromStart on closed buffer: %v", err)
	}
	data, err := b.Read(context.Background(), rid, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "final screen" {
		t.Fatalf("replay after close got %q, want %q", data, "final screen")
	}
	if _, err := b.Read(context.Background(), rid, 0, 0); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after draining closed buffer, got %v", err)
	}
}

func TestReaderCount(t *testing.T) {
	b := New()
	if got := b.ReaderCount(); got != 0 {
		t.Fatalf("initial count = %d, want 0", got)
	}
	r1, _ := b.NewReader()
	r2, _ := b.NewReaderFromStart()
	if got := b.ReaderCount(); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
	b.Unregister(r1)
	if got := b.ReaderCount(); got != 1 {
		t.Fatalf("count after unregister = %d, want 1", got)
	}
	b.Unregister(r2)
	if got := b.ReaderCount(); got != 0 {
		t.Fatalf("count after all unregistered = %d, want 0", got)
	}
}

func TestCompactionDropsConsumedPrefix(t *testing.T) {
	b := New()
	// Force aggressive compaction so the retained history stays small.
	b.compactThreshold = 16
	b.compactMinAdvance = 1

	r1, _ := b.NewReaderFromStart()
	r2, _ := b.NewReaderFromStart()

	chunk := bytes.Repeat([]byte{'a'}, 4)
	for i := 0; i < 200; i++ {
		_ = b.Write(chunk)
	}
	// Both readers consume everything so the whole prefix is reclaimable.
	for _, id := range []int{r1, r2} {
		if _, err := b.Read(context.Background(), id, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	_ = b.Write(chunk) // triggers compaction (min reader position >= minAdvance)

	// A reader starting from the beginning must see only the retained tail.
	nr, _ := b.NewReaderFromStart()
	data, err := b.Read(context.Background(), nr, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(chunk) {
		t.Fatalf("after compaction got %q, want %q", data, chunk)
	}
}
