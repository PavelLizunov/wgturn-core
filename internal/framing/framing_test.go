// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package framing_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/PavelLizunov/wgturn-core/internal/framing"
)

// TestHandshake_RoundTrip checks the obvious property: WriteHandshake
// followed by ReadHandshake recovers the same session ID and stream ID,
// and emits exactly HandshakeSize bytes on the wire.
func TestHandshake_RoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		sessionID []byte
		streamID  byte
	}{
		{
			name:      "all-zero session, stream 0",
			sessionID: make([]byte, framing.SessionIDSize),
			streamID:  0,
		},
		{
			name:      "all-ones session, stream 255",
			sessionID: bytes.Repeat([]byte{0xff}, framing.SessionIDSize),
			streamID:  255,
		},
		{
			name: "ascii session, stream 17",
			sessionID: []byte{
				'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h',
				'i', 'j', 'k', 'l', 'm', 'n', 'o', 'p',
			},
			streamID: 17,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := framing.WriteHandshake(&buf, tc.sessionID, tc.streamID); err != nil {
				t.Fatalf("WriteHandshake: %v", err)
			}
			if got := buf.Len(); got != framing.HandshakeSize {
				t.Fatalf("wrote %d bytes, want %d", got, framing.HandshakeSize)
			}

			gotID, gotStream, err := framing.ReadHandshake(&buf)
			if err != nil {
				t.Fatalf("ReadHandshake: %v", err)
			}
			if !bytes.Equal(gotID, tc.sessionID) {
				t.Errorf("session id = %x, want %x", gotID, tc.sessionID)
			}
			if gotStream != tc.streamID {
				t.Errorf("stream id = %d, want %d", gotStream, tc.streamID)
			}
		})
	}
}

// TestHandshake_WireBytes pins the on-the-wire layout: session ID is
// emitted in order, with the stream byte as the trailing byte. Servers
// hex-encode the session ID without further transformation, so any
// re-ordering here would silently break compatibility with existing
// clients in the field.
func TestHandshake_WireBytes(t *testing.T) {
	sessionID := []byte{
		0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
		0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f,
	}
	var buf bytes.Buffer
	if err := framing.WriteHandshake(&buf, sessionID, 42); err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte{}, sessionID...), 42)
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("wire = %x, want %x", buf.Bytes(), want)
	}
}

// TestWriteHandshake_BadSessionLen rejects sessionIDs that don't match
// the protocol's fixed 16-byte width. Callers passing a wrong-sized
// slice almost certainly have a bug; surface it loudly.
func TestWriteHandshake_BadSessionLen(t *testing.T) {
	for _, n := range []int{0, 1, 8, 15, 17, 32} {
		var buf bytes.Buffer
		err := framing.WriteHandshake(&buf, make([]byte, n), 0)
		if err == nil {
			t.Errorf("len=%d: want error, got nil (wrote %d bytes)", n, buf.Len())
		}
	}
}

// TestReadHandshake_ShortRead surfaces a truncated stream as a
// non-EOF error wrapped in io.ErrUnexpectedEOF, the same shape a
// caller gets from io.ReadFull.
func TestReadHandshake_ShortRead(t *testing.T) {
	short := bytes.NewReader(make([]byte, framing.HandshakeSize-1))
	_, _, err := framing.ReadHandshake(short)
	if err == nil {
		t.Fatal("want error on short read, got nil")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("err = %v, want chain containing io.ErrUnexpectedEOF", err)
	}
}

// TestReadHandshake_EmptyStream maps cleanly to io.EOF so callers can
// distinguish "client never sent anything" from "client sent garbage".
func TestReadHandshake_EmptyStream(t *testing.T) {
	_, _, err := framing.ReadHandshake(bytes.NewReader(nil))
	if err == nil {
		t.Fatal("want error on empty stream, got nil")
	}
	if !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want chain containing io.EOF", err)
	}
}
