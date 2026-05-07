// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// Package wgconf parses WireGuard configuration files extended with
// wgturn-specific metadata comments AND the standard wg-quick
// [Interface] / [Peer] sections, so a single .conf file is enough to
// stand up both a wgturn.Tunnel and an embedded wgkernel.
//
// # Format
//
// We use the same convention introduced by kiper292/wireguard-turn-android:
// arbitrary lines beginning with `#@wgt:` (case-insensitive) inside a
// standard WireGuard `.conf` file. Each such line carries one
// `Key = Value` pair.
//
//	[Interface]
//	PrivateKey = ...
//	Address    = 10.7.0.2/32
//	DNS        = 1.1.1.1, 8.8.8.8
//	MTU        = 1280
//	#@wgt:EnableTURN = true
//	#@wgt:Mode = vk_link
//	#@wgt:VkLink = https://vk.com/call/join/abc123
//	#@wgt:PeerType = proxy_v2
//	#@wgt:Streams = 24
//	#@wgt:WatchdogTimeout = 30
//	#@wgt:Peer = vps.example.com:56000
//
//	[Peer]
//	PublicKey  = ...
//	Endpoint   = 127.0.0.1:9000   ; client points WG at the local hub
//	AllowedIPs = 0.0.0.0/0
//	PersistentKeepalive = 25
//
// The advantage of this convention is that any vanilla WireGuard tool
// (wg-quick, wg-go, NetworkManager) ignores the `#` comments and the
// file remains valid; wgturn-aware code reads the metadata and brings
// up the proxy.
//
// # Scope
//
// Parse extracts:
//
//   - The wgturn metadata (EnableTURN, VkLink, Streams, Peer, …) into
//     the top-level Settings fields, lifted to wgturn.Config via
//     Settings.ToTunnelConfig.
//
//   - The [Interface] section into Settings.Iface (PrivateKey, Address,
//     DNS, MTU, ListenPort) — enough to build a wgkernel.Config in the
//     caller. Host-side wg-quick keys (PostUp, Table, FwMark,
//     SaveConfig) are silently ignored.
//
//   - Each [Peer] section into Settings.WGPeers (PublicKey, PresharedKey,
//     Endpoint, AllowedIPs, PersistentKeepalive).
//
// Conversion of Iface + WGPeers into a wgkernel.Config is intentionally
// left to the caller: this package has no dependency on wgkernel and
// therefore does not pull in golang.zx2c4.com/wireguard for embedders
// that only need parsing.
package wgconf
