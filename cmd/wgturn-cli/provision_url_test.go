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

// TestRunProvisionURL_MissingArgs surfaces clear errors for the two
// most-likely user mistakes: forgetting the name list, or forgetting
// the --endpoint flag the URL needs to be useful.
func TestRunProvisionURL_MissingArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no names", []string{"--endpoint", "is-01:56000"}, "name is required"},
		{"no endpoint", []string{"alice"}, "--endpoint"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runProvisionURL(tc.args)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// TestParseCIDRList covers the comma/whitespace-tolerant flag parser
// the CLI uses for --allowed.
func TestParseCIDRList(t *testing.T) {
	cases := []struct {
		in     string
		want   []string
		errStr string
	}{
		{"", nil, ""},
		{"   ", nil, ""},
		{"0.0.0.0/0", []string{"0.0.0.0/0"}, ""},
		{"0.0.0.0/0, ::/0", []string{"0.0.0.0/0", "::/0"}, ""},
		{"0.0.0.0/0  ::/0", []string{"0.0.0.0/0", "::/0"}, ""},
		{"not-a-cidr", nil, "invalid CIDR"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseCIDRList(tc.in)
			if tc.errStr != "" {
				if err == nil {
					t.Fatalf("want error containing %q", tc.errStr)
				}
				if !strings.Contains(err.Error(), tc.errStr) {
					t.Errorf("err = %v, want %q", err, tc.errStr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tc.want))
			}
			for i, w := range tc.want {
				if got[i].String() != w {
					t.Errorf("[%d] = %v, want %v", i, got[i], w)
				}
			}
		})
	}
}

// TestParseIPList covers the flag parser the CLI uses for --dns.
// The accepted shapes mirror parseCIDRList for symmetry.
func TestParseIPList(t *testing.T) {
	got, err := parseIPList("1.1.1.1, 9.9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].String() != "1.1.1.1" || got[1].String() != "9.9.9.9" {
		t.Errorf("got %v", got)
	}
	if _, err := parseIPList("not-an-ip"); err == nil {
		t.Error("want error on bad IP")
	}
}

// TestFormatWGQuickConf prints a Profile in the wg-quick layout legacy
// clients expect. Pinning the rendered keys (PrivateKey, PublicKey,
// Address, Endpoint, AllowedIPs) shields against an accidental
// re-ordering that would break older `wg-quick up` invocations.
func TestFormatWGQuickConf(t *testing.T) {
	p := wgshare.Profile{
		Label:               "alice",
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
	out := formatWGQuickConf(p)
	for _, want := range []string{
		"# wgturn-name = alice",
		"[Interface]",
		"PrivateKey = SF/m",
		"Address = 10.7.0.5/24",
		"DNS = 1.1.1.1",
		"MTU = 1280",
		"[Peer]",
		"PublicKey = MQ5eo",
		"PresharedKey = j8p3",
		"Endpoint = is-01.example.com:56000",
		"AllowedIPs = 0.0.0.0/0",
		"PersistentKeepalive = 25",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
