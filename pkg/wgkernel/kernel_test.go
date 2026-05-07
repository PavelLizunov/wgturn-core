// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgkernel_test

// End-to-end test: stand up two Kernels in the same process, paired
// memory TUNs, real curve25519 keys, real localhost-UDP carrier. We
// do NOT speak full IP — the WireGuard handshake itself is the
// observable event we care about. last_handshake_time_sec going
// non-zero on both ends proves the WG state machine, the IPC config,
// and the conn.Bind plumbing all work together.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"

	"github.com/PavelLizunov/wgturn-core/pkg/wgkernel"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
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

// waitForHandshake polls k.Stats() until the first peer reports a
// non-zero last_handshake_time_sec. Returns when seen, or fails the
// test on deadline.
func waitForHandshake(t *testing.T, k *wgkernel.Kernel, deadline time.Duration) {
	t.Helper()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		st, err := k.Stats()
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		for _, p := range st.Peers {
			if p.LastHandshakeTimeSec != 0 {
				return
			}
		}
		<-tick.C
	}
	st, _ := k.Stats()
	t.Fatalf("no handshake within %v (peers=%+v)", deadline, st.Peers)
}

func TestKernel_Handshake_BothEnds(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end WG handshake; skipped under -short")
	}

	serverPriv, serverPub := keyPair(t)
	clientPriv, clientPub := keyPair(t)

	// Paired in-memory TUNs are not strictly needed for the handshake
	// (the handshake is purely UDP-carrier-side), but they must exist
	// because device.NewDevice requires a tun.Device.
	tunServer, tunClient := wgkernel.NewMemoryTUNPair("e2e", 1280, 16)
	t.Cleanup(func() { _ = tunServer.Close() })
	t.Cleanup(func() { _ = tunClient.Close() })

	// Server: ListenPort=0 so we discover the kernel-picked port,
	// then point client at it.
	serverCfg := wgkernel.Config{
		PrivateKey: serverPriv,
		ListenPort: 0,
		Peers: []wgkernel.PeerConfig{{
			PublicKey:  clientPub,
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.7.0.2/32")},
		}},
	}
	server, err := wgkernel.New(serverCfg, tunServer)
	if err != nil {
		t.Fatalf("server New: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop() })

	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("server Start: %v", err)
	}
	port, err := server.ActualListenPort()
	if err != nil {
		t.Fatalf("ActualListenPort: %v", err)
	}
	if port == 0 {
		t.Fatal("server ActualListenPort = 0")
	}
	t.Logf("server bound to UDP/%d", port)

	clientCfg := wgkernel.Config{
		PrivateKey: clientPriv,
		ListenPort: 0,
		Peers: []wgkernel.PeerConfig{{
			PublicKey:           serverPub,
			Endpoint:            netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port).String(),
			AllowedIPs:          []netip.Prefix{netip.MustParsePrefix("10.7.0.1/32")},
			PersistentKeepalive: 1 * time.Second, // force initiator to send right away
		}},
	}
	client, err := wgkernel.New(clientCfg, tunClient)
	if err != nil {
		t.Fatalf("client New: %v", err)
	}
	t.Cleanup(func() { _ = client.Stop() })

	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("client Start: %v", err)
	}

	waitForHandshake(t, client, 10*time.Second)
	waitForHandshake(t, server, 5*time.Second)

	// Verify Stats() shape:
	st, err := server.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Peers) != 1 {
		t.Errorf("server Peers = %d, want 1", len(st.Peers))
	}
	if st.Peers[0].LastHandshakeTimeSec == 0 {
		t.Errorf("server peer last_handshake_time_sec = 0")
	}
}

func TestKernel_Validate_RequiresTUN(t *testing.T) {
	priv, _ := keyPair(t)
	_, err := wgkernel.New(wgkernel.Config{PrivateKey: priv}, nil)
	if err == nil || !contains(err.Error(), "nil tun") {
		t.Errorf("err = %v", err)
	}
}

func TestKernel_StartTwice_Errors(t *testing.T) {
	priv, _ := keyPair(t)
	a, _ := wgkernel.NewMemoryTUNPair("dup", 1280, 4)
	t.Cleanup(func() { _ = a.Close() })

	k, err := wgkernel.New(wgkernel.Config{PrivateKey: priv}, a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Stop() })

	if err := k.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	err = k.Start(context.Background())
	if err == nil || !contains(err.Error(), "already started") {
		t.Errorf("second Start err = %v", err)
	}
}

func TestKernel_StatsBeforeStart(t *testing.T) {
	priv, _ := keyPair(t)
	a, _ := wgkernel.NewMemoryTUNPair("stats-pre", 1280, 4)
	t.Cleanup(func() { _ = a.Close() })

	k, err := wgkernel.New(wgkernel.Config{PrivateKey: priv}, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Stats(); err == nil {
		t.Error("expected Stats error before Start")
	}
}

func TestKernel_StopBeforeStart_NoOp(t *testing.T) {
	priv, _ := keyPair(t)
	a, _ := wgkernel.NewMemoryTUNPair("stop-pre", 1280, 4)
	t.Cleanup(func() { _ = a.Close() })

	k, err := wgkernel.New(wgkernel.Config{PrivateKey: priv}, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Stop(); err != nil {
		t.Errorf("Stop on never-started Kernel: %v", err)
	}
}

func TestKernel_WithTurnTunnel_RequiresStarted(t *testing.T) {
	// Make a not-started Tunnel; WithTurnTunnel should refuse.
	tn, err := wgturn.New(wgturn.Config{
		PeerAddr:   "1.2.3.4:56000",
		ListenAddr: "127.0.0.1:0",
		Streams:    1,
		Provider:   nullProvider{},
		Protector:  wgturn.NoopProtector{},
	})
	if err != nil {
		t.Fatal(err)
	}

	priv, pub := keyPair(t)
	_, otherPub := keyPair(t)
	_ = otherPub

	a, _ := wgkernel.NewMemoryTUNPair("turn-not-started", 1280, 4)
	t.Cleanup(func() { _ = a.Close() })

	k, err := wgkernel.New(wgkernel.Config{
		PrivateKey: priv,
		Peers:      []wgkernel.PeerConfig{{PublicKey: pub}},
	}, a, wgkernel.WithTurnTunnel(tn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Stop() })

	err = k.Start(context.Background())
	if err == nil || !contains(err.Error(), "not started") {
		t.Errorf("Start with un-started TURN tunnel: err = %v", err)
	}
}

// nullProvider is a CredentialsProvider that always returns ErrNotStarted —
// used only because wgturn.Config.Validate insists on a non-nil provider.
type nullProvider struct{}

func (nullProvider) Fetch(context.Context, string, int) (wgturn.Credentials, error) {
	return wgturn.Credentials{}, errors.New("null provider")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
