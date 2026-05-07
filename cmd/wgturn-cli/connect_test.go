// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PavelLizunov/wgturn-core/pkg/wgconf"
	"github.com/PavelLizunov/wgturn-core/pkg/wgkernel"
)

// TestBuildKernelConfig_HappyPath confirms the field-by-field copy
// from wgconf.Settings into wgkernel.Config preserves all wg-quick
// fields the embedded kernel needs.
func TestBuildKernelConfig_HappyPath(t *testing.T) {
	s := wgconf.Settings{
		Iface: wgconf.IfaceSection{
			PrivateKey: "SF/myiexWdwolUFVxHeQzixgyll0SzH9ikr7kBir/Uc=",
			Address:    []netip.Prefix{netip.MustParsePrefix("10.7.0.2/24")},
			DNS:        []netip.Addr{netip.MustParseAddr("1.1.1.1")},
			MTU:        1280,
			ListenPort: 51820,
		},
		WGPeers: []wgconf.PeerSection{{
			PublicKey:           "MQ5eopWhtjAyj5IcyLmzfZZ2yRPVbe7WlVWHk79DBQQ=",
			PresharedKey:        "j8p3hvHOOfTBq1LEZzeIRLPq/JoIAect/xFMpN1X/4k=",
			Endpoint:            "127.0.0.1:9000",
			AllowedIPs:          []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
			PersistentKeepalive: 25 * time.Second,
		}},
	}
	cfg, err := buildKernelConfig(s)
	if err != nil {
		t.Fatalf("buildKernelConfig: %v", err)
	}

	if cfg.PrivateKey != s.Iface.PrivateKey {
		t.Errorf("PrivateKey not copied: %q", cfg.PrivateKey)
	}
	if len(cfg.Address) != 1 || cfg.Address[0] != s.Iface.Address[0] {
		t.Errorf("Address: %v", cfg.Address)
	}
	if cfg.MTU != 1280 || cfg.ListenPort != 51820 {
		t.Errorf("MTU/ListenPort: %d/%d", cfg.MTU, cfg.ListenPort)
	}
	if len(cfg.Peers) != 1 {
		t.Fatalf("Peers len = %d", len(cfg.Peers))
	}
	p := cfg.Peers[0]
	if p.PublicKey != s.WGPeers[0].PublicKey ||
		p.PresharedKey != s.WGPeers[0].PresharedKey ||
		p.Endpoint != s.WGPeers[0].Endpoint ||
		p.PersistentKeepalive != s.WGPeers[0].PersistentKeepalive {
		t.Errorf("Peer scalar mismatch: %+v", p)
	}
	if len(p.AllowedIPs) != 1 || p.AllowedIPs[0] != s.WGPeers[0].AllowedIPs[0] {
		t.Errorf("AllowedIPs: %v", p.AllowedIPs)
	}
}

func TestBuildKernelConfig_MissingPrivateKey(t *testing.T) {
	_, err := buildKernelConfig(wgconf.Settings{
		WGPeers: []wgconf.PeerSection{{PublicKey: "abc"}},
	})
	if err == nil || !strings.Contains(err.Error(), "PrivateKey") {
		t.Errorf("err = %v, want PrivateKey-required", err)
	}
}

func TestBuildKernelConfig_NoPeers(t *testing.T) {
	_, err := buildKernelConfig(wgconf.Settings{
		Iface: wgconf.IfaceSection{PrivateKey: "anything"},
	})
	if err == nil || !strings.Contains(err.Error(), "[Peer]") {
		t.Errorf("err = %v, want Peer-required", err)
	}
}

func TestBuildKernelConfig_PeerMissingPublicKey(t *testing.T) {
	_, err := buildKernelConfig(wgconf.Settings{
		Iface:   wgconf.IfaceSection{PrivateKey: "anything"},
		WGPeers: []wgconf.PeerSection{{}},
	})
	if err == nil || !strings.Contains(err.Error(), "PublicKey") {
		t.Errorf("err = %v, want PublicKey-required", err)
	}
}

// TestParseWGConfig_HappyPath round-trips a real-shape wg-quick config
// through the on-disk path runConnect uses.
func TestParseWGConfig_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wgturn.conf")
	contents := `
[Interface]
PrivateKey = SF/myiexWdwolUFVxHeQzixgyll0SzH9ikr7kBir/Uc=
Address    = 10.7.0.2/24
MTU        = 1280

[Peer]
PublicKey  = MQ5eopWhtjAyj5IcyLmzfZZ2yRPVbe7WlVWHk79DBQQ=
Endpoint   = 127.0.0.1:9000
AllowedIPs = 0.0.0.0/0
PersistentKeepalive = 25

#@wgt:EnableTURN = true
#@wgt:Mode       = vk_link
#@wgt:Peer       = vps:56000
#@wgt:VkLink     = https://vk.com/call/join/abc
#@wgt:Streams    = 24
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write tmp config: %v", err)
	}
	got, err := parseWGConfig(path)
	if err != nil {
		t.Fatalf("parseWGConfig: %v", err)
	}
	if got.Iface.MTU != 1280 || got.Peer != "vps:56000" || len(got.WGPeers) != 1 {
		t.Errorf("unexpected parse: %+v", got)
	}
}

func TestParseWGConfig_MissingFile(t *testing.T) {
	_, err := parseWGConfig(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil || !strings.Contains(err.Error(), "open config") {
		t.Errorf("err = %v, want open-config error", err)
	}
}

// TestResolveChromeURL_OverrideUsedVerbatim confirms an explicit
// --vk-chrome-url short-circuits and never spawns Chrome — that's the
// escape hatch users with their own browser depend on.
func TestResolveChromeURL_OverrideUsedVerbatim(t *testing.T) {
	url, cp, err := resolveChromeURL(context.Background(),
		"http://192.168.0.142:9222", true /*auto, ignored*/, noopLogger{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if url != "http://192.168.0.142:9222" {
		t.Errorf("url = %q", url)
	}
	if cp != nil {
		t.Errorf("did not expect a chromeProcess, got %+v", cp)
	}
}

// TestResolveChromeURL_AutoFalseNoOverride returns empty URL — the
// captcha solver downstream falls back to stdio. This is the only
// path where slider-mode captchas will fail; CLI logs a warning.
func TestResolveChromeURL_AutoFalseNoOverride(t *testing.T) {
	url, cp, err := resolveChromeURL(context.Background(), "", false, noopLogger{})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if url != "" || cp != nil {
		t.Errorf("expected no URL and no process, got %q / %v", url, cp)
	}
}

// TestResolveChromeURL_AutoBubblesLaunchError exercises auto-launch on a
// host with no Chrome installed, asserting the error from launchChrome
// (= findChromeOnPath) flows up unchanged. We use the same $PATH-stub
// trick chrome_test.go uses.
func TestResolveChromeURL_AutoBubblesLaunchError(t *testing.T) {
	if runtime.GOOS == goosDarwin || runtime.GOOS == goosWindows {
		t.Skip("cannot mask system Chrome install paths on this platform")
	}
	t.Setenv("PATH", t.TempDir())

	_, cp, err := resolveChromeURL(context.Background(), "", true, noopLogger{})
	if err == nil {
		t.Fatal("expected error when no Chrome is reachable")
	}
	if cp != nil {
		t.Errorf("did not expect chromeProcess on error, got %+v", cp)
	}
	if !strings.Contains(err.Error(), "chrome auto-launch") {
		t.Errorf("expected chrome auto-launch prefix, got %v", err)
	}
}

// --- Host setup tests (Linux-conditional) ---

// TestSetupHostIface_NonLinuxReturnsHelpfulError ensures macOS/Windows
// users see the manual commands they need to run, not a cryptic
// "command not found".
func TestSetupHostIface_NonLinuxReturnsHelpfulError(t *testing.T) {
	if runtime.GOOS == goosLinux {
		t.Skip("Linux uses the real ip-command path; covered separately")
	}
	_, err := setupHostIface("wgturn0", wgkernel.Config{}, noopLogger{})
	if err == nil {
		t.Fatal("expected unsupported-OS error")
	}
	if !errors.Is(err, errHostSetupUnsupported) {
		t.Errorf("err = %v, want errors.Is(errHostSetupUnsupported)", err)
	}
	for _, want := range []string{"ip link set", "ip addr add", "ip route add"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message missing manual hint %q: %v", want, err)
		}
	}
}

// TestSetupHostIfaceLinux_ExecutesExpectedIPCommands writes a fake
// `ip` binary that records every invocation, points $PATH at it, and
// asserts setupHostIfaceLinux produces the right command sequence
// (link up, addr add per address, route add per non-covered AllowedIPs)
// followed by reverse-order rollback when the returned teardown runs.
func TestSetupHostIfaceLinux_ExecutesExpectedIPCommands(t *testing.T) {
	if runtime.GOOS != goosLinux {
		t.Skip("Linux-only host setup")
	}

	dir := t.TempDir()
	logFile := filepath.Join(dir, "ip.log")
	fakeIP := filepath.Join(dir, "ip")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logFile + "\nexit 0\n"
	if err := os.WriteFile(fakeIP, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ip: %v", err)
	}
	t.Setenv("PATH", dir)

	cfg := wgkernel.Config{
		Address: []netip.Prefix{netip.MustParsePrefix("10.7.0.2/24")},
		Peers: []wgkernel.PeerConfig{{
			PublicKey: "abc",
			AllowedIPs: []netip.Prefix{
				netip.MustParsePrefix("0.0.0.0/0"),
				netip.MustParsePrefix("10.7.0.0/24"), // covered by connected, must be skipped
			},
		}},
	}
	teardown, err := setupHostIfaceLinux("wgturn0", cfg, noopLogger{})
	if err != nil {
		t.Fatalf("setupHostIfaceLinux: %v", err)
	}
	teardown()

	got, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read fake-ip log: %v", err)
	}
	want := []string{
		"link set wgturn0 up",
		"addr add 10.7.0.2/24 dev wgturn0",
		"route add 0.0.0.0/0 dev wgturn0",
		// teardown LIFO:
		"route del 0.0.0.0/0 dev wgturn0",
		"addr del 10.7.0.2/24 dev wgturn0",
		"link set wgturn0 down",
	}
	gotLines := strings.Split(strings.TrimSpace(string(got)), "\n")
	if len(gotLines) != len(want) {
		t.Fatalf("got %d ip invocations, want %d:\n%s",
			len(gotLines), len(want), got)
	}
	for i, w := range want {
		if gotLines[i] != w {
			t.Errorf("ip call #%d = %q, want %q", i, gotLines[i], w)
		}
	}
}

// TestSetupHostIfaceLinux_RollbackOnErrorMidway makes the fake ip
// fail on the third call (route add). The function must roll back
// the addr add and link up that succeeded so we don't leave the box
// in a half-configured state on a partial failure.
func TestSetupHostIfaceLinux_RollbackOnErrorMidway(t *testing.T) {
	if runtime.GOOS != goosLinux {
		t.Skip("Linux-only host setup")
	}

	dir := t.TempDir()
	logFile := filepath.Join(dir, "ip.log")
	fakeIP := filepath.Join(dir, "ip")
	// Fail on `route add` (third call), succeed on everything else
	// including the rollback `addr del` / `link set ... down`.
	script := `#!/bin/sh
printf '%s\n' "$*" >> ` + logFile + `
case "$*" in
  "route add"*) exit 2 ;;
esac
exit 0
`
	if err := os.WriteFile(fakeIP, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ip: %v", err)
	}
	t.Setenv("PATH", dir)

	cfg := wgkernel.Config{
		Address: []netip.Prefix{netip.MustParsePrefix("10.7.0.2/24")},
		Peers: []wgkernel.PeerConfig{{
			PublicKey:  "abc",
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
		}},
	}
	teardown, err := setupHostIfaceLinux("wgturn0", cfg, noopLogger{})
	if err == nil {
		t.Fatal("expected error on route add")
	}
	if teardown != nil {
		t.Error("expected nil teardown on error path")
	}

	got, _ := os.ReadFile(logFile)
	gotLines := strings.Split(strings.TrimSpace(string(got)), "\n")
	want := []string{
		"link set wgturn0 up",              // succeeded
		"addr add 10.7.0.2/24 dev wgturn0", // succeeded
		"route add 0.0.0.0/0 dev wgturn0",  // failed (exit 2)
		// rollback in LIFO order — route add never went in, so its
		// rollback isn't queued:
		"addr del 10.7.0.2/24 dev wgturn0",
		"link set wgturn0 down",
	}
	if len(gotLines) != len(want) {
		t.Fatalf("got %d ip invocations, want %d:\n%s",
			len(gotLines), len(want), got)
	}
	for i, w := range want {
		if gotLines[i] != w {
			t.Errorf("ip call #%d = %q, want %q", i, gotLines[i], w)
		}
	}
}

// TestIsCoveredByConnectedRoute pins the kernel-routes-already-exist
// detection. Misjudging this either spams "RTNETLINK File exists"
// errors (false negative) or drops needed peer routes (false positive).
func TestIsCoveredByConnectedRoute(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		addrs  []string
		want   bool
	}{
		{"identical /24 covered", "10.7.0.0/24", []string{"10.7.0.2/24"}, true},
		{"narrower /32 covered by /24", "10.7.0.5/32", []string{"10.7.0.2/24"}, true},
		{"broader /16 NOT covered by /24", "10.7.0.0/16", []string{"10.7.0.2/24"}, false},
		{"different network NOT covered", "192.168.1.0/24", []string{"10.7.0.2/24"}, false},
		{"v4 prefix NOT covered by v6 addr", "10.0.0.0/8", []string{"::1/128"}, false},
		{"v6 prefix NOT covered by v4 addr", "fd00::/8", []string{"10.0.0.1/24"}, false},
		{"default route 0.0.0.0/0 NOT covered by tunnel addr",
			"0.0.0.0/0", []string{"10.7.0.2/24"}, false},
		{"empty addrs => never covered", "10.0.0.0/8", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pref := netip.MustParsePrefix(c.prefix)
			addrs := make([]netip.Prefix, len(c.addrs))
			for i, a := range c.addrs {
				addrs[i] = netip.MustParsePrefix(a)
			}
			got := isCoveredByConnectedRoute(pref, addrs)
			if got != c.want {
				t.Errorf("isCoveredByConnectedRoute(%v, %v) = %v, want %v",
					pref, addrs, got, c.want)
			}
		})
	}
}
