// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgconf

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/netip"
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
// configuration file plus the standard wg-quick [Interface] / [Peer]
// sections. Zero values mean "not specified".
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
	// EnableTURN is true. Note: this is the wgturn-server endpoint,
	// distinct from the WireGuard [Peer] section captured in WGPeers.
	Peer string

	// Iface holds the parsed [Interface] section of the wg-quick config.
	// Zero value means the file had no [Interface] section.
	Iface IfaceSection

	// WGPeers holds the parsed [Peer] sections of the wg-quick config,
	// in source order. May be empty if the file has no [Peer] sections.
	WGPeers []PeerSection

	// Unknown captures keys we did not recognise, so callers can warn
	// or fail-strict at their discretion. Keys are normalised to
	// lower-case. Only wgturn metadata keys are tracked here; unknown
	// wg-quick ini keys (PostUp, Table, FwMark, SaveConfig, …) are
	// silently ignored because they're outside our concern.
	Unknown map[string]string
}

// IfaceSection mirrors the wg-quick [Interface] section in typed form.
// Only the fields wgturn-core needs to bring up an embedded WireGuard
// kernel are parsed; platform-specific keys (PostUp, Table, FwMark,
// SaveConfig) are silently ignored.
type IfaceSection struct {
	// PrivateKey is the local WG private key. Format follows wg-quick:
	// 44-character base64 (typical) or 64-character hex.
	PrivateKey string

	// Address is the list of CIDRs the local end of the tunnel claims.
	// A bare IP (no /N) is accepted and treated as /32 (IPv4) or /128
	// (IPv6) for forward-compat with relaxed configs.
	Address []netip.Prefix

	// DNS is the list of recommended DNS resolvers. Informational only —
	// wgkernel does not configure host DNS itself.
	DNS []netip.Addr

	// MTU is the interface MTU. Zero means "use the wgkernel default".
	MTU int

	// ListenPort is the WG bind port. Zero means "let the kernel pick".
	ListenPort uint16
}

// PeerSection mirrors a single wg-quick [Peer] section in typed form.
type PeerSection struct {
	// PublicKey of the remote peer. base64 (44 chars) or hex (64 chars).
	PublicKey string

	// PresharedKey is the optional symmetric pre-shared key.
	PresharedKey string

	// Endpoint is the remote IP:port (or host:port). May be empty for
	// reactive peers that only learn endpoints from successful handshakes.
	Endpoint string

	// AllowedIPs is the set of CIDRs whose packets should route to this
	// peer. wg-quick accepts comma-separated lists.
	AllowedIPs []netip.Prefix

	// PersistentKeepalive is the heartbeat interval. wg-quick uses bare
	// seconds ("PersistentKeepalive = 25"); we also accept Go duration
	// strings ("25s", "1m") for forward-compat.
	PersistentKeepalive time.Duration
}

// section tracks the current ini section while parsing.
type section int

const (
	sectionNone section = iota
	sectionInterface
	sectionPeer
)

// Parse reads a WireGuard configuration from r and extracts both the
// wgturn metadata lines AND the standard wg-quick [Interface] / [Peer]
// sections. The result is sufficient to bring up a wgturn.Tunnel
// (via ToTunnelConfig) and an embedded WireGuard kernel from the same
// .conf file.
//
// Lines starting with MetaPrefix (case-insensitive) are wgturn
// metadata; they're parsed regardless of the current section so legacy
// configs that put metadata at file top still work.
//
// Lines inside [Interface] / [Peer] are treated as wg-quick ini.
// Unrecognised keys (PostUp, Table, FwMark, SaveConfig, …) are silently
// ignored — wgturn-core does not concern itself with platform-specific
// host setup.
//
// Parse returns an error only on malformed lines (missing '=', invalid
// numbers, malformed CIDRs).
func Parse(r io.Reader) (Settings, error) {
	s := Settings{Unknown: map[string]string{}}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	cur := sectionNone
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if trimmed == "" {
			continue
		}

		// wgturn metadata works regardless of section.
		if hasPrefixFold(trimmed, MetaPrefix) {
			if err := s.parseMeta(trimmed, line, lineNo); err != nil {
				return s, err
			}
			continue
		}

		// Pure comment lines (not wgturn metadata).
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}

		// Section header.
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			name := strings.ToLower(strings.TrimSpace(trimmed[1 : len(trimmed)-1]))
			switch name {
			case "interface":
				cur = sectionInterface
			case "peer":
				cur = sectionPeer
				s.WGPeers = append(s.WGPeers, PeerSection{})
			default:
				cur = sectionNone
			}
			continue
		}

		// wg-quick ini key = value, only meaningful inside a known section.
		if cur == sectionNone {
			continue
		}

		// Allow trailing "; comment" after the value (matches wg-quick lenience).
		body := trimmed
		if i := strings.IndexByte(body, ';'); i >= 0 {
			body = strings.TrimSpace(body[:i])
		}
		if body == "" {
			continue
		}

		eq := strings.IndexByte(body, '=')
		if eq < 0 {
			// Not a key=value line; ignore silently. wg-quick has none of
			// these in well-formed configs, but lenience here matches the
			// "unknown PostUp lines etc." policy.
			continue
		}
		key := strings.TrimSpace(body[:eq])
		val := strings.TrimSpace(body[eq+1:])
		if key == "" {
			continue
		}

		switch cur {
		case sectionNone:
			// Unreachable: guarded above. Listed to satisfy `exhaustive`.
		case sectionInterface:
			if err := s.setIface(key, val); err != nil {
				return s, fmt.Errorf("wgconf: line %d: %w", lineNo, err)
			}
		case sectionPeer:
			if err := s.setPeer(key, val); err != nil {
				return s, fmt.Errorf("wgconf: line %d: %w", lineNo, err)
			}
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

// parseMeta parses one `#@wgt:Key = Value` line.
func (s *Settings) parseMeta(trimmed, raw string, lineNo int) error {
	body := strings.TrimSpace(trimmed[len(MetaPrefix):])
	if body == "" {
		return nil
	}
	if i := strings.IndexByte(body, ';'); i >= 0 {
		body = strings.TrimSpace(body[:i])
	}
	eq := strings.IndexByte(body, '=')
	if eq < 0 {
		return fmt.Errorf("wgconf: line %d: missing '=' in metadata %q", lineNo, raw)
	}
	key := strings.TrimSpace(body[:eq])
	val := strings.TrimSpace(body[eq+1:])
	if key == "" {
		return fmt.Errorf("wgconf: line %d: empty key in metadata %q", lineNo, raw)
	}
	if err := s.set(key, val); err != nil {
		return fmt.Errorf("wgconf: line %d: %w", lineNo, err)
	}
	return nil
}

// set assigns a single wgturn-metadata key=value pair, returning an
// error if the value is malformed for the given key. Keys are matched
// case-insensitively.
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

// setIface assigns a single wg-quick [Interface] key=value pair. Keys
// are matched case-insensitively. Unknown keys (PostUp, Table, FwMark,
// SaveConfig, …) are silently ignored — they're host-side concerns
// outside wgturn-core's scope.
func (s *Settings) setIface(key, val string) error {
	switch strings.ToLower(key) {
	case "privatekey":
		s.Iface.PrivateKey = val
	case "address":
		prefixes, err := parsePrefixList(val)
		if err != nil {
			return fmt.Errorf("Address: %w", err)
		}
		s.Iface.Address = prefixes
	case "dns":
		addrs, err := parseAddrList(val)
		if err != nil {
			return fmt.Errorf("DNS: %w", err)
		}
		s.Iface.DNS = addrs
	case "mtu":
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 {
			return fmt.Errorf("MTU: invalid integer %q", val)
		}
		s.Iface.MTU = n
	case "listenport":
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 || n > 65535 {
			return fmt.Errorf("ListenPort: invalid port %q", val)
		}
		s.Iface.ListenPort = uint16(n)
	default:
		// Silently ignore: PostUp, PreDown, Table, FwMark, SaveConfig, …
	}
	return nil
}

// setPeer assigns a single wg-quick [Peer] key=value pair to the
// most-recently-opened peer in s.WGPeers. Caller guarantees the slice
// is non-empty (Parse appends an entry on every "[Peer]" header).
func (s *Settings) setPeer(key, val string) error {
	if len(s.WGPeers) == 0 {
		// Defensive: shouldn't happen, Parse appends on the section header.
		return errors.New("setPeer called with no active peer")
	}
	p := &s.WGPeers[len(s.WGPeers)-1]
	switch strings.ToLower(key) {
	case "publickey":
		p.PublicKey = val
	case "presharedkey":
		p.PresharedKey = val
	case "endpoint":
		p.Endpoint = val
	case "allowedips":
		prefixes, err := parsePrefixList(val)
		if err != nil {
			return fmt.Errorf("AllowedIPs: %w", err)
		}
		p.AllowedIPs = prefixes
	case "persistentkeepalive":
		// wg-quick: bare seconds. We also accept Go duration strings
		// ("25s", "1m") for forward-compat.
		if d, err := time.ParseDuration(val); err == nil {
			p.PersistentKeepalive = d
			break
		}
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 {
			return fmt.Errorf("PersistentKeepalive: invalid value %q", val)
		}
		p.PersistentKeepalive = time.Duration(n) * time.Second
	default:
		// Silently ignore unknown peer-section keys.
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

// splitListField splits a wg-quick list field by commas and whitespace.
// Empty entries are dropped. Callers that want to parse the entries as
// CIDRs or addrs should pass each result through netip.ParsePrefix etc.
func splitListField(v string) []string {
	parts := strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// parsePrefixList parses a wg-quick CIDR list. A bare address (no /N)
// is accepted and promoted to /32 (IPv4) or /128 (IPv6) — that's the
// lenience wg-quick itself shows for Address fields.
func parsePrefixList(v string) ([]netip.Prefix, error) {
	tokens := splitListField(v)
	if len(tokens) == 0 {
		return nil, nil
	}
	out := make([]netip.Prefix, 0, len(tokens))
	for _, t := range tokens {
		if pref, err := netip.ParsePrefix(t); err == nil {
			out = append(out, pref)
			continue
		}
		// Bare address fallback: treat as /32 or /128.
		addr, err := netip.ParseAddr(t)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR or address %q", t)
		}
		bits := 32
		if addr.Is6() {
			bits = 128
		}
		out = append(out, netip.PrefixFrom(addr, bits))
	}
	return out, nil
}

// parseAddrList parses a wg-quick IP-address list (DNS field).
func parseAddrList(v string) ([]netip.Addr, error) {
	tokens := splitListField(v)
	if len(tokens) == 0 {
		return nil, nil
	}
	out := make([]netip.Addr, 0, len(tokens))
	for _, t := range tokens {
		addr, err := netip.ParseAddr(t)
		if err != nil {
			return nil, fmt.Errorf("invalid IP address %q", t)
		}
		out = append(out, addr)
	}
	return out, nil
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
