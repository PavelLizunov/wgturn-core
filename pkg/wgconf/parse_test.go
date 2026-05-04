// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgconf

import (
	"errors"
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
	if got.LocalListen != "127.0.0.1:9000" {
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
	if cfg.ListenAddr != "127.0.0.1:9000" {
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
