// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// Package wgturn is the public API of wgturn-core: an embeddable Go library
// that tunnels arbitrary UDP traffic (typically WireGuard) through a
// public TURN relay using DTLS 1.2 obfuscation and STUN ChannelData,
// matching the on-the-wire protocol of kiper292/vk-turn-proxy v2.
//
// # Usage
//
// A typical embedding looks like this:
//
//	tn, err := wgturn.New(wgturn.Config{
//	    PeerAddr:   "vps.example.com:56000",
//	    ListenAddr: "127.0.0.1:9000",
//	    Streams:    4,
//	    PeerType:   wgturn.PeerTypeProxyV2,
//	    Provider:   myProvider,                 // see CredentialsProvider
//	    Protector:  wgturn.NoopProtector{},     // or platform-specific
//	    Logger:     wgturn.NoopLogger{},
//	})
//	if err != nil { return err }
//
//	if err := tn.Start(ctx); err != nil { return err }
//	defer tn.Stop()
//
//	// Point your WireGuard client at 127.0.0.1:9000 and connect normally.
//
// # Architecture
//
// One Tunnel owns N parallel "streams". Each stream:
//
//  1. Fetches TURN credentials via the configured CredentialsProvider
//     (cached per stream-group, see internal/creds).
//  2. Dials the TURN server (TCP or UDP).
//  3. Allocates a relay address and (for proxy_v2 / proxy_v1 modes)
//     wraps the relay channel in DTLS for obfuscation.
//  4. Forwards bytes between a local UDP listener (ListenAddr) and the
//     remote peer (PeerAddr) via the relay.
//
// The Tunnel exposes one logical UDP socket on ListenAddr; the multi-stream
// aggregation is invisible to the caller. WireGuard, sing-box, or any
// other UDP consumer can be pointed at ListenAddr.
//
// # Platform abstraction
//
// All platform-specific concerns are behind two interfaces:
//
//   - SocketProtector — called for every outgoing socket so that a host
//     VPN doesn't catch its own carrier traffic in a routing loop.
//     Android implementations call VpnService.protect(fd); iOS uses
//     NEPacketTunnelProvider; desktop uses NoopProtector.
//
//   - Logger — structured-ish leveled logging. Default is NoopLogger.
//
// # Stability
//
// This package is pre-1.0. The API may change. Sub-packages under internal/
// are not part of the public API and have no compatibility guarantees.
package wgturn
