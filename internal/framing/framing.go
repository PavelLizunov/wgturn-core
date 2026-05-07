// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// Package framing holds the proxy_v2 wire-format primitives that both
// sides of the wgturn proxy speak: the 17-byte session+stream handshake
// and the DTLS-config builder. Lives under internal/ so it is shared
// between the client (internal/proxy) and the server (pkg/wgturnsrv)
// without leaking into the public SDK surface.
package framing

import (
	"fmt"
	"io"
)

// SessionIDSize is the length, in bytes, of the session identifier the
// client emits at the start of every stream. The full UUID is treated
// as opaque bytes on the wire; the server hex-encodes it for use as a
// map key (matching the legacy behaviour) but the wire format itself
// is binary.
const SessionIDSize = 16

// HandshakeSize is the total length, in bytes, of the per-stream
// preamble: SessionIDSize bytes of session identifier followed by a
// single byte of stream identifier.
const HandshakeSize = SessionIDSize + 1

// WriteHandshake emits the 17-byte preamble that opens every proxy_v2
// stream. sessionID must be exactly SessionIDSize bytes long; streamID
// is encoded verbatim. The function performs a single Write call so
// the bytes land contiguously in one DTLS record.
func WriteHandshake(w io.Writer, sessionID []byte, streamID byte) error {
	if len(sessionID) != SessionIDSize {
		return fmt.Errorf("framing: session id must be %d bytes, got %d", SessionIDSize, len(sessionID))
	}
	var buf [HandshakeSize]byte
	copy(buf[:SessionIDSize], sessionID)
	buf[SessionIDSize] = streamID
	if _, err := w.Write(buf[:]); err != nil {
		return fmt.Errorf("framing: write handshake: %w", err)
	}
	return nil
}

// ReadHandshake consumes exactly HandshakeSize bytes from r and splits
// them into the session identifier (first SessionIDSize bytes, copied
// into a fresh slice the caller owns) and the stream identifier (last
// byte). Short reads surface as an error from io.ReadFull.
func ReadHandshake(r io.Reader) (sessionID []byte, streamID byte, err error) {
	var buf [HandshakeSize]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return nil, 0, fmt.Errorf("framing: read handshake: %w", err)
	}
	out := make([]byte, SessionIDSize)
	copy(out, buf[:SessionIDSize])
	return out, buf[SessionIDSize], nil
}
