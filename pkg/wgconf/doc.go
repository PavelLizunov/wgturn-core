// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// Package wgconf parses WireGuard configuration files extended with
// wgturn-specific metadata comments.
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
//	#@wgt:EnableTURN = true
//	#@wgt:Mode = vk_link
//	#@wgt:VkLink = https://vk.com/call/join/abc123
//	#@wgt:PeerType = proxy_v2
//	#@wgt:Streams = 4
//	#@wgt:WatchdogTimeout = 30
//
//	[Peer]
//	PublicKey  = ...
//	Endpoint   = 127.0.0.1:9000   ; client points WG at the local hub
//	AllowedIPs = 0.0.0.0/0
//
// The advantage of this convention is that any vanilla WireGuard tool
// (wg-quick, wg-go, NetworkManager) ignores the `#` comments and the
// file remains valid; wgturn-aware code reads the metadata and brings
// up the proxy.
//
// # Scope
//
// This package only parses the wgturn metadata into a typed Settings
// struct. It does NOT parse the WireGuard portion of the file (use
// existing tooling for that). Use Settings.ToTunnelConfig() to lift the
// settings into a wgturn.Config.
package wgconf
