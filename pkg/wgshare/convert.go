// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgshare

import (
	"net/netip"

	"github.com/PavelLizunov/wgturn-core/pkg/wgkernel"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
)

// DefaultStreams is the number of parallel TURN streams ToTunnelConfig
// uses when the caller does not override it. Matches cmd/wgturn-cli's
// hard-won default — empirically the sweet spot for VK Calls per
// source IP.
const DefaultStreams = 24

// DefaultListen is the local UDP listen address ToTunnelConfig sets
// on the produced wgturn.Config. "127.0.0.1:0" lets the kernel pick
// an ephemeral port; the wgkernel built from the same Profile picks
// it up via wgkernel.WithTurnTunnel.
const DefaultListen = "127.0.0.1:0"

// ToTunnelConfig builds the wgturn.Config skeleton for the proxy hub.
// The caller must still attach Provider, Logger, and (if needed)
// Protector / TURNHostOverride / etc. — fields wgshare can't fill in
// because they're embedder-specific.
//
// vkLink is the runtime VK invite that drives credential rotation.
// Pass an empty string to leave the Hint unset (useful for the stub
// provider in tests). Multiple VK links can be supplied via the
// returned config's Hints field after the call:
//
//	cfg := profile.ToTunnelConfig("")
//	cfg.Hints = []string{link1, link2, link3}
//
// Mode defaults to ModeVKLink when vkLink is non-empty so the public
// Tunnel.Validate accepts the config without further fiddling.
func (p Profile) ToTunnelConfig(vkLink string) wgturn.Config {
	cfg := wgturn.Config{
		PeerAddr:   p.Endpoint,
		ListenAddr: DefaultListen,
		Streams:    DefaultStreams,
		PeerType:   wgturn.PeerTypeProxyV2,
		UDP:        true,
	}
	if vkLink != "" {
		cfg.Mode = wgturn.ModeVKLink
		cfg.Hint = vkLink
	}
	return cfg
}

// ToKernelConfig builds the wgkernel.Config that drives the embedded
// WireGuard userspace. The Peer's Endpoint is intentionally left
// empty: the caller wires it up through wgkernel.WithTurnTunnel(tn)
// at construction time, which rewrites it to the local hub's
// ListenAddr once tn.Start has bound a port.
//
// AllowedIPs falls back to [0.0.0.0/0] when the profile carries none —
// the most common case for "send everything through the tunnel".
// Embedders that want split tunneling override after the call.
func (p Profile) ToKernelConfig() wgkernel.Config {
	allowed := p.AllowedIPs
	if len(allowed) == 0 {
		allowed = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}
	}
	return wgkernel.Config{
		PrivateKey: p.ClientPrivateKey,
		Address:    []netip.Prefix{p.Address},
		DNS:        p.DNS,
		MTU:        p.MTU,
		Peers: []wgkernel.PeerConfig{{
			PublicKey:           p.ServerPublicKey,
			PresharedKey:        p.PresharedKey,
			AllowedIPs:          allowed,
			PersistentKeepalive: p.PersistentKeepalive,
		}},
	}
}
