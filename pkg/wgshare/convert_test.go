// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgshare_test

import (
	"net/netip"
	"testing"

	"github.com/PavelLizunov/wgturn-core/pkg/wgshare"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
)

// TestToTunnelConfig_FieldsCopied confirms ToTunnelConfig forwards
// the Profile's endpoint and adopts the package defaults for fields
// the Profile doesn't carry (Streams, Listen, PeerType, UDP).
func TestToTunnelConfig_FieldsCopied(t *testing.T) {
	p := sampleProfile()
	cfg := p.ToTunnelConfig("https://vk.com/call/join/abc")

	if cfg.PeerAddr != p.Endpoint {
		t.Errorf("PeerAddr = %q, want %q", cfg.PeerAddr, p.Endpoint)
	}
	if cfg.ListenAddr != wgshare.DefaultListen {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, wgshare.DefaultListen)
	}
	if cfg.Streams != wgshare.DefaultStreams {
		t.Errorf("Streams = %d, want %d", cfg.Streams, wgshare.DefaultStreams)
	}
	if cfg.PeerType != wgturn.PeerTypeProxyV2 {
		t.Errorf("PeerType = %q, want %q", cfg.PeerType, wgturn.PeerTypeProxyV2)
	}
	if !cfg.UDP {
		t.Error("UDP = false; want true")
	}
	if cfg.Mode != wgturn.ModeVKLink {
		t.Errorf("Mode = %q, want %q", cfg.Mode, wgturn.ModeVKLink)
	}
	if cfg.Hint != "https://vk.com/call/join/abc" {
		t.Errorf("Hint = %q", cfg.Hint)
	}
}

// TestToTunnelConfig_EmptyVKLink leaves Mode/Hint unset so the stub
// provider path (used in tests) still works.
func TestToTunnelConfig_EmptyVKLink(t *testing.T) {
	cfg := sampleProfile().ToTunnelConfig("")
	if cfg.Mode != "" || cfg.Hint != "" {
		t.Errorf("expected unset Mode/Hint with empty link, got Mode=%q Hint=%q", cfg.Mode, cfg.Hint)
	}
}

// TestToKernelConfig_FieldsCopied checks the wg-quick fields land in
// the right slots: PrivateKey, Address as a single-element slice,
// DNS, MTU, and a single Peer with PublicKey/PSK/AllowedIPs/keepalive.
// Endpoint is left empty because WithTurnTunnel rewrites it.
func TestToKernelConfig_FieldsCopied(t *testing.T) {
	p := sampleProfile()
	cfg := p.ToKernelConfig()

	if cfg.PrivateKey != p.ClientPrivateKey {
		t.Errorf("PrivateKey not copied")
	}
	if len(cfg.Address) != 1 || cfg.Address[0] != p.Address {
		t.Errorf("Address = %v, want [%v]", cfg.Address, p.Address)
	}
	if cfg.MTU != p.MTU {
		t.Errorf("MTU = %d", cfg.MTU)
	}
	if len(cfg.Peers) != 1 {
		t.Fatalf("Peers len = %d", len(cfg.Peers))
	}
	peer := cfg.Peers[0]
	if peer.PublicKey != p.ServerPublicKey ||
		peer.PresharedKey != p.PresharedKey ||
		peer.PersistentKeepalive != p.PersistentKeepalive {
		t.Errorf("peer scalar mismatch: %+v", peer)
	}
	if peer.Endpoint != "" {
		t.Errorf("peer.Endpoint = %q, want empty (WithTurnTunnel rewrites it)", peer.Endpoint)
	}
	if len(peer.AllowedIPs) != len(p.AllowedIPs) {
		t.Fatalf("AllowedIPs len = %d", len(peer.AllowedIPs))
	}
	for i, a := range p.AllowedIPs {
		if peer.AllowedIPs[i] != a {
			t.Errorf("AllowedIPs[%d] = %v", i, peer.AllowedIPs[i])
		}
	}
}

// TestToKernelConfig_AllowedIPsDefault: when the Profile carries no
// AllowedIPs, ToKernelConfig falls back to [0.0.0.0/0] so the
// resulting wg-quick equivalent is "send everything through the
// tunnel" — the most common end-user expectation.
func TestToKernelConfig_AllowedIPsDefault(t *testing.T) {
	p := sampleProfile()
	p.AllowedIPs = nil
	cfg := p.ToKernelConfig()
	want := netip.MustParsePrefix("0.0.0.0/0")
	if len(cfg.Peers[0].AllowedIPs) != 1 || cfg.Peers[0].AllowedIPs[0] != want {
		t.Errorf("AllowedIPs = %v, want [%v]", cfg.Peers[0].AllowedIPs, want)
	}
}
