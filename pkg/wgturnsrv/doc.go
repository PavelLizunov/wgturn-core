// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// Package wgturnsrv is the server-side counterpart to pkg/wgturn: it
// terminates a wgturn proxy_v2 stream and forwards the inner UDP
// payload to a configurable backend (typically a local WireGuard
// listener on the same host).
//
// One Server listens on UDP, accepts DTLS sessions from any number of
// clients, demultiplexes them via the 16-byte session identifier each
// client sends right after the DTLS handshake, and pumps datagrams
// between the DTLS streams and a per-session backend connection.
//
// The wire format matches kiper292/vk-turn-proxy (GPL-3.0); this
// package does not vendor or copy GPL-3.0 sources, only re-implements
// the same protocol from public documentation and observable wire
// behaviour. The shared on-the-wire primitives — handshake encoder /
// decoder and DTLS configuration — live in internal/framing and are
// imported by both client and server, so any future protocol tweak
// has exactly one place to land.
package wgturnsrv
