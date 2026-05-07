// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/PavelLizunov/wgturn-core/pkg/wgshare"
)

// TestRunConnectURL_MissingArgs surfaces clear errors for the two
// most-likely user mistakes: forgetting the URL itself, or forgetting
// the --vk-link the URL needs to actually drive the proxy.
func TestRunConnectURL_MissingArgs(t *testing.T) {
	wellFormedURL := mustEncode(t, wgshare.Profile{
		ServerPublicKey:  "MQ5eopWhtjAyj5IcyLmzfZZ2yRPVbe7WlVWHk79DBQQ=",
		ClientPrivateKey: "SF/myiexWdwolUFVxHeQzixgyll0SzH9ikr7kBir/Uc=",
		Endpoint:         "is-01:56000",
		Address:          netip.MustParsePrefix("10.7.0.5/24"),
	})
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no URL", []string{"--vk-link", "https://vk.com/call/join/abc"}, "share URL is required"},
		{"no VK link", []string{wellFormedURL}, "vk-link"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runConnectURL(tc.args)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// mustEncode is a tiny helper that turns a Profile into a URL or fails
// the test. Used to seed the well-formed-but-incomplete-args cases.
func mustEncode(t *testing.T, p wgshare.Profile) string {
	t.Helper()
	url, err := p.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return url
}

// TestRunConnectURL_BadURL bubbles a wgshare.ErrInvalidURL through
// the connect-url flow so an operator who pasted the wrong string
// sees a useful message rather than a generic "tunnel start failed".
func TestRunConnectURL_BadURL(t *testing.T) {
	err := runConnectURL([]string{"--vk-link", "https://vk.com/call/join/abc", "not-a-wgturn-url"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "parse URL") {
		t.Errorf("err = %v, want parse-URL hint", err)
	}
}

// TestProfileURL_Roundtrip confirms a freshly Encoded Profile (the
// shape provision-url emits) parses back to a consistent shape via
// connect-url's resolver. Smoke for the contract between provision
// and connect — the two always pair at runtime, so a regression here
// is a correctness bug not a unit-test nitpick.
func TestProfileURL_Roundtrip(t *testing.T) {
	original := wgshare.Profile{
		Label:               "alice-phone",
		ServerPublicKey:     "MQ5eopWhtjAyj5IcyLmzfZZ2yRPVbe7WlVWHk79DBQQ=",
		ClientPrivateKey:    "SF/myiexWdwolUFVxHeQzixgyll0SzH9ikr7kBir/Uc=",
		PresharedKey:        "j8p3hvHOOfTBq1LEZzeIRLPq/JoIAect/xFMpN1X/4k=",
		Endpoint:            "is-01.example.com:56000",
		Address:             netip.MustParsePrefix("10.7.0.5/24"),
		AllowedIPs:          []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
		DNS:                 []netip.Addr{netip.MustParseAddr("1.1.1.1")},
		MTU:                 1280,
		PersistentKeepalive: 25 * time.Second,
	}
	url, err := original.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := wgshare.Parse(url)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Label != original.Label || got.Address != original.Address {
		t.Errorf("round-trip differs: %+v vs %+v", got, original)
	}

	// connect-url's ToTunnelConfig + ToKernelConfig conversion must
	// produce non-empty wgturn / wgkernel configs from this Profile.
	turnCfg := got.ToTunnelConfig("https://vk.com/call/join/abc")
	if turnCfg.PeerAddr != "is-01.example.com:56000" {
		t.Errorf("PeerAddr = %q", turnCfg.PeerAddr)
	}
	kCfg := got.ToKernelConfig()
	if len(kCfg.Peers) != 1 || kCfg.Peers[0].PublicKey != original.ServerPublicKey {
		t.Errorf("kernel peers wrong: %+v", kCfg.Peers)
	}
}
