// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgconf

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestParseString_AllKnownKeys(t *testing.T) {
	const cfg = `
[Interface]
PrivateKey = abc
Address    = 10.7.0.2/32
#@wgt:EnableTURN = true
#@wgt:Mode = vk_link
#@wgt:VkLink = https://vk.com/call/join/xyz
#@wgt:PeerType = proxy_v2
#@wgt:Streams = 4
#@wgt:StreamsPerCred = 2
#@wgt:WatchdogTimeout = 30
#@wgt:UDP = false
#@wgt:TURNHost = 1.2.3.4
#@wgt:TURNPort = 3478
#@wgt:LocalListen = 127.0.0.1:9000
#@wgt:Peer = vps.example.com:56000

[Peer]
PublicKey  = def
Endpoint   = 127.0.0.1:9000
AllowedIPs = 0.0.0.0/0
`
	got, err := ParseString(cfg)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !got.EnableTURN {
		t.Errorf("EnableTURN: want true")
	}
	if got.Mode != "vk_link" {
		t.Errorf("Mode: %q", got.Mode)
	}
	if got.VkLink != "https://vk.com/call/join/xyz" {
		t.Errorf("VkLink: %q", got.VkLink)
	}
	if got.PeerType != "proxy_v2" {
		t.Errorf("PeerType: %q", got.PeerType)
	}
	if got.Streams != 4 {
		t.Errorf("Streams: %d", got.Streams)
	}
	if got.StreamsPerCred != 2 {
		t.Errorf("StreamsPerCred: %d", got.StreamsPerCred)
	}
	if got.WatchdogTimeout != 30*time.Second {
		t.Errorf("WatchdogTimeout: %v", got.WatchdogTimeout)
	}
	if got.UDP {
		t.Errorf("UDP: want false")
	}
	if got.TURNHost != "1.2.3.4" {
		t.Errorf("TURNHost: %q", got.TURNHost)
	}
	if got.TURNPort != 3478 {
		t.Errorf("TURNPort: %d", got.TURNPort)
	}
	if got.LocalListen != DefaultLocalListen {
		t.Errorf("LocalListen: %q", got.LocalListen)
	}
	if got.Peer != "vps.example.com:56000" {
		t.Errorf("Peer: %q", got.Peer)
	}
	if len(got.Unknown) != 0 {
		t.Errorf("Unknown should be empty, got %v", got.Unknown)
	}
}

func TestParseString_KiperCompatibleAliases(t *testing.T) {
	// kiper292 docs use StreamNum; we accept both StreamNum and Streams.
	const cfg = `
#@wgt:EnableTURN = yes
#@wgt:StreamNum = 8
#@wgt:Peer = 10.0.0.1:56000
`
	got, err := ParseString(cfg)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Streams != 8 {
		t.Errorf("Streams: %d", got.Streams)
	}
}

func TestParseString_BoolForms(t *testing.T) {
	cases := map[string]bool{
		"true": true, "True": true, "TRUE": true,
		"yes": true, "on": true, "1": true, "y": true,
		"false": false, "No": false, "off": false, "0": false, "n": false,
	}
	for in, want := range cases {
		got, err := ParseString("#@wgt:EnableTURN = " + in)
		if err != nil {
			t.Errorf("Parse %q: unexpected error %v", in, err)
			continue
		}
		if got.EnableTURN != want {
			t.Errorf("EnableTURN(%q): got %v, want %v", in, got.EnableTURN, want)
		}
	}
}

func TestParseString_DurationForms(t *testing.T) {
	for _, in := range []string{"30", "30s"} {
		got, err := ParseString("#@wgt:WatchdogTimeout = " + in)
		if err != nil {
			t.Fatalf("Parse %q: %v", in, err)
		}
		if got.WatchdogTimeout != 30*time.Second {
			t.Errorf("WatchdogTimeout(%q): %v", in, got.WatchdogTimeout)
		}
	}

	// Non-second durations work too via time.ParseDuration.
	got, err := ParseString("#@wgt:WatchdogTimeout = 2m")
	if err != nil {
		t.Fatalf("Parse 2m: %v", err)
	}
	if got.WatchdogTimeout != 2*time.Minute {
		t.Errorf("WatchdogTimeout 2m: %v", got.WatchdogTimeout)
	}
}

func TestParseString_PrefixCaseInsensitive(t *testing.T) {
	const cfg = `
#@WGT:EnableTURN = true
#@Wgt:Streams = 2
`
	got, err := ParseString(cfg)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !got.EnableTURN || got.Streams != 2 {
		t.Errorf("got=%+v", got)
	}
}

func TestParseString_TrailingSemicolonComment(t *testing.T) {
	got, err := ParseString("#@wgt:Streams = 7 ; cap at 7 to be polite")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Streams != 7 {
		t.Errorf("Streams: %d", got.Streams)
	}
}

func TestParseString_UnknownKeyRecorded(t *testing.T) {
	got, err := ParseString("#@wgt:FutureFlag = experimental")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Unknown["futureflag"] != "experimental" {
		t.Errorf("Unknown: %v", got.Unknown)
	}
}

func TestParseString_NonMetaLinesIgnored(t *testing.T) {
	const cfg = `
[Interface]
PrivateKey = secret-but-not-our-job
# normal comment, not metadata
   #also-not-metadata
;ini comment
PostUp = some shell trickery
#@wgt:EnableTURN = true
`
	got, err := ParseString(cfg)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !got.EnableTURN {
		t.Error("expected EnableTURN")
	}
}

func TestParseString_MalformedReturnsError(t *testing.T) {
	cases := []string{
		"#@wgt:NoEqualsHere",
		"#@wgt: = empty key",
		"#@wgt:Streams = not_a_number",
		"#@wgt:TURNPort = 99999",
		"#@wgt:EnableTURN = maybe",
	}
	for _, in := range cases {
		_, err := ParseString(in)
		if err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}

func TestSettings_ToTunnelConfig_HappyPath(t *testing.T) {
	s := Settings{
		EnableTURN:     true,
		Mode:           "vk_link",
		VkLink:         "https://vk.com/call/join/abc",
		PeerType:       "proxy_v2",
		Streams:        4,
		StreamsPerCred: 4,
		Peer:           "vps:56000",
	}
	cfg, err := s.ToTunnelConfig()
	if err != nil {
		t.Fatalf("ToTunnelConfig: %v", err)
	}
	if cfg.PeerAddr != "vps:56000" || cfg.Streams != 4 || cfg.Hint != s.VkLink {
		t.Errorf("cfg = %+v", cfg)
	}
	if cfg.ListenAddr != DefaultLocalListen {
		t.Errorf("default ListenAddr = %q", cfg.ListenAddr)
	}
}

func TestSettings_ToTunnelConfig_EnableTURNFalse(t *testing.T) {
	_, err := Settings{EnableTURN: false}.ToTunnelConfig()
	if err == nil || !strings.Contains(err.Error(), "EnableTURN is false") {
		t.Errorf("err = %v", err)
	}
}

func TestSettings_ToTunnelConfig_PeerRequired(t *testing.T) {
	_, err := Settings{EnableTURN: true}.ToTunnelConfig()
	if err == nil || !strings.Contains(err.Error(), "Peer is required") {
		t.Errorf("err = %v", err)
	}
}

// _ uses errors so go vet doesn't whine if we ever drop the import.
var _ = errors.New

// --- wg-quick [Interface] / [Peer] section parsing ---

func TestParseString_IfaceSection(t *testing.T) {
	const cfg = `
[Interface]
PrivateKey = SF/myiexWdwolUFVxHeQzixgyll0SzH9ikr7kBir/Uc=
Address    = 10.7.0.2/24
DNS        = 1.1.1.1, 8.8.8.8
MTU        = 1280
ListenPort = 51820

[Peer]
PublicKey    = MQ5eopWhtjAyj5IcyLmzfZZ2yRPVbe7WlVWHk79DBQQ=
PresharedKey = j8p3hvHOOfTBq1LEZzeIRLPq/JoIAect/xFMpN1X/4k=
Endpoint     = 127.0.0.1:9000
AllowedIPs   = 0.0.0.0/0, ::/0
PersistentKeepalive = 25
`
	got, err := ParseString(cfg)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got.Iface.PrivateKey != "SF/myiexWdwolUFVxHeQzixgyll0SzH9ikr7kBir/Uc=" {
		t.Errorf("Iface.PrivateKey = %q", got.Iface.PrivateKey)
	}
	if len(got.Iface.Address) != 1 ||
		got.Iface.Address[0] != netip.MustParsePrefix("10.7.0.2/24") {
		t.Errorf("Iface.Address = %v", got.Iface.Address)
	}
	wantDNS := []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("8.8.8.8")}
	if len(got.Iface.DNS) != len(wantDNS) {
		t.Fatalf("Iface.DNS len = %d, want %d", len(got.Iface.DNS), len(wantDNS))
	}
	for i := range wantDNS {
		if got.Iface.DNS[i] != wantDNS[i] {
			t.Errorf("Iface.DNS[%d] = %v, want %v", i, got.Iface.DNS[i], wantDNS[i])
		}
	}
	if got.Iface.MTU != 1280 {
		t.Errorf("Iface.MTU = %d", got.Iface.MTU)
	}
	if got.Iface.ListenPort != 51820 {
		t.Errorf("Iface.ListenPort = %d", got.Iface.ListenPort)
	}

	if len(got.WGPeers) != 1 {
		t.Fatalf("WGPeers len = %d, want 1", len(got.WGPeers))
	}
	p := got.WGPeers[0]
	if p.PublicKey != "MQ5eopWhtjAyj5IcyLmzfZZ2yRPVbe7WlVWHk79DBQQ=" {
		t.Errorf("Peer.PublicKey = %q", p.PublicKey)
	}
	if p.PresharedKey != "j8p3hvHOOfTBq1LEZzeIRLPq/JoIAect/xFMpN1X/4k=" {
		t.Errorf("Peer.PresharedKey = %q", p.PresharedKey)
	}
	if p.Endpoint != DefaultLocalListen {
		t.Errorf("Peer.Endpoint = %q", p.Endpoint)
	}
	wantAIPs := []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/0"),
		netip.MustParsePrefix("::/0"),
	}
	if len(p.AllowedIPs) != len(wantAIPs) {
		t.Fatalf("Peer.AllowedIPs len = %d, want %d", len(p.AllowedIPs), len(wantAIPs))
	}
	for i := range wantAIPs {
		if p.AllowedIPs[i] != wantAIPs[i] {
			t.Errorf("Peer.AllowedIPs[%d] = %v, want %v", i, p.AllowedIPs[i], wantAIPs[i])
		}
	}
	if p.PersistentKeepalive != 25*time.Second {
		t.Errorf("Peer.PersistentKeepalive = %v", p.PersistentKeepalive)
	}
}

func TestParseString_MultiplePeers(t *testing.T) {
	const cfg = `
[Interface]
PrivateKey = aaa

[Peer]
PublicKey = peer1
AllowedIPs = 10.0.0.1/32

[Peer]
PublicKey = peer2
AllowedIPs = 10.0.0.2/32
Endpoint   = 1.2.3.4:51820
PersistentKeepalive = 30s
`
	got, err := ParseString(cfg)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.WGPeers) != 2 {
		t.Fatalf("WGPeers len = %d, want 2", len(got.WGPeers))
	}
	if got.WGPeers[0].PublicKey != "peer1" || got.WGPeers[1].PublicKey != "peer2" {
		t.Errorf("WGPeers keys = %v", got.WGPeers)
	}
	if got.WGPeers[1].Endpoint != "1.2.3.4:51820" {
		t.Errorf("WGPeers[1].Endpoint = %q", got.WGPeers[1].Endpoint)
	}
	if got.WGPeers[1].PersistentKeepalive != 30*time.Second {
		t.Errorf("PersistentKeepalive = %v", got.WGPeers[1].PersistentKeepalive)
	}
}

func TestParseString_BareAddressPromotedToHostMask(t *testing.T) {
	const cfg = `
[Interface]
Address = 10.7.0.2

[Peer]
PublicKey = abc
AllowedIPs = ::1
`
	got, err := ParseString(cfg)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Iface.Address[0] != netip.MustParsePrefix("10.7.0.2/32") {
		t.Errorf("Iface.Address[0] = %v, want 10.7.0.2/32", got.Iface.Address[0])
	}
	if got.WGPeers[0].AllowedIPs[0] != netip.MustParsePrefix("::1/128") {
		t.Errorf("AllowedIPs[0] = %v, want ::1/128", got.WGPeers[0].AllowedIPs[0])
	}
}

func TestParseString_HostKeysIgnoredSilently(t *testing.T) {
	// PostUp / Table / FwMark / SaveConfig are wg-quick keys we don't
	// implement — they must not error, must not appear in Unknown
	// (Unknown tracks wgturn metadata only).
	const cfg = `
[Interface]
PrivateKey = aaa
PostUp     = iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE
PreDown    = something
Table      = off
FwMark     = 0xff
SaveConfig = true

[Peer]
PublicKey = bbb
AllowedIPs = 10.0.0.0/8
`
	got, err := ParseString(cfg)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Iface.PrivateKey != "aaa" {
		t.Errorf("Iface.PrivateKey = %q", got.Iface.PrivateKey)
	}
	if len(got.Unknown) != 0 {
		t.Errorf("Unknown should not capture host keys; got %v", got.Unknown)
	}
}

func TestParseString_TrailingInlineCommentInIni(t *testing.T) {
	const cfg = `
[Peer]
PublicKey = abc
Endpoint  = 127.0.0.1:9000   ; client points WG at the local hub
AllowedIPs = 0.0.0.0/0
`
	got, err := ParseString(cfg)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.WGPeers[0].Endpoint != DefaultLocalListen {
		t.Errorf("Endpoint = %q (trailing ';' not stripped)", got.WGPeers[0].Endpoint)
	}
}

func TestParseString_MalformedIniReturnsError(t *testing.T) {
	cases := []string{
		"[Interface]\nMTU = not_a_number",
		"[Interface]\nListenPort = 99999",
		"[Interface]\nAddress = not.an.ip",
		"[Interface]\nDNS = 999.999.999.999",
		"[Peer]\nAllowedIPs = bogus",
		"[Peer]\nPersistentKeepalive = lol",
	}
	for _, in := range cases {
		_, err := ParseString(in)
		if err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}

// TestParseString_ServerSideKeys covers the EnableServer / Listen /
// Backend metadata keys introduced for the server-side .conf format.
func TestParseString_ServerSideKeys(t *testing.T) {
	const cfg = `
#@wgt:EnableServer = true
#@wgt:Listen       = :56000
#@wgt:Backend      = udp:127.0.0.1:51820
`
	got, err := ParseString(cfg)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !got.EnableServer {
		t.Error("EnableServer: want true")
	}
	if got.Listen != ":56000" {
		t.Errorf("Listen = %q", got.Listen)
	}
	if got.Backend != "udp:127.0.0.1:51820" {
		t.Errorf("Backend = %q", got.Backend)
	}
}

// TestParseBackendSpec walks the small surface of ParseBackendSpec:
// the two recognised forms plus malformed inputs should all surface
// the right kind / address / error shape.
func TestParseBackendSpec(t *testing.T) {
	cases := []struct {
		name    string
		spec    string
		want    BackendKind
		addr    string
		wantErr bool
	}{
		{name: "udp lowercase", spec: "udp:127.0.0.1:51820", want: BackendUDP, addr: "127.0.0.1:51820"},
		{name: "udp uppercase prefix", spec: "UDP:10.0.0.1:9000", want: BackendUDP, addr: "10.0.0.1:9000"},
		{name: "udp with hostname", spec: "udp:wg0.local:51820", want: BackendUDP, addr: "wg0.local:51820"},
		{name: "wgkernel lowercase", spec: "wgkernel", want: BackendWGKernel},
		{name: "wgkernel uppercase", spec: "WGKERNEL", want: BackendWGKernel},
		{name: "empty", spec: "", wantErr: true},
		{name: "udp without addr", spec: "udp:", wantErr: true},
		{name: "unknown kind", spec: "tcp:127.0.0.1:9000", wantErr: true},
		{name: "garbage", spec: "asdf", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, addr, err := ParseBackendSpec(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ParseBackendSpec(%q) = (%q, %q, nil); want error", tc.spec, kind, addr)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseBackendSpec(%q) error: %v", tc.spec, err)
				return
			}
			if kind != tc.want {
				t.Errorf("kind = %q, want %q", kind, tc.want)
			}
			if addr != tc.addr {
				t.Errorf("addr = %q, want %q", addr, tc.addr)
			}
		})
	}
}

func TestParseString_SectionsAndMetaInterleave(t *testing.T) {
	// wgturn metadata can appear before, between, or after sections —
	// the result is the same.
	const cfg = `
#@wgt:EnableTURN = true
[Interface]
PrivateKey = aaa
#@wgt:Peer = vps:56000
Address = 10.7.0.2/24

[Peer]
PublicKey = bbb
#@wgt:VkLink = https://vk.com/call/join/abc
AllowedIPs = 0.0.0.0/0
`
	got, err := ParseString(cfg)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !got.EnableTURN {
		t.Error("EnableTURN")
	}
	if got.Peer != "vps:56000" {
		t.Errorf("Peer = %q", got.Peer)
	}
	if got.VkLink != "https://vk.com/call/join/abc" {
		t.Errorf("VkLink = %q", got.VkLink)
	}
	if got.Iface.PrivateKey != "aaa" {
		t.Errorf("Iface.PrivateKey = %q", got.Iface.PrivateKey)
	}
	if got.WGPeers[0].PublicKey != "bbb" {
		t.Errorf("WGPeers[0].PublicKey = %q", got.WGPeers[0].PublicKey)
	}
}
