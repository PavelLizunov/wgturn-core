// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// Package wgshare encodes and decodes "share URLs" — single-string
// representations of a wgturn client profile that bundle every key,
// IP, and option a user needs to connect.
//
// # Format
//
// A share URL has the shape
//
//	wgturn://<base64url-payload>[#label]
//
// where payload is the base64url-encoded JSON of a wireFormat struct
// (see share.go). The URL is opaque to humans; the optional fragment
// after `#` is a free-form label that survives round-trips and is
// shown in CLI output / connection lists.
//
// What's in the payload, what isn't
//
// IN: server's WireGuard public key, a freshly generated client
// private key, optional preshared key, the wgturn DTLS endpoint
// (host:port the server listens on), the assigned client tunnel
// address (a /32 in the server's subnet), AllowedIPs / DNS / MTU /
// PersistentKeepalive.
//
// NOT IN: any VK Calls link. The VK invite that drives the proxy's
// credential rotation is a runtime parameter the user supplies on
// each connect — both because it changes more often than the wg
// keys, and because the share URL is meant to be portable across
// users / devices.
//
// # Threat model
//
// Anyone who can read the share URL gets the WireGuard private key
// and full tunnel access — same property as a vless:// or wg-quick
// .conf attachment. Distribute through a channel you trust (Signal,
// Threema, paper note); rotate by revoking the peer on the server
// and issuing a fresh URL. There is no "URL revocation" primitive —
// access ends when the corresponding [Peer] is removed from the
// server's wg0 config.
//
// # Versioning
//
// The wireFormat struct carries a `v` field (currently 1). Future
// breaking changes bump it; older binaries Parse-error on unknown
// versions instead of silently misinterpreting fields.
package wgshare
