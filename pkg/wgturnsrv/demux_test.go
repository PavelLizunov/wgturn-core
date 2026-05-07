// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturnsrv_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pion/dtls/v3"

	"github.com/PavelLizunov/wgturn-core/internal/framing"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturnsrv"
)

// stubBackend is a Backend whose Open returns the server-half of a
// net.Pipe pair; the test holds the client-half so it can play the
// role of "wg0" for the duration. One pair per session id, recorded
// so tests can fish them back out.
type stubBackend struct {
	mu    sync.Mutex
	peers map[string]net.Conn
	opens atomic.Int32
}

func newStubBackend() *stubBackend {
	return &stubBackend{peers: make(map[string]net.Conn)}
}

func (b *stubBackend) Open(_ context.Context, sessionID string) (net.Conn, error) {
	server, client := net.Pipe()
	b.mu.Lock()
	b.peers[sessionID] = client
	b.mu.Unlock()
	b.opens.Add(1)
	return server, nil
}

func (b *stubBackend) peer(sessionID string) net.Conn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.peers[sessionID]
}

// TestServer_Demux_StreamRoundTrip is the headline S3 test: a real
// DTLS client dials the server, writes the 17-byte preamble, sends a
// payload, and the test (impersonating the WG side) reads it through
// the backend pipe and writes back. The reply must arrive on the
// client's DTLS conn.
func TestServer_Demux_StreamRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	backend := newStubBackend()
	srv, err := wgturnsrv.New(wgturnsrv.Config{
		ListenAddr:        "127.0.0.1:0",
		Backend:           backend,
		Logger:            &tLogger{t: t},
		StreamReadTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	sessionID, _ := uuid.New().MarshalBinary()
	client := dialClient(t, srv.LocalAddr().(*net.UDPAddr))
	t.Cleanup(func() { _ = client.Close() })

	if err := framing.WriteHandshake(client, sessionID, 0); err != nil {
		t.Fatalf("WriteHandshake: %v", err)
	}

	// Wait for the server to register the session and open the
	// backend. The Open count goes to 1 once getOrCreateSession runs.
	waitFor(t, time.Second, func() bool { return backend.opens.Load() >= 1 })

	// Send a payload from client → backend.
	clientToServer := []byte("hello-server")
	if _, err := client.Write(clientToServer); err != nil {
		t.Fatalf("client write: %v", err)
	}

	// Read it on the backend's peer side.
	sidHex := hexEncode(sessionID)
	peer := backend.peer(sidHex)
	if peer == nil {
		t.Fatalf("backend has no peer for session %s", sidHex)
	}
	_ = peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1500)
	n, err := peer.Read(buf)
	if err != nil {
		t.Fatalf("backend peer read: %v", err)
	}
	if string(buf[:n]) != string(clientToServer) {
		t.Errorf("backend got %q, want %q", buf[:n], clientToServer)
	}

	// Now the reverse path: backend → client.
	serverToClient := []byte("hello-client")
	if _, err := peer.Write(serverToClient); err != nil {
		t.Fatalf("backend peer write: %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err = client.Read(buf)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(buf[:n]) != string(serverToClient) {
		t.Errorf("client got %q, want %q", buf[:n], serverToClient)
	}

	// Stats reflects the live session and stream.
	stats, err := srv.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.SessionsActive != 1 || stats.StreamsActive != 1 {
		t.Errorf("Stats = %+v, want 1/1", stats)
	}
}

// TestServer_Demux_MultipleStreamsRoundRobin opens two streams under
// the same session ID and drives backend → client traffic; round-robin
// dispatch should split packets evenly across them.
func TestServer_Demux_MultipleStreamsRoundRobin(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	backend := newStubBackend()
	srv, err := wgturnsrv.New(wgturnsrv.Config{
		ListenAddr:        "127.0.0.1:0",
		Backend:           backend,
		Logger:            &tLogger{t: t},
		StreamReadTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	sessionID, _ := uuid.New().MarshalBinary()

	// Open two DTLS streams sharing the session id.
	c0 := dialClient(t, srv.LocalAddr().(*net.UDPAddr))
	t.Cleanup(func() { _ = c0.Close() })
	if err := framing.WriteHandshake(c0, sessionID, 0); err != nil {
		t.Fatalf("c0 handshake: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		st, _ := srv.Stats()
		return st.StreamsActive >= 1
	})

	c1 := dialClient(t, srv.LocalAddr().(*net.UDPAddr))
	t.Cleanup(func() { _ = c1.Close() })
	if err := framing.WriteHandshake(c1, sessionID, 1); err != nil {
		t.Fatalf("c1 handshake: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		st, _ := srv.Stats()
		return st.SessionsActive == 1 && st.StreamsActive == 2
	})

	// Drive 6 packets from the backend; each stream should get 3.
	sidHex := hexEncode(sessionID)
	peer := backend.peer(sidHex)
	if peer == nil {
		t.Fatalf("no peer for %s", sidHex)
	}
	for i := 0; i < 6; i++ {
		if _, err := peer.Write([]byte{byte(i)}); err != nil {
			t.Fatalf("peer write %d: %v", i, err)
		}
	}

	c0Got, c1Got := drainCount(t, c0, 3, 5*time.Second), drainCount(t, c1, 3, 5*time.Second)
	if c0Got != 3 || c1Got != 3 {
		t.Errorf("distribution = c0:%d c1:%d, want 3/3", c0Got, c1Got)
	}
}

// TestServer_Demux_EvictionOnDuplicateStreamID re-uses an in-flight
// (sessionID, streamID) pair: the second handshake should evict the
// first conn (closing it) and the new conn takes the slot. Stats
// stays at one stream.
func TestServer_Demux_EvictionOnDuplicateStreamID(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	backend := newStubBackend()
	srv, err := wgturnsrv.New(wgturnsrv.Config{
		ListenAddr: "127.0.0.1:0",
		Backend:    backend,
		Logger:     &tLogger{t: t},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	sessionID, _ := uuid.New().MarshalBinary()

	c0 := dialClient(t, srv.LocalAddr().(*net.UDPAddr))
	t.Cleanup(func() { _ = c0.Close() })
	if err := framing.WriteHandshake(c0, sessionID, 7); err != nil {
		t.Fatalf("c0 handshake: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		st, _ := srv.Stats()
		return st.StreamsActive == 1
	})

	c1 := dialClient(t, srv.LocalAddr().(*net.UDPAddr))
	t.Cleanup(func() { _ = c1.Close() })
	if err := framing.WriteHandshake(c1, sessionID, 7); err != nil {
		t.Fatalf("c1 handshake: %v", err)
	}

	// c0 should be closed by the server; reading from it should
	// surface an error within a moment.
	_ = c0.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c0.Read(make([]byte, 16)); err == nil {
		t.Error("c0 read after eviction returned no error; expected close")
	}

	// Stats: still one session, still one stream (c1 in c0's old
	// slot).
	waitFor(t, 2*time.Second, func() bool {
		st, _ := srv.Stats()
		return st.SessionsActive == 1 && st.StreamsActive == 1
	})
	st, _ := srv.Stats()
	if st.SessionsActive != 1 || st.StreamsActive != 1 {
		t.Errorf("post-eviction Stats = %+v, want 1/1", st)
	}
}

// TestServer_Demux_HandshakeTimeout: a client that DTLS-handshakes but
// never sends the 17-byte preamble has its conn closed once
// HandshakeTimeout fires. No session is ever registered.
func TestServer_Demux_HandshakeTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	backend := newStubBackend()
	srv, err := wgturnsrv.New(wgturnsrv.Config{
		ListenAddr:       "127.0.0.1:0",
		Backend:          backend,
		Logger:           &tLogger{t: t},
		HandshakeTimeout: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	c := dialClient(t, srv.LocalAddr().(*net.UDPAddr))
	t.Cleanup(func() { _ = c.Close() })

	// Don't send the handshake. After ~300ms the server tears the
	// conn down; reading on the client side sees ErrClosed/EOF.
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 16)); err == nil {
		t.Error("client read after server timeout returned no error")
	}

	// Backend must never have been opened — there was no valid
	// session id to map.
	if got := backend.opens.Load(); got != 0 {
		t.Errorf("backend opens = %d, want 0", got)
	}
}

// dialClient runs a real DTLS client handshake against laddr and
// returns the conn. Failures fail the test immediately.
func dialClient(t *testing.T, laddr *net.UDPAddr) net.Conn {
	t.Helper()
	cert, err := framing.GenerateCertificate()
	if err != nil {
		t.Fatalf("client cert: %v", err)
	}
	cfg := framing.NewDTLSConfig(framing.RoleClient, cert)
	conn, err := dtls.Dial("udp", laddr, cfg)
	if err != nil {
		t.Fatalf("dtls dial %s: %v", laddr, err)
	}
	return conn
}

// drainCount reads up to expected packets from c within the deadline,
// returning how many were observed.
func drainCount(t *testing.T, c net.Conn, expected int, within time.Duration) int {
	t.Helper()
	buf := make([]byte, 1500)
	deadline := time.Now().Add(within)
	got := 0
	for got < expected && time.Now().Before(deadline) {
		_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		_, err := c.Read(buf)
		if err == nil {
			got++
			continue
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			continue
		}
		// Other errors: bail out, return what we have.
		return got
	}
	return got
}

// waitFor polls cond every 5ms until it returns true or the deadline
// passes; reports a fatal failure if it never becomes true.
func waitFor(t *testing.T, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not reached within %s", within)
}

// hexEncode mirrors the server's hex.EncodeToString call so tests can
// build the same map key without taking a dep on the server's
// internals.
func hexEncode(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0xf]
	}
	return string(out)
}

// tLogger forwards lib log lines into t.Log so failure dumps include
// server-side context.
type tLogger struct{ t *testing.T }

func (l *tLogger) Debugf(f string, args ...any) { l.t.Logf("[debug] "+f, args...) }
func (l *tLogger) Infof(f string, args ...any)  { l.t.Logf("[info] "+f, args...) }
func (l *tLogger) Warnf(f string, args ...any)  { l.t.Logf("[warn] "+f, args...) }
func (l *tLogger) Errorf(f string, args ...any) { l.t.Logf("[error] "+f, args...) }

// Compile-time check that tLogger implements wgturn.Logger.
var _ wgturn.Logger = (*tLogger)(nil)
