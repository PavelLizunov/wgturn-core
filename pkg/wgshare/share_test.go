// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgshare_test

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/PavelLizunov/wgturn-core/pkg/wgshare"
)

func sampleProfile() wgshare.Profile {
	return wgshare.Profile{
		Label:               "alice",
		ServerPublicKey:     "MQ5eopWhtjAyj5IcyLmzfZZ2yRPVbe7WlVWHk79DBQQ=",
		ClientPrivateKey:    "SF/myiexWdwolUFVxHeQzixgyll0SzH9ikr7kBir/Uc=",
		PresharedKey:        "j8p3hvHOOfTBq1LEZzeIRLPq/JoIAect/xFMpN1X/4k=",
		Endpoint:            "is-01.example.com:56000",
		Address:             netip.MustParsePrefix("10.7.0.5/24"),
		AllowedIPs:          []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")},
		DNS:                 []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("9.9.9.9")},
		MTU:                 1280,
		PersistentKeepalive: 25 * time.Second,
	}
}

// TestEncodeParse_RoundTrip is the headline property: every Profile
// that survives Validate must encode → decode back to itself byte-
// equivalent. Catches any field-renaming or default-handling drift.
func TestEncodeParse_RoundTrip(t *testing.T) {
	want := sampleProfile()

	url, err := want.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.HasPrefix(url, "wgturn://") {
		t.Errorf("missing scheme: %q", url)
	}
	if !strings.Contains(url, "#alice") {
		t.Errorf("missing label fragment: %q", url)
	}

	got, err := wgshare.Parse(url)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got.Label != want.Label ||
		got.ServerPublicKey != want.ServerPublicKey ||
		got.ClientPrivateKey != want.ClientPrivateKey ||
		got.PresharedKey != want.PresharedKey ||
		got.Endpoint != want.Endpoint ||
		got.Address != want.Address ||
		got.MTU != want.MTU ||
		got.PersistentKeepalive != want.PersistentKeepalive {
		t.Errorf("scalar mismatch:\nwant=%+v\n got=%+v", want, got)
	}
	if len(got.AllowedIPs) != len(want.AllowedIPs) {
		t.Fatalf("AllowedIPs len = %d, want %d", len(got.AllowedIPs), len(want.AllowedIPs))
	}
	for i := range want.AllowedIPs {
		if got.AllowedIPs[i] != want.AllowedIPs[i] {
			t.Errorf("AllowedIPs[%d] = %v, want %v", i, got.AllowedIPs[i], want.AllowedIPs[i])
		}
	}
	if len(got.DNS) != len(want.DNS) {
		t.Fatalf("DNS len = %d, want %d", len(got.DNS), len(want.DNS))
	}
	for i := range want.DNS {
		if got.DNS[i] != want.DNS[i] {
			t.Errorf("DNS[%d] = %v, want %v", i, got.DNS[i], want.DNS[i])
		}
	}
}

// TestEncode_OmitEmptyOptionalFields keeps the URL short when the
// caller didn't set DNS/MTU/keepalive/PSK. The size matters for
// phone screenshot/QR distribution.
func TestEncode_OmitEmptyOptionalFields(t *testing.T) {
	p := wgshare.Profile{
		ServerPublicKey:  "MQ5eopWhtjAyj5IcyLmzfZZ2yRPVbe7WlVWHk79DBQQ=",
		ClientPrivateKey: "SF/myiexWdwolUFVxHeQzixgyll0SzH9ikr7kBir/Uc=",
		Endpoint:         "is-01.example.com:56000",
		Address:          netip.MustParsePrefix("10.7.0.5/24"),
	}
	url, err := p.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// Round-trip still works with the minimum.
	got, err := wgshare.Parse(url)
	if err != nil {
		t.Fatalf("Parse minimal: %v", err)
	}
	if got.MTU != 0 || got.PersistentKeepalive != 0 {
		t.Errorf("expected zero optionals, got mtu=%d ka=%v", got.MTU, got.PersistentKeepalive)
	}
	if len(got.DNS) != 0 || len(got.AllowedIPs) != 0 || got.PresharedKey != "" {
		t.Errorf("expected empty optionals: %+v", got)
	}
}

// TestEncode_LabelWithSpaces escapes the fragment so a pasted URL
// survives clipboard handlers that split on whitespace. Round-trip
// recovers the original.
func TestEncode_LabelWithSpaces(t *testing.T) {
	p := sampleProfile()
	p.Label = "Alice's Phone (work)"
	url, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(url, " ") {
		t.Errorf("URL contains literal space: %q", url)
	}
	got, err := wgshare.Parse(url)
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != p.Label {
		t.Errorf("label = %q, want %q", got.Label, p.Label)
	}
}

// TestParse_InvalidInputs collects every malformed form a Parse caller
// might trip over. Each case must error and chain to ErrInvalidURL so
// embedders can match with errors.Is.
func TestParse_InvalidInputs(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty string", ""},
		{"wrong scheme", "https://example.com"},
		{"missing payload", "wgturn://"},
		{"invalid base64", "wgturn://!!!not-base64!!!"},
		{"invalid json", "wgturn://" + base64URL("not json")},
		{"unsupported version", "wgturn://" + base64URL(`{"v":99}`)},
		{"missing required fields", "wgturn://" + base64URL(`{"v":1}`)},
		{"bad address", "wgturn://" + base64URL(`{"v":1,"sp":"a","cp":"b","ep":"x:1","ad":"not-a-cidr"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := wgshare.Parse(tc.in)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !errors.Is(err, wgshare.ErrInvalidURL) {
				t.Errorf("err = %v, want chain containing ErrInvalidURL", err)
			}
		})
	}
}

// TestProfile_Validate_RequiredFields is the unit-level sanity check
// that Validate rejects each missing required field individually. If
// it ever stops, callers risk silently shipping a half-built profile.
func TestProfile_Validate_RequiredFields(t *testing.T) {
	full := sampleProfile()

	cases := []struct {
		name string
		mut  func(*wgshare.Profile)
	}{
		{"missing ServerPublicKey", func(p *wgshare.Profile) { p.ServerPublicKey = "" }},
		{"missing ClientPrivateKey", func(p *wgshare.Profile) { p.ClientPrivateKey = "" }},
		{"missing Endpoint", func(p *wgshare.Profile) { p.Endpoint = "" }},
		{"missing Address", func(p *wgshare.Profile) { p.Address = netip.Prefix{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := full
			tc.mut(&p)
			err := p.Validate()
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !errors.Is(err, wgshare.ErrInvalidURL) {
				t.Errorf("err = %v, want chain ErrInvalidURL", err)
			}
		})
	}
}

// TestEncode_ValidateFails surfaces a Validate error before producing
// a URL — Encoders that returned a malformed URL would propagate the
// half-built profile to wherever the URL gets pasted.
func TestEncode_ValidateFails(t *testing.T) {
	_, err := wgshare.Profile{}.Encode()
	if err == nil {
		t.Fatal("Encode of empty profile returned no error")
	}
	if !errors.Is(err, wgshare.ErrInvalidURL) {
		t.Errorf("err = %v, want ErrInvalidURL", err)
	}
}

// base64URL is a tiny test helper: encodes literal s using URL-safe
// raw base64 — the same alphabet Encode/Parse use.
func base64URL(s string) string {
	const enc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	src := []byte(s)
	var out strings.Builder
	for i := 0; i < len(src); i += 3 {
		var b1, b2, b3 byte
		b1 = src[i]
		if i+1 < len(src) {
			b2 = src[i+1]
		}
		if i+2 < len(src) {
			b3 = src[i+2]
		}
		out.WriteByte(enc[b1>>2])
		out.WriteByte(enc[(b1&0x03)<<4|b2>>4])
		if i+1 < len(src) {
			out.WriteByte(enc[(b2&0x0f)<<2|b3>>6])
		}
		if i+2 < len(src) {
			out.WriteByte(enc[b3&0x3f])
		}
	}
	return out.String()
}
