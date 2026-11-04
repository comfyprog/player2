package audio

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

func allZero(p []byte) bool {
	for _, b := range p {
		if b != 0 {
			return false
		}
	}
	return true
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
}

func TestNewRingPanicsOnNonPositiveCapacity(t *testing.T) {
	for _, capacity := range []int{0, -1, -1024} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("NewRing(%d) did not panic", capacity)
				}
			}()
			NewRing(capacity)
		}()
	}
}

func TestRingWrapAround(t *testing.T) {
	r := NewRing(8)

	if n, err := r.Write([]byte("abcdefgh")); n != 8 || err != nil {
		t.Fatalf("Write(full) = (%d, %v), want (8, nil)", n, err)
	}

	buf := make([]byte, 4)
	if n := r.Read(buf); n != 4 || string(buf) != "abcd" {
		t.Fatalf("Read() = (%d, %q), want (4, \"abcd\")", n, buf)
	}

	// The ring is now head=4, size=4. These four bytes wrap around the end.
	if n, err := r.Write([]byte("ijkl")); n != 4 || err != nil {
		t.Fatalf("Write(wrapped) = (%d, %v), want (4, nil)", n, err)
	}

	got := make([]byte, 8)
	if n := r.Read(got); n != 8 || string(got) != "efghijkl" {
		t.Fatalf("Read(wrapped) = (%d, %q), want (8, \"efghijkl\")", n, got)
	}

	if consumed, written := r.Consumed(), r.Written(); consumed != 12 || written != 12 {
		t.Errorf("Consumed/Written = %d/%d, want 12/12", consumed, written)
	}
}

func TestRingChunkedWrapAround(t *testing.T) {
	r := NewRing(7)

	if n, err := r.Write([]byte("abcdefg")); n != 7 || err != nil {
		t.Fatalf("Write() = (%d, %v), want (7, nil)", n, err)
	}

	first := make([]byte, 3)
	if n := r.Read(first); n != 3 || string(first) != "abc" {
		t.Fatalf("Read() = (%d, %q), want (3, \"abc\")", n, first)
	}
	if n, err := r.Write([]byte("hij")); n != 3 || err != nil {
		t.Fatalf("Write() = (%d, %v), want (3, nil)", n, err)
	}

	var got []byte
	for _, size := range []int{2, 2, 3} {
		buf := make([]byte, size)
		n := r.Read(buf)
		got = append(got, buf[:n]...)
	}
	if string(got) != "defghij" {
		t.Errorf("chunked reads = %q, want \"defghij\"", got)
	}
}

func TestRingCapacityOne(t *testing.T) {
	r := NewRing(1)
	for _, b := range []byte("xyz") {
		if n, err := r.Write([]byte{b}); n != 1 || err != nil {
			t.Fatalf("Write(%q) = (%d, %v), want (1, nil)", b, n, err)
		}
		out := make([]byte, 1)
		if n := r.Read(out); n != 1 || out[0] != b {
			t.Fatalf("Read() = (%d, %q), want (1, %q)", n, out, string(b))
		}
	}
}

func TestRingWriteBlocksUntilRead(t *testing.T) {
	r := NewRing(4)
	if _, err := r.Write([]byte("abcd")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := r.Write([]byte("efgh"))
		done <- result{n: n, err: err}
	}()

	select {
	case res := <-done:
		t.Fatalf("Write returned while ring was full: %+v", res)
	case <-time.After(50 * time.Millisecond):
	}

	buf := make([]byte, 4)
	if n := r.Read(buf); n != 4 || string(buf) != "abcd" {
		t.Fatalf("Read() = (%d, %q), want (4, \"abcd\")", n, buf)
	}

	select {
	case res := <-done:
		if res.n != 4 || res.err != nil {
			t.Fatalf("blocked Write = (%d, %v), want (4, nil)", res.n, res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Write did not unblock after Read")
	}

	got := make([]byte, 4)
	if n := r.Read(got); n != 4 || string(got) != "efgh" {
		t.Fatalf("Read() = (%d, %q), want (4, \"efgh\")", n, got)
	}
}

func TestRingAbortUnblocksWrite(t *testing.T) {
	r := NewRing(8)
	if _, err := r.Write([]byte("123456")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := r.Write([]byte("abcdefgh"))
		done <- result{n: n, err: err}
	}()

	// The writer must accept the two free bytes before it can block.
	waitFor(t, "writer to fill the ring", func() bool { return r.Written() == 8 })

	r.Abort()

	select {
	case res := <-done:
		if res.n != 2 || !errors.Is(res.err, ErrAborted) {
			t.Fatalf("aborted Write = (%d, %v), want (2, ErrAborted)", res.n, res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Abort did not unblock writer")
	}

	if n, err := r.Write([]byte("x")); n != 0 || !errors.Is(err, ErrAborted) {
		t.Errorf("Write after Abort = (%d, %v), want (0, ErrAborted)", n, err)
	}

	// Reads still drain buffered audio after Abort.
	buf := make([]byte, 8)
	if n := r.Read(buf); n != 8 || string(buf) != "123456ab" {
		t.Errorf("Read after Abort = (%d, %q), want (8, \"123456ab\")", n, buf)
	}

	r.Reset()
	if n, err := r.Write([]byte("ok")); n != 2 || err != nil {
		t.Errorf("Write after Reset = (%d, %v), want (2, nil)", n, err)
	}
}

func TestRingCloseUnblocksWriteAndIsTerminal(t *testing.T) {
	r := NewRing(2)
	if _, err := r.Write([]byte("ab")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := r.Write([]byte("cd"))
		done <- result{n: n, err: err}
	}()

	waitFor(t, "writer to fill the ring", func() bool { return r.Written() == 2 })
	r.Close()

	select {
	case res := <-done:
		if res.n != 0 || !errors.Is(res.err, ErrClosed) {
			t.Fatalf("Write after Close = (%d, %v), want (0, ErrClosed)", res.n, res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock writer")
	}

	// Reads must remain safe and zero-fill underruns after Close.
	buf := make([]byte, 4)
	if n := r.Read(buf); n != 2 || string(buf[:2]) != "ab" || !allZero(buf[2:]) {
		t.Errorf("Read after Close = (%d, %q), want (2, \"ab\" + zeros)", n, buf)
	}

	if n, err := r.Write([]byte("x")); n != 0 || !errors.Is(err, ErrClosed) {
		t.Errorf("Write after Close = (%d, %v), want (0, ErrClosed)", n, err)
	}

	// Reset clears counters and buffered data but Close remains terminal for
	// writes.
	r.Reset()
	if consumed, written := r.Consumed(), r.Written(); consumed != 0 || written != 0 {
		t.Errorf("Consumed/Written after Reset = %d/%d, want 0/0", consumed, written)
	}
	if n := r.Read(buf); n != 0 || !allZero(buf) {
		t.Errorf("Read after Reset = (%d, %q), want (0, zeros)", n, buf)
	}
	if n, err := r.Write([]byte("x")); n != 0 || !errors.Is(err, ErrClosed) {
		t.Errorf("Write after Reset following Close = (%d, %v), want (0, ErrClosed)", n, err)
	}
}

func TestRingReadUnderrunDoesNotCountSilence(t *testing.T) {
	r := NewRing(8)

	out := bytes.Repeat([]byte{0xff}, 8)
	if n := r.Read(out); n != 0 {
		t.Fatalf("Read(empty) = %d, want 0", n)
	}
	if !allZero(out) {
		t.Errorf("Read(empty) = %q, want zero-filled", out)
	}
	if r.Consumed() != 0 || r.Written() != 0 {
		t.Errorf("Consumed/Written = %d/%d, want 0/0", r.Consumed(), r.Written())
	}

	if _, err := r.Write([]byte{1, 2, 3}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	out = bytes.Repeat([]byte{0xff}, 8)
	if n := r.Read(out); n != 3 {
		t.Fatalf("Read(partial) = %d, want 3", n)
	}
	if !bytes.Equal(out[:3], []byte{1, 2, 3}) {
		t.Errorf("Read(partial) data = %v, want [1 2 3]", out[:3])
	}
	if !allZero(out[3:]) {
		t.Errorf("Read(partial) tail = %v, want zero-filled", out[3:])
	}
	if consumed := r.Consumed(); consumed != 3 {
		t.Errorf("Consumed() = %d, want 3 (silence must not count)", consumed)
	}
	if written := r.Written(); written != 3 {
		t.Errorf("Written() = %d, want 3", written)
	}
}

func TestRingResetClearsCountersAndAbortState(t *testing.T) {
	r := NewRing(8)
	if _, err := r.Write([]byte("abcdef")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	r.Read(make([]byte, 2))
	r.Abort()

	if n, err := r.Write([]byte("x")); n != 0 || !errors.Is(err, ErrAborted) {
		t.Fatalf("Write after Abort = (%d, %v), want (0, ErrAborted)", n, err)
	}

	r.Reset()

	if consumed, written := r.Consumed(), r.Written(); consumed != 0 || written != 0 {
		t.Errorf("Consumed/Written after Reset = %d/%d, want 0/0", consumed, written)
	}
	out := make([]byte, 8)
	if n := r.Read(out); n != 0 || !allZero(out) {
		t.Errorf("Read after Reset = (%d, %q), want (0, zeros)", n, out)
	}
	if n, err := r.Write([]byte("ok")); n != 2 || err != nil {
		t.Errorf("Write after Reset = (%d, %v), want (2, nil)", n, err)
	}
}

func TestRingOversizedWriteCompletesWhileDrained(t *testing.T) {
	r := NewRing(4)
	defer r.Abort()

	payload := []byte("0123456789")
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := r.Write(payload)
		done <- result{n: n, err: err}
	}()

	buf := make([]byte, 3)
	var got []byte
	deadline := time.Now().Add(2 * time.Second)
	for len(got) < len(payload) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out draining oversized write; got %q", got)
		}
		if n := r.Read(buf); n > 0 {
			got = append(got, buf[:n]...)
		}
		runtime.Gosched()
	}

	select {
	case res := <-done:
		if res.n != len(payload) || res.err != nil {
			t.Fatalf("oversized Write = (%d, %v), want (%d, nil)", res.n, res.err, len(payload))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("oversized Write did not complete")
	}

	if !bytes.Equal(got, payload) {
		t.Errorf("drained data = %q, want %q", got, payload)
	}
}

func TestRingZeroLengthOperations(t *testing.T) {
	r := NewRing(4)

	if n, err := r.Write(nil); n != 0 || err != nil {
		t.Errorf("Write(nil) = (%d, %v), want (0, nil)", n, err)
	}
	if n, err := r.Write([]byte{}); n != 0 || err != nil {
		t.Errorf("Write(empty) = (%d, %v), want (0, nil)", n, err)
	}
	if n := r.Read(nil); n != 0 {
		t.Errorf("Read(nil) = %d, want 0", n)
	}
	if n := r.Read([]byte{}); n != 0 {
		t.Errorf("Read(empty) = %d, want 0", n)
	}
	if r.Consumed() != 0 || r.Written() != 0 {
		t.Errorf("Consumed/Written = %d/%d, want 0/0", r.Consumed(), r.Written())
	}

	// Zero-length writes are no-ops even after Close.
	r.Close()
	if n, err := r.Write(nil); n != 0 || err != nil {
		t.Errorf("Write(nil) after Close = (%d, %v), want (0, nil)", n, err)
	}
}

func TestRingConcurrentWriterReader(t *testing.T) {
	const total = 256 << 10

	r := NewRing(4096)
	defer r.Abort()

	payload := make([]byte, total)
	for i := range payload {
		payload[i] = byte(i*7 + 3)
	}

	errs := make(chan error, 2)
	go func() {
		for off := 0; off < total; {
			n := 1 + off%997
			if off+n > total {
				n = total - off
			}
			written, err := r.Write(payload[off : off+n])
			off += written
			if err != nil {
				errs <- fmt.Errorf("writer: %w", err)
				return
			}
		}
		errs <- nil
	}()

	go func() {
		got := make([]byte, 0, total)
		buf := make([]byte, 777)
		for len(got) < total {
			n := r.Read(buf)
			if n == 0 {
				runtime.Gosched()
				continue
			}
			got = append(got, buf[:n]...)
		}
		if !bytes.Equal(got, payload) {
			errs <- fmt.Errorf("reader: data mismatch at %d bytes", len(got))
			return
		}
		errs <- nil
	}()

	// Concurrent counter reads must be race-free.
	stop := make(chan struct{})
	var poll sync.WaitGroup
	poll.Add(1)
	go func() {
		defer poll.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = r.Consumed()
				_ = r.Written()
				runtime.Gosched()
			}
		}
	}()

	for i := 0; i < 2; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent writer/reader timed out")
		}
	}
	close(stop)
	poll.Wait()

	if consumed, written := r.Consumed(), r.Written(); consumed != total || written != total {
		t.Errorf("Consumed/Written = %d/%d, want %d/%d", consumed, written, total, total)
	}
}
