// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgadmin_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/PavelLizunov/wgturn-core/pkg/wgadmin"
)

// TestGenerateKeypair_Shape pins what wg-tools-compatible keys look
// like: 32 raw bytes, base64-standard encoded → 44 chars including
// trailing '='. Both keys end in '=' because 32%3 == 2.
func TestGenerateKeypair_Shape(t *testing.T) {
	kp, err := wgadmin.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	for name, b64 := range map[string]string{"private": kp.Private, "public": kp.Public} {
		if len(b64) != 44 {
			t.Errorf("%s key length %d, want 44", name, len(b64))
		}
		if !strings.HasSuffix(b64, "=") {
			t.Errorf("%s key %q does not end in '='", name, b64)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			t.Errorf("%s key not valid base64: %v", name, err)
		}
		if len(raw) != 32 {
			t.Errorf("%s key decodes to %d bytes, want 32", name, len(raw))
		}
	}
}

// TestGenerateKeypair_Clamping checks the curve25519 clamping bits.
// Without these the public key is not on the right subgroup and
// existing wg implementations reject the peer.
func TestGenerateKeypair_Clamping(t *testing.T) {
	kp, err := wgadmin.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(kp.Private)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if raw[0]&7 != 0 {
		t.Errorf("low 3 bits of byte 0 = %#x, want 0 (clamping)", raw[0]&7)
	}
	if raw[31]&0x80 != 0 {
		t.Errorf("high bit of byte 31 = %#x, want 0 (clamping)", raw[31]&0x80)
	}
	if raw[31]&0x40 == 0 {
		t.Errorf("bit 6 of byte 31 = 0, want 1 (clamping)")
	}
}

// TestPublicKeyFor_RoundTrip: GenerateKeypair → PublicKeyFor on the
// produced private key must reproduce the public key exactly. This
// is the unit test that also covers the server-side derivation in
// Server.Provision.
func TestPublicKeyFor_RoundTrip(t *testing.T) {
	for i := 0; i < 32; i++ {
		kp, err := wgadmin.GenerateKeypair()
		if err != nil {
			t.Fatalf("iter %d: GenerateKeypair: %v", i, err)
		}
		got, err := wgadmin.PublicKeyFor(kp.Private)
		if err != nil {
			t.Fatalf("iter %d: PublicKeyFor: %v", i, err)
		}
		if got != kp.Public {
			t.Errorf("iter %d: derived %q, want %q", i, got, kp.Public)
		}
	}
}

// TestPublicKeyFor_BadInput rejects malformed base64 / wrong-length
// keys with explicit error messages, not panics.
func TestPublicKeyFor_BadInput(t *testing.T) {
	cases := []string{"", "not-base64-!!", "QUJDRA==" /* 4 bytes */}
	for _, in := range cases {
		_, err := wgadmin.PublicKeyFor(in)
		if err == nil {
			t.Errorf("PublicKeyFor(%q): want error, got nil", in)
		}
	}
}

// TestGeneratePresharedKey returns a 44-char base64 value; two calls
// produce different keys (guards against a stuck PRNG seed).
func TestGeneratePresharedKey(t *testing.T) {
	a, err := wgadmin.GeneratePresharedKey()
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	b, err := wgadmin.GeneratePresharedKey()
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if len(a) != 44 || len(b) != 44 {
		t.Errorf("PSK length: %d / %d, want 44", len(a), len(b))
	}
	if a == b {
		t.Error("two PSKs identical — RNG stuck?")
	}
}
