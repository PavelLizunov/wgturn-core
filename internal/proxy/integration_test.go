// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package proxy_test

// Integration test for the proxy hub. Spins up:
//
//   1. A real pion/turn TURN server on 127.0.0.1:<random>
//   2. A fake "WireGuard server" — really an echoing PacketConn — on
//      127.0.0.1:<random>; it represents the "peer" the Hub is supposed
//      to reach via the TURN allocation.
//   3. A Hub configured with a stub credentials provider, PeerType set
//      to "wireguard" (raw mode, no DTLS — keeps the test deterministic
//      and free of cert-time dependencies).
//
// Then we send a UDP packet at the Hub's local listener and assert the
// echo arrives back. This exercises:
//   - credentials provider plumbing
//   - TURN dial + Allocate
//   - relay TX and RX paths
//   - round-robin scheduling
//   - return-address tracking (peer.Store / peer.Load)

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"

	"github.com/PavelLizunov/wgturn-core/internal/proxy"
)

const (
	turnRealm    = "wgturn-test"
	turnUser     = "u"
	turnPassword = "p"
	startTimeout = 15 * time.Second
)

// startTurnServer brings up a single-listener UDP TURN server on
// 127.0.0.1:<pickedPort>. The caller registers cleanup with t.Cleanup.
func startTurnServer(t *testing.T) (string, func()) {
	t.Helper()

	udpListener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("turn listen: %v", err)
	}

	authKey := turn.GenerateAuthKey(turnUser, turnRealm, turnPassword)

	loggerFactory := logging.NewDefaultLoggerFactory()
	// Suppress info-level chatter so the test output stays readable.
	loggerFactory.DefaultLogLevel = logging.LogLevelError

	srv, err := turn.NewServer(turn.ServerConfig{
		Realm: turnRealm,
		AuthHandler: func(ra *turn.RequestAttributes) (string, []byte, bool) {
			if ra.Username == turnUser {
				return ra.Username, authKey, true
			}
			return "", nil, false
		},
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn: udpListener,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
				RelayAddress: net.ParseIP("127.0.0.1"),
				Address:      "127.0.0.1",
			},
		}},
		LoggerFactory:      loggerFactory,
		AllocationLifetime: time.Minute,
	})
	if err != nil {
		_ = udpListener.Close()
		t.Fatalf("turn.NewServer: %v", err)
	}

	addr := udpListener.LocalAddr().String()
	t.Logf("turn server listening on %s", addr)

	cleanup := func() { _ = srv.Close() }
	t.Cleanup(cleanup)
	return addr, cleanup
}

// echoServer is a minimal UDP echo target playing the role of the
// "WireGuard server" sitting behind the TURN relay. It sends back
// whatever it receives.
type echoServer struct {
	conn      net.PacketConn
	received  atomic.Int64
	echoCount atomic.Int64
}

func startEchoServer(t *testing.T) *echoServer {
	t.Helper()
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	e := &echoServer{conn: c}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			e.received.Add(1)
			payload := append([]byte("ECHO:"), buf[:n]...)
			if _, err := c.WriteTo(payload, from); err == nil {
				e.echoCount.Add(1)
			}
		}
	}()
	t.Cleanup(func() { _ = c.Close() })
	return e
}

func (e *echoServer) Addr() string { return e.conn.LocalAddr().String() }

// stubProvider is the proxy package's own minimal Provider: avoids
// importing pkg/wgturn/provider/stub from internal/proxy (cycle).
type stubProvider struct {
	creds proxy.Credentials
	calls atomic.Int64
}

func (s *stubProvider) Fetch(_ context.Context, _ string, _ int) (proxy.Credentials, error) {
	s.calls.Add(1)
	return s.creds, nil
}

// testLogger emits at Warn+ to keep the test output sane.
type testLogger struct{ t *testing.T }

func (l *testLogger) Debugf(string, ...any)        {}
func (l *testLogger) Infof(string, ...any)         {}
func (l *testLogger) Warnf(f string, args ...any)  { l.t.Logf("[warn] "+f, args...) }
func (l *testLogger) Errorf(f string, args ...any) { l.t.Logf("[error] "+f, args...) }

// TestHub_RawMode_RoundTrip is the headline integration test: real TURN
// server, real echo peer, raw (no-DTLS) mode, single stream, single
// round trip.
func TestHub_RawMode_RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test; skipped under -short")
	}

	turnAddr, _ := startTurnServer(t)
	echo := startEchoServer(t)

	provider := &stubProvider{
		creds: proxy.Credentials{
			Username:   turnUser,
			Password:   turnPassword,
			ServerAddr: turnAddr,
		},
	}

	hub, err := proxy.NewHub(proxy.HubConfig{
		PeerAddr:   echo.Addr(),
		ListenAddr: "127.0.0.1:0",
		Streams:    1,
		PeerType:   "wireguard", // raw mode keeps the test off DTLS
		UDP:        true,        // dial TURN over UDP — faster handshake
		Provider:   provider,
		Protector:  nil, // means no Control hook; net.Dialer accepts that
		Logger:     &testLogger{t: t},
	})
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() { _ = hub.Stop() })

	if err := hub.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case <-hub.Ready():
	case <-time.After(startTimeout):
		t.Fatalf("hub never reached ready (stats=%+v)", hub.Stats())
	}
	t.Logf("hub ready, local addr=%s", hub.LocalAddr())

	// Open a "WG client" UDP socket and send a packet at the hub.
	clientConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("client listen: %v", err)
	}
	defer clientConn.Close()

	hubAddr := hub.LocalAddr()
	payload := []byte("hello-via-turn")
	if _, err := clientConn.WriteTo(payload, hubAddr); err != nil {
		t.Fatalf("write to hub: %v", err)
	}

	// Expect echo back at the client. Generous deadline because the
	// first packet kicks off a CreatePermission + ChannelBind round
	// trip on the TURN server.
	_ = clientConn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 4096)
	n, from, err := clientConn.ReadFrom(buf)
	if err != nil {
		t.Fatalf("client read: %v (echo received=%d echoed=%d, stats=%+v)",
			err, echo.received.Load(), echo.echoCount.Load(), hub.Stats())
	}
	if from.String() != hubAddr.String() {
		t.Errorf("packet came from %s, expected %s", from, hubAddr)
	}
	want := "ECHO:" + string(payload)
	if string(buf[:n]) != want {
		t.Errorf("echo payload = %q, want %q", buf[:n], want)
	}

	stats := hub.Stats()
	if stats.PacketsTx == 0 || stats.PacketsRx == 0 {
		t.Errorf("expected non-zero TX and RX packet counters, got %+v", stats)
	}
	if stats.StreamsRunning != 1 {
		t.Errorf("StreamsRunning = %d, want 1", stats.StreamsRunning)
	}
	if got := provider.calls.Load(); got < 1 {
		t.Errorf("provider was not called at all: %d", got)
	}
}

// TestHub_LocalAddrBeforeStart returns nil rather than panicking.
func TestHub_LocalAddrBeforeStart(t *testing.T) {
	hub, err := proxy.NewHub(proxy.HubConfig{
		PeerAddr:   "1.2.3.4:9000",
		ListenAddr: "127.0.0.1:0",
		Streams:    1,
		Provider:   &stubProvider{},
		Logger:     &testLogger{t: t},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := hub.LocalAddr(); got != nil {
		t.Errorf("LocalAddr before Start = %v, want nil", got)
	}
}
