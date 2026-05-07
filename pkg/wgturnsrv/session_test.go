// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturnsrv

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
)

// memConn is a deterministic in-memory net.Conn pair built around two
// channels. It mirrors the parts of net.Pipe() the demuxer relies on
// (Read/Write/Close + deadlines) without the gotcha that a closed
// net.Pipe Write returns io.ErrClosedPipe rather than net.ErrClosed.
type memConn struct {
	in       chan []byte
	out      chan []byte
	closed   atomic.Bool
	closeCh  chan struct{}
	readDdl  atomic.Pointer[time.Time]
	writeDdl atomic.Pointer[time.Time]
}

func newMemConnPair() (*memConn, *memConn) {
	a := &memConn{in: make(chan []byte, 16), out: make(chan []byte, 16), closeCh: make(chan struct{})}
	b := &memConn{in: a.out, out: a.in, closeCh: make(chan struct{})}
	return a, b
}

func (c *memConn) Read(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	var timer *time.Timer
	var deadlineCh <-chan time.Time
	if d := c.readDdl.Load(); d != nil && !d.IsZero() {
		timer = time.NewTimer(time.Until(*d))
		defer timer.Stop()
		deadlineCh = timer.C
	}
	select {
	case b := <-c.in:
		n := copy(p, b)
		return n, nil
	case <-c.closeCh:
		return 0, net.ErrClosed
	case <-deadlineCh:
		return 0, &timeoutErr{}
	}
}

func (c *memConn) Write(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	cp := append([]byte(nil), p...)
	select {
	case c.out <- cp:
		return len(p), nil
	case <-c.closeCh:
		return 0, net.ErrClosed
	}
}

func (c *memConn) Close() error {
	if c.closed.CompareAndSwap(false, true) {
		close(c.closeCh)
	}
	return nil
}

func (c *memConn) LocalAddr() net.Addr  { return memAddr{} }
func (c *memConn) RemoteAddr() net.Addr { return memAddr{} }
func (c *memConn) SetDeadline(t time.Time) error {
	c.readDdl.Store(&t)
	c.writeDdl.Store(&t)
	return nil
}
func (c *memConn) SetReadDeadline(t time.Time) error  { c.readDdl.Store(&t); return nil }
func (c *memConn) SetWriteDeadline(t time.Time) error { c.writeDdl.Store(&t); return nil }

type memAddr struct{}

func (memAddr) Network() string { return "mem" }
func (memAddr) String() string  { return "mem" }

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "deadline exceeded" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// TestSession_AddStream_EvictsDuplicate registers two conns under the
// same stream id and verifies the first one is returned as displaced
// while the second occupies the slot. removeStream from the loser
// must be a no-op; the winner is still in place afterwards.
func TestSession_AddStream_EvictsDuplicate(t *testing.T) {
	sess := newSession(context.Background(), "abcd", &nopConn{}, wgturn.NoopLogger{}, Config{}.withDefaults(), nil)

	a, b := &nopConn{}, &nopConn{}

	displaced, ok := sess.addStream(7, a)
	if !ok || displaced != nil {
		t.Fatalf("first add: ok=%v displaced=%v", ok, displaced)
	}
	if got := sess.streamCount(); got != 1 {
		t.Errorf("streamCount = %d, want 1", got)
	}

	displaced, ok = sess.addStream(7, b)
	if !ok {
		t.Fatal("second add: ok=false, want eviction-on-conflict")
	}
	if displaced != net.Conn(a) {
		t.Errorf("displaced = %v, want a", displaced)
	}
	if got := sess.streamCount(); got != 1 {
		t.Errorf("streamCount after eviction = %d, want 1", got)
	}

	// The loser tries to remove itself: must be a no-op so the winner
	// (b) stays in place.
	drained := sess.removeStream(7, a)
	if drained {
		t.Error("loser removeStream reported drained, but winner b is still here")
	}
	if got := sess.streamCount(); got != 1 {
		t.Errorf("streamCount after loser-remove = %d, want 1", got)
	}

	// Now remove the winner: slice should drain.
	drained = sess.removeStream(7, b)
	if !drained {
		t.Error("winner removeStream drained = false, want true")
	}
	if got := sess.streamCount(); got != 0 {
		t.Errorf("streamCount after drain = %d, want 0", got)
	}
}

// TestSession_RoundRobin_Backend exercises runBackend directly: feeding
// 6 packets into a session with 3 streams should land 2 on each stream
// in strict round-robin order (rrCounter is atomic and monotonic).
func TestSession_RoundRobin_Backend(t *testing.T) {
	backend, peer := newMemConnPair()
	cfg := Config{
		StreamReadTimeout:   2 * time.Second,
		BackendWriteTimeout: 1 * time.Second,
	}.withDefaults()
	sess := newSession(context.Background(), "rr", backend, wgturn.NoopLogger{}, cfg, nil)

	streamConns := make([]*recordingConn, 3)
	for i := range streamConns {
		streamConns[i] = &recordingConn{}
		if _, ok := sess.addStream(byte(i), streamConns[i]); !ok {
			t.Fatalf("addStream %d: not accepted", i)
		}
	}

	done := make(chan struct{})
	go func() {
		sess.runBackend()
		close(done)
	}()

	for i := 0; i < 6; i++ {
		if _, err := peer.Write([]byte{byte(i)}); err != nil {
			t.Fatalf("peer write %d: %v", i, err)
		}
	}

	// Wait for all 6 packets to land. recordingConn counts writes.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		total := streamConns[0].count() + streamConns[1].count() + streamConns[2].count()
		if total >= 6 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	for i, sc := range streamConns {
		if got := sc.count(); got != 2 {
			t.Errorf("stream %d received %d packets, want 2", i, got)
		}
	}

	// rrCounter advanced exactly 6 times.
	if got := sess.rrCounter.Load(); got != 6 {
		t.Errorf("rrCounter = %d, want 6", got)
	}

	// Tear down: close peer side so backend.Read returns net.ErrClosed.
	_ = peer.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runBackend didn't exit after backend close")
	}
}

// TestSession_Terminate_Idempotent calls terminate() multiple times
// concurrently. Only the first call should invoke onTerminate, and all
// callers must return promptly.
func TestSession_Terminate_Idempotent(t *testing.T) {
	backend, _ := newMemConnPair()
	var fired atomic.Int32
	sess := newSession(
		context.Background(), "term", backend,
		wgturn.NoopLogger{}, Config{}.withDefaults(),
		func() { fired.Add(1) },
	)
	const N = 8
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() { defer wg.Done(); sess.terminate() }()
	}
	wg.Wait()
	if got := fired.Load(); got != 1 {
		t.Errorf("onTerminate fired %d times, want exactly 1", got)
	}
}

// TestSession_RunStream_BackendWriteFailureTerminates: when the
// backend write errors (because it's been closed externally), the
// stream reader should call terminate() so peer streams don't keep
// shovelling bytes into a dead fd.
func TestSession_RunStream_BackendWriteFailureTerminates(t *testing.T) {
	backend, _ := newMemConnPair()
	_ = backend.Close()

	streamA, peerA := newMemConnPair()
	var fired atomic.Int32
	sess := newSession(
		context.Background(), "bad-backend", backend,
		wgturn.NoopLogger{},
		Config{StreamReadTimeout: time.Second}.withDefaults(),
		func() { fired.Add(1) },
	)
	if _, ok := sess.addStream(0, streamA); !ok {
		t.Fatal("addStream rejected")
	}

	// Push one packet from peer-side; runStream will read it, try to
	// write to closed backend, fail, and terminate.
	go func() {
		_, _ = peerA.Write([]byte("ping"))
	}()

	done := make(chan struct{})
	go func() {
		sess.runStream(0, streamA)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runStream didn't exit after backend write failure")
	}
	if got := fired.Load(); got != 1 {
		t.Errorf("onTerminate fired %d times, want 1", got)
	}
}

// TestSession_RunBackend_NoStreamsDropsPacket — packets that arrive
// from the WG side before any stream is registered have nowhere to
// go and are silently dropped (matches legacy behaviour).
func TestSession_RunBackend_NoStreamsDropsPacket(t *testing.T) {
	backend, peer := newMemConnPair()
	sess := newSession(
		context.Background(), "no-streams", backend,
		wgturn.NoopLogger{},
		Config{StreamReadTimeout: 500 * time.Millisecond}.withDefaults(),
		nil,
	)

	done := make(chan struct{})
	go func() {
		sess.runBackend()
		close(done)
	}()

	// Push a packet — no streams to receive it.
	if _, err := peer.Write([]byte("orphan")); err != nil {
		t.Fatalf("peer write: %v", err)
	}

	// Add a stream after a short delay; subsequent packets should land.
	time.Sleep(50 * time.Millisecond)
	streamConn := &recordingConn{}
	if _, ok := sess.addStream(0, streamConn); !ok {
		t.Fatal("addStream rejected")
	}
	if _, err := peer.Write([]byte("delivered")); err != nil {
		t.Fatalf("peer write 2: %v", err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && streamConn.count() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := streamConn.count(); got != 1 {
		t.Errorf("stream received %d packets, want 1 (orphan should have been dropped)", got)
	}
	if !bytes.Equal(streamConn.lastPayload(), []byte("delivered")) {
		t.Errorf("stream got %q, want %q", streamConn.lastPayload(), "delivered")
	}

	_ = peer.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runBackend didn't exit")
	}
}

// nopConn is a net.Conn that succeeds at everything and reads / writes
// nothing. Used as a placeholder when only the slot-occupancy
// behaviour matters.
type nopConn struct {
	closed atomic.Bool
}

func (c *nopConn) Read(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	return 0, io.EOF
}
func (c *nopConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *nopConn) Close() error                     { c.closed.Store(true); return nil }
func (c *nopConn) LocalAddr() net.Addr              { return memAddr{} }
func (c *nopConn) RemoteAddr() net.Addr             { return memAddr{} }
func (c *nopConn) SetDeadline(time.Time) error      { return nil }
func (c *nopConn) SetReadDeadline(time.Time) error  { return nil }
func (c *nopConn) SetWriteDeadline(time.Time) error { return nil }

// recordingConn keeps every payload it receives so tests can assert
// distribution properties.
type recordingConn struct {
	mu       sync.Mutex
	payloads [][]byte
	closed   atomic.Bool
}

func (c *recordingConn) Read(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	return 0, io.EOF
}
func (c *recordingConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.payloads = append(c.payloads, append([]byte(nil), p...))
	return len(p), nil
}
func (c *recordingConn) Close() error                     { c.closed.Store(true); return nil }
func (c *recordingConn) LocalAddr() net.Addr              { return memAddr{} }
func (c *recordingConn) RemoteAddr() net.Addr             { return memAddr{} }
func (c *recordingConn) SetDeadline(time.Time) error      { return nil }
func (c *recordingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *recordingConn) SetWriteDeadline(time.Time) error { return nil }

func (c *recordingConn) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.payloads)
}
func (c *recordingConn) lastPayload() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.payloads) == 0 {
		return nil
	}
	return c.payloads[len(c.payloads)-1]
}
