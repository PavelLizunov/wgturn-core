// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgkernel

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// genKey returns a random 32-byte WG key in base64 form. Useful for
// building Configs in tests; the bytes are not real curve25519 scalars
// but Validate / IPC don't care, they only check length / format.
func genKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestDecodeWGKey(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	hexStr := hex.EncodeToString(raw)
	b64 := base64.StdEncoding.EncodeToString(raw)

	cases := []struct {
		name    string
		in      string
		want    []byte
		wantErr bool
	}{
		{"hex 64 chars", hexStr, raw, false},
		{"base64 44 chars", b64, raw, false},
		{"trims surrounding whitespace", "  " + b64 + "  ", raw, false},

		{"empty", "", nil, true},
		{"too short", "abcdef", nil, true},
		{"odd hex", strings.Repeat("g", 64), nil, true},
		{"bad base64", strings.Repeat("@", 44), nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeWGKey(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if string(got) != string(tc.want) {
				t.Errorf("got %x, want %x", got, tc.want)
			}
		})
	}
}

func TestConfigValidate(t *testing.T) {
	priv := genKey(t)
	pub := genKey(t)

	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"happy", Config{
			PrivateKey: priv,
			Peers: []PeerConfig{{
				PublicKey:  pub,
				AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")},
			}},
		}, false},

		{"empty private", Config{}, true},
		{"bad private key length", Config{PrivateKey: "short"}, true},
		{"negative MTU", Config{PrivateKey: priv, MTU: -1}, true},
		{"peer bad public", Config{
			PrivateKey: priv,
			Peers:      []PeerConfig{{PublicKey: "tooshort"}},
		}, true},
		{"peer negative keepalive", Config{
			PrivateKey: priv,
			Peers:      []PeerConfig{{PublicKey: pub, PersistentKeepalive: -time.Second}},
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestConfigIPC_Shape(t *testing.T) {
	priv := genKey(t)
	pub1 := genKey(t)
	pub2 := genKey(t)
	psk := genKey(t)

	cfg := Config{
		PrivateKey: priv,
		ListenPort: 51820,
		Peers: []PeerConfig{
			{
				PublicKey:           pub1,
				PresharedKey:        psk,
				Endpoint:            "10.0.0.1:51820",
				PersistentKeepalive: 25 * time.Second,
				AllowedIPs: []netip.Prefix{
					netip.MustParsePrefix("10.7.0.0/24"),
					netip.MustParsePrefix("0.0.0.0/0"),
				},
			},
			{
				PublicKey:  pub2,
				AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.8.0.1/32")},
			},
		},
	}

	ipc, err := cfg.IPC()
	if err != nil {
		t.Fatalf("IPC: %v", err)
	}

	// Spot-check critical lines. Order matters in the IPC stream — we
	// verify that the peer block stays self-contained.
	for _, want := range []string{
		"private_key=", // hex form, lowercase
		"listen_port=51820",
		"replace_peers=true",
		"public_key=",
		"preshared_key=",
		"endpoint=10.0.0.1:51820",
		"persistent_keepalive_interval=25",
		"replace_allowed_ips=true",
		"allowed_ip=10.7.0.0/24",
		"allowed_ip=0.0.0.0/0",
		"allowed_ip=10.8.0.1/32",
	} {
		if !strings.Contains(ipc, want) {
			t.Errorf("missing %q\nIPC:\n%s", want, ipc)
		}
	}

	// Private key must be hex (lowercase 64 chars) in the wire format.
	for _, line := range strings.Split(ipc, "\n") {
		if rest, ok := strings.CutPrefix(line, "private_key="); ok {
			if len(rest) != 64 {
				t.Errorf("private_key wire form len = %d, want 64", len(rest))
			}
			if _, err := hex.DecodeString(rest); err != nil {
				t.Errorf("private_key not hex: %v", err)
			}
			break
		}
	}
}

func TestConfigIPC_NoEndpointWhenEmpty(t *testing.T) {
	cfg := Config{
		PrivateKey: genKey(t),
		Peers: []PeerConfig{{
			PublicKey: genKey(t),
		}},
	}
	ipc, err := cfg.IPC()
	if err != nil {
		t.Fatalf("IPC: %v", err)
	}
	if strings.Contains(ipc, "endpoint=") {
		t.Errorf("endpoint should be omitted when empty\n%s", ipc)
	}
	if strings.Contains(ipc, "persistent_keepalive_interval=") {
		t.Errorf("keepalive should be omitted when zero\n%s", ipc)
	}
}

func TestEncodeWGKeyBase64_Roundtrip(t *testing.T) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	enc := encodeWGKeyBase64(raw)
	if len(enc) != 44 {
		t.Errorf("len = %d, want 44", len(enc))
	}
	dec, err := decodeWGKey(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(dec) != string(raw) {
		t.Error("roundtrip mismatch")
	}
}
