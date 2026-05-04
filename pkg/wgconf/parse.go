// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgconf

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/slovn/wgturn-core/pkg/wgturn"
)

// MetaPrefix is the prefix that introduces a wgturn metadata line.
const MetaPrefix = "#@wgt:"

// DefaultLocalListen is the local UDP listen address used by
// Settings.ToTunnelConfig when LocalListen is empty.
const DefaultLocalListen = "127.0.0.1:9000"

// Settings holds wgturn-specific values extracted from a WireGuard
// configuration file. Zero values mean "not specified".
type Settings struct {
	// EnableTURN gates whether wgturn should run for this tunnel at all.
	// When false (default), the file is treated as a vanilla WireGuard
	// config and wgturn does nothing.
	EnableTURN bool

	// Mode selects the credentials provider mode. Valid values match
	// wgturn.Mode constants. Defaults to wgturn.ModeStub when empty.
	Mode string

	// VkLink is the full https://vk.com/call/join/<id> URL when Mode is
	// vk_link. Used as the Hint for the credentials provider.
	VkLink string

	// PeerType is "proxy_v2" / "proxy_v1" / "wireguard". Defaults to
	// proxy_v2 when empty.
	PeerType string

	// Streams is the number of parallel TURN streams. Defaults to 1.
	Streams int

	// StreamsPerCred is how many streams share a single cached
	// credentials response. Defaults to 4.
	StreamsPerCred int

	// WatchdogTimeout (seconds in the file) aborts a stream that has
	// not received any RX bytes for this duration. Zero disables it.
	WatchdogTimeout time.Duration

	// UDP forces TURN transport to UDP.
	UDP bool

	// TURNHost / TURNPort override the provider-supplied TURN address.
	TURNHost string
	TURNPort int

	// LocalListen is the local UDP address the hub binds to. Defaults
	// to "127.0.0.1:9000" when empty.
	LocalListen string

	// Peer is the wgturn peer (server) host:port. Required when
	// EnableTURN is true.
	Peer string

	// Unknown captures keys we did not recognise, so callers can warn
	// or fail-strict at their discretion. Keys are normalised to
	// lower-case.
	Unknown map[string]string
}

// Parse reads a WireGuard configuration from r and extracts the wgturn
// metadata lines. Lines that don't start with MetaPrefix (case-insensitive,
// after trimming leading whitespace) are silently ignored.
//
// Parse returns an error only on malformed metadata lines (e.g. missing
// `=`). Unrecognised keys are NOT errors; they are recorded in
// Settings.Unknown so the caller can log/warn as appropriate.
func Parse(r io.Reader) (Settings, error) {
	s := Settings{Unknown: map[string]string{}}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Case-insensitive prefix match
		if !hasPrefixFold(trimmed, MetaPrefix) {
			continue
		}
		body := strings.TrimSpace(trimmed[len(MetaPrefix):])
		if body == "" {
			continue
		}

		// Allow `; comment` after the value.
		if i := strings.IndexAny(body, ";"); i >= 0 {
			body = strings.TrimSpace(body[:i])
		}

		eq := strings.IndexByte(body, '=')
		if eq < 0 {
			return s, fmt.Errorf("wgconf: line %d: missing '=' in metadata %q", lineNo, line)
		}
		key := strings.TrimSpace(body[:eq])
		val := strings.TrimSpace(body[eq+1:])
		if key == "" {
			return s, fmt.Errorf("wgconf: line %d: empty key in metadata %q", lineNo, line)
		}

		if err := s.set(key, val); err != nil {
			return s, fmt.Errorf("wgconf: line %d: %w", lineNo, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return s, fmt.Errorf("wgconf: read: %w", err)
	}
	return s, nil
}

// ParseString is a string convenience wrapper around Parse.
func ParseString(text string) (Settings, error) {
	return Parse(strings.NewReader(text))
}

// set assigns a single key=value pair, returning an error if the value
// is malformed for the given key. Keys are matched case-insensitively.
func (s *Settings) set(key, val string) error {
	switch strings.ToLower(key) {
	case "enableturn":
		b, err := parseBool(val)
		if err != nil {
			return fmt.Errorf("EnableTURN: %w", err)
		}
		s.EnableTURN = b

	case "mode":
		s.Mode = val

	case "vklink":
		s.VkLink = val

	case "peertype":
		s.PeerType = val

	case "streamnum", "streams":
		// Accept both names: kiper292's docs use "StreamNum", we prefer
		// "Streams" for consistency with wgturn.Config.
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 {
			return fmt.Errorf("Streams: invalid integer %q", val)
		}
		s.Streams = n

	case "streamspercred":
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 {
			return fmt.Errorf("StreamsPerCred: invalid integer %q", val)
		}
		s.StreamsPerCred = n

	case "watchdogtimeout":
		// Accept both bare seconds (kiper292 convention) and Go duration
		// strings (e.g. "30s", "2m") for forward compatibility.
		if d, err := time.ParseDuration(val); err == nil {
			s.WatchdogTimeout = d
			break
		}
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 {
			return fmt.Errorf("WatchdogTimeout: invalid value %q", val)
		}
		s.WatchdogTimeout = time.Duration(n) * time.Second

	case "udp":
		b, err := parseBool(val)
		if err != nil {
			return fmt.Errorf("UDP: %w", err)
		}
		s.UDP = b

	case "turnhost":
		s.TURNHost = val
	case "turnport":
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 || n > 65535 {
			return fmt.Errorf("TURNPort: invalid port %q", val)
		}
		s.TURNPort = n

	case "locallisten":
		s.LocalListen = val
	case "peer":
		s.Peer = val

	default:
		s.Unknown[strings.ToLower(key)] = val
	}
	return nil
}

// parseBool accepts the casual variants people put in config files.
func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "y":
		return true, nil
	case "0", "false", "no", "off", "n":
		return false, nil
	}
	return false, fmt.Errorf("expected boolean, got %q", v)
}

// hasPrefixFold reports whether s starts with prefix, case-insensitively.
func hasPrefixFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return strings.EqualFold(s[:len(prefix)], prefix)
}

// ToTunnelConfig lifts Settings into a wgturn.Config skeleton. The
// caller must still provide Provider, Protector, and (optionally) a
// Logger before constructing a Tunnel — this function fills in only the
// fields that come from the file.
//
// Returns an error if EnableTURN is false (in which case wgturn should
// not run for this config) or if a required field (like Peer) is
// missing.
func (s Settings) ToTunnelConfig() (wgturn.Config, error) {
	if !s.EnableTURN {
		return wgturn.Config{}, errors.New("wgconf: EnableTURN is false")
	}
	if s.Peer == "" {
		return wgturn.Config{}, errors.New("wgconf: Peer is required when EnableTURN is true")
	}
	listen := s.LocalListen
	if listen == "" {
		listen = DefaultLocalListen
	}
	cfg := wgturn.Config{
		PeerAddr:         s.Peer,
		ListenAddr:       listen,
		Streams:          s.Streams,
		StreamsPerCred:   s.StreamsPerCred,
		PeerType:         wgturn.PeerType(s.PeerType),
		Mode:             wgturn.Mode(s.Mode),
		Hint:             s.VkLink,
		UDP:              s.UDP,
		TURNHostOverride: s.TURNHost,
		TURNPortOverride: s.TURNPort,
		WatchdogTimeout:  s.WatchdogTimeout,
	}
	return cfg, nil
}
