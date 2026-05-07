// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturnsrv_test

// pair_test is the keystone integration test for N8: it stands up the
// full wgturn proxy stack in-process and asserts that a WireGuard
// handshake between two wgkernel instances completes through it.
//
//   [wgkernel client] --UDP--> [wgturn.Tunnel (Hub)]
//                                   ↓ DTLS over TURN-relayed UDP
//                              [in-process pion/turn]
//                                   ↓
//                              [wgturnsrv.Server]
//                                   ↓ via WGKernelBackend's bind
//                              [wgkernel server]
//
// If both kernels report a non-zero last_handshake_time_sec within
// the deadline, every layer in the stack works end-to-end: cred
// fetch, TURN allocate, DTLS handshake, the 17-byte session+stream
// preamble, demux on the server, eviction-safe addStream, the
// in-process Bind, wireguard-go's state machine on both sides.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
	"golang.org/x/crypto/curve25519"

	"github.com/PavelLizunov/wgturn-core/pkg/wgkernel"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn/provider/stub"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturnsrv"
)

const (
	pairTurnRealm    = "wgturn-pair"
	pairTurnUser     = "u"
	pairTurnPassword = "p"
)

// keyPair returns a (private, public) base64 WireGuard key pair.
// Uses curve25519 directly so the keys are valid curve points the
// WireGuard handshake will actually accept.
func keyPair(t *testing.T) (priv, pub string) {
	t.Helper()
	var sk [32]byte
	if _, err := rand.Read(sk[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	// WireGuard's clamping convention.
	sk[0] &= 248
	sk[31] &= 127
	sk[31] |= 64
	pkBytes, err := curve25519.X25519(sk[:], curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive public: %v", err)
	}
	return base64.StdEncoding.EncodeToString(sk[:]),
		base64.StdEncoding.EncodeToString(pkBytes)
}

// startPairTurnServer brings up a single-listener UDP TURN server on
// 127.0.0.1:<picked>. Mirrors the helper in internal/proxy's
// integration test.
func startPairTurnServer(t *testing.T) string {
	t.Helper()
	udpListener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("turn listen: %v", err)
	}
	authKey := turn.GenerateAuthKey(pairTurnUser, pairTurnRealm, pairTurnPassword)

	loggerFactory := logging.NewDefaultLoggerFactory()
	loggerFactory.DefaultLogLevel = logging.LogLevelError

	srv, err := turn.NewServer(turn.ServerConfig{
		Realm: pairTurnRealm,
		AuthHandler: func(ra *turn.RequestAttributes) (string, []byte, bool) {
			if ra.Username == pairTurnUser {
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
	t.Cleanup(func() { _ = srv.Close() })
	addr := udpListener.LocalAddr().String()
	t.Logf("turn server listening on %s", addr)
	return addr
}

// waitForHandshake polls k.Stats() until the first peer reports a
// non-zero last_handshake_time_sec or the deadline expires.
func waitForHandshake(t *testing.T, k *wgkernel.Kernel, label string, deadline time.Duration) {
	t.Helper()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		st, err := k.Stats()
		if err != nil {
			t.Fatalf("%s Stats: %v", label, err)
		}
		for _, p := range st.Peers {
			if p.LastHandshakeTimeSec != 0 {
				t.Logf("%s saw handshake at %d", label, p.LastHandshakeTimeSec)
				return
			}
		}
		<-tick.C
	}
	st, _ := k.Stats()
	t.Fatalf("%s: no handshake within %v (peers=%+v)", label, deadline, st.Peers)
}

// TestPair_HandshakeTraversesStack is the headline test. It spins up
// the full stack, drives a WireGuard handshake from client → server
// through it, and asserts both sides see a completed handshake within
// 15 seconds. If this test passes, the server is real.
func TestPair_HandshakeTraversesStack(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test; skipped under -short")
	}

	// 1. In-process TURN server: shared "VK relay".
	turnAddr := startPairTurnServer(t)

	// 2. wgturnsrv.Server backed by an in-memory bind into wgkernel#2.
	backend := wgturnsrv.NewWGKernelBackend()
	srv, err := wgturnsrv.New(wgturnsrv.Config{
		ListenAddr:        "127.0.0.1:0",
		Backend:           backend,
		Logger:            &tLogger{t: t},
		StreamReadTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("wgturnsrv.New: %v", err)
	}
	srvCtx, srvCancel := context.WithCancel(context.Background())
	defer srvCancel()
	if err := srv.Start(srvCtx); err != nil {
		t.Fatalf("wgturnsrv.Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	srvAddr := srv.LocalAddr().String()
	t.Logf("wgturnsrv listening on %s", srvAddr)

	// 3. wgkernel#2 (server side): backed by the backend's bind, no
	//    Endpoint on its peer (it never initiates).
	clientPriv, clientPub := keyPair(t)
	serverPriv, serverPub := keyPair(t)

	tunServer, tunClient := wgkernel.NewMemoryTUNPair("pair", 1280, 16)
	t.Cleanup(func() { _ = tunServer.Close() })
	t.Cleanup(func() { _ = tunClient.Close() })

	wgServer, err := wgkernel.New(
		wgkernel.Config{
			PrivateKey: serverPriv,
			ListenPort: 0,
			Peers: []wgkernel.PeerConfig{{
				PublicKey:  clientPub,
				AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.7.0.2/32")},
			}},
		},
		tunServer,
		wgkernel.WithBind(backend.Bind()),
		wgkernel.WithLogger(prefixLogger{t: t, prefix: "wg-server"}),
	)
	if err != nil {
		t.Fatalf("wgkernel#2 New: %v", err)
	}
	t.Cleanup(func() { _ = wgServer.Stop() })
	if err := wgServer.Start(context.Background()); err != nil {
		t.Fatalf("wgkernel#2 Start: %v", err)
	}

	// 4. wgturn.Tunnel (the proxy hub): stub provider points at the
	//    in-process TURN, PeerAddr is the wgturnsrv listener.
	provider := stub.New(pairTurnUser, pairTurnPassword, turnAddr)
	tn, err := wgturn.New(wgturn.Config{
		PeerAddr:   srvAddr,
		ListenAddr: "127.0.0.1:0",
		Streams:    1,
		PeerType:   wgturn.PeerTypeProxyV2,
		UDP:        true,
		Provider:   provider,
		Protector:  wgturn.NoopProtector{},
		Logger:     prefixLogger{t: t, prefix: "hub"},
	})
	if err != nil {
		t.Fatalf("wgturn.New: %v", err)
	}
	tnCtx, tnCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer tnCancel()
	if err := tn.Start(tnCtx); err != nil {
		t.Fatalf("wgturn.Start: %v", err)
	}
	t.Cleanup(func() { _ = tn.Stop() })
	t.Logf("wgturn hub local addr=%s", tn.LocalAddr())

	// 5. wgkernel#1 (client side): default UDP bind, peer endpoint
	//    rewritten by WithTurnTunnel to the hub's local addr.
	wgClient, err := wgkernel.New(
		wgkernel.Config{
			PrivateKey: clientPriv,
			ListenPort: 0,
			Peers: []wgkernel.PeerConfig{{
				PublicKey:           serverPub,
				AllowedIPs:          []netip.Prefix{netip.MustParsePrefix("10.7.0.1/32")},
				PersistentKeepalive: 1 * time.Second,
			}},
		},
		tunClient,
		wgkernel.WithTurnTunnel(tn),
		wgkernel.WithLogger(prefixLogger{t: t, prefix: "wg-client"}),
	)
	if err != nil {
		t.Fatalf("wgkernel#1 New: %v", err)
	}
	t.Cleanup(func() { _ = wgClient.Stop() })
	if err := wgClient.Start(context.Background()); err != nil {
		t.Fatalf("wgkernel#1 Start: %v", err)
	}

	// 6. Wait for both kernels to see the handshake. Generous deadline
	//    because the first packet kicks off TURN's CreatePermission +
	//    ChannelBind round trips before any WG byte traverses.
	waitForHandshake(t, wgClient, "wgkernel#1 (client)", 20*time.Second)
	waitForHandshake(t, wgServer, "wgkernel#2 (server)", 5*time.Second)

	// Sanity: provider was actually called (not a cached / no-op path),
	// and the wgturnsrv saw a session.
	if got := provider.Calls.Load(); got < 1 {
		t.Errorf("stub provider not called: %d", got)
	}
	stats, err := srv.Stats()
	if err != nil {
		t.Fatalf("srv.Stats: %v", err)
	}
	if stats.SessionsActive != 1 {
		t.Errorf("SessionsActive = %d, want 1", stats.SessionsActive)
	}
	if stats.StreamsActive != 1 {
		t.Errorf("StreamsActive = %d, want 1", stats.StreamsActive)
	}
}

// prefixLogger writes log output directly to os.Stderr with a per-
// component prefix. We deliberately avoid t.Logf because wgkernel's
// internal goroutines (RoutineTUNEventReader, encryption workers)
// can race with t.Cleanup and emit one last log line after the test
// returns, which trips t.Logf's "Log called after test completion"
// panic under -race. Stderr has no such constraint.
type prefixLogger struct {
	t      *testing.T
	prefix string
}

func (l prefixLogger) emit(level, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "    %s: [%s][%s] %s\n",
		l.t.Name(), l.prefix, level, fmt.Sprintf(format, args...))
}
func (l prefixLogger) Debugf(format string, args ...any) { l.emit("debug", format, args...) }
func (l prefixLogger) Infof(format string, args ...any)  { l.emit("info", format, args...) }
func (l prefixLogger) Warnf(format string, args ...any)  { l.emit("warn", format, args...) }
func (l prefixLogger) Errorf(format string, args ...any) { l.emit("error", format, args...) }

// Compile-time check that prefixLogger satisfies wgturn.Logger.
var _ wgturn.Logger = prefixLogger{}
