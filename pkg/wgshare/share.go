// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgshare

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Scheme is the URL scheme of a wgturn share URL. Embedders that
// surface multiple share formats (e.g. plain wireguard://, vless://,
// wgturn://) use this constant for dispatch.
const Scheme = "wgturn"

// formatVersion is bumped only on incompatible wireFormat changes.
// Old binaries fail Parse with a "unsupported version" error rather
// than silently misinterpreting fields.
const formatVersion = 1

// ErrInvalidURL is returned by Parse when the input does not match
// the wgturn share-URL grammar. Callers can errors.Is it for setup-
// time error handling.
var ErrInvalidURL = errors.New("wgshare: invalid share URL")

// Profile is the typed representation of a share URL. Constructors
// (server-side: pkg/wgadmin; tests: literal struct) populate it;
// Encode turns it back into a URL string; Parse decodes one.
type Profile struct {
	// Label is the free-form identifier shown after `#` in the URL
	// (e.g. user name, device name). Survives round-trips. Optional.
	Label string

	// ServerPublicKey is the server-side WireGuard public key the
	// client peers with. base64, 44 chars.
	ServerPublicKey string

	// ClientPrivateKey is the freshly generated client-side
	// WireGuard private key. base64, 44 chars. The server has the
	// matching public key in its [Peer] entry.
	ClientPrivateKey string

	// PresharedKey is the optional symmetric pre-shared key. base64,
	// 44 chars. Empty string when no PSK is in use.
	PresharedKey string

	// Endpoint is the wgturn DTLS listener address in "host:port"
	// form. e.g. "is-01.example.com:56000". This is NOT the
	// underlying WireGuard endpoint — that is replaced at runtime
	// with the local wgturn hub's listener via wgkernel.WithTurnTunnel.
	Endpoint string

	// Address is the assigned tunnel address for this client, as a
	// CIDR (e.g. 10.7.0.5/24). The host bits identify the client;
	// the prefix length matches the server's subnet so the kernel
	// installs the connected route correctly.
	Address netip.Prefix

	// AllowedIPs is the set of CIDRs whose packets should route to
	// the server (typically [0.0.0.0/0, ::/0] for "send everything
	// through the tunnel"). Required.
	AllowedIPs []netip.Prefix

	// DNS is the optional list of recommended resolvers. Embedders
	// configure host DNS themselves; wgkernel doesn't.
	DNS []netip.Addr

	// MTU is the WireGuard interface MTU. Zero means "use wgkernel
	// default". Typical: 1280.
	MTU int

	// PersistentKeepalive is the WG heartbeat interval. Zero
	// disables. Typical: 25 seconds — keeps NAT mappings alive on
	// the carrier path.
	PersistentKeepalive time.Duration
}

// wireFormat is the on-the-wire JSON shape. Field tags are short to
// keep URLs compact (every saved character is one less to copy/paste
// on a phone). Renaming fields is a breaking change; bump
// formatVersion.
type wireFormat struct {
	V   int      `json:"v"`
	SP  string   `json:"sp"`
	CP  string   `json:"cp"`
	PSK string   `json:"psk,omitempty"`
	EP  string   `json:"ep"`
	AD  string   `json:"ad"`
	AI  []string `json:"ai,omitempty"`
	DNS []string `json:"dns,omitempty"`
	MTU int      `json:"mtu,omitempty"`
	KA  int      `json:"ka,omitempty"` // keepalive seconds
}

// Validate returns nil iff p has the minimum fields a Profile needs
// to be useful: server pubkey, client privkey, endpoint, address.
// AllowedIPs is recommended but defaults to [0.0.0.0/0] in
// ToKernelConfig when empty.
func (p Profile) Validate() error {
	if p.ServerPublicKey == "" {
		return fmt.Errorf("%w: ServerPublicKey is required", ErrInvalidURL)
	}
	if p.ClientPrivateKey == "" {
		return fmt.Errorf("%w: ClientPrivateKey is required", ErrInvalidURL)
	}
	if p.Endpoint == "" {
		return fmt.Errorf("%w: Endpoint is required", ErrInvalidURL)
	}
	if !p.Address.IsValid() {
		return fmt.Errorf("%w: Address is required", ErrInvalidURL)
	}
	return nil
}

// Encode serialises p into a wgturn:// URL. Returns an error if p
// fails Validate; callers can errors.Is(ErrInvalidURL).
func (p Profile) Encode() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	w := wireFormat{
		V:   formatVersion,
		SP:  p.ServerPublicKey,
		CP:  p.ClientPrivateKey,
		PSK: p.PresharedKey,
		EP:  p.Endpoint,
		AD:  p.Address.String(),
		MTU: p.MTU,
		KA:  int(p.PersistentKeepalive / time.Second),
	}
	for _, a := range p.AllowedIPs {
		w.AI = append(w.AI, a.String())
	}
	for _, d := range p.DNS {
		w.DNS = append(w.DNS, d.String())
	}
	js, err := json.Marshal(w)
	if err != nil {
		return "", fmt.Errorf("wgshare: marshal: %w", err)
	}
	out := Scheme + "://" + base64.RawURLEncoding.EncodeToString(js)
	if p.Label != "" {
		out += "#" + url.QueryEscape(p.Label)
	}
	return out, nil
}

// Parse decodes a wgturn:// URL into a Profile. Malformed input,
// unsupported versions, or invalid base64 / JSON / CIDRs all surface
// as errors wrapping ErrInvalidURL.
func Parse(s string) (Profile, error) {
	rest, ok := strings.CutPrefix(s, Scheme+"://")
	if !ok {
		return Profile{}, fmt.Errorf("%w: missing %q scheme", ErrInvalidURL, Scheme+"://")
	}

	var label string
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		raw := rest[i+1:]
		dec, err := url.QueryUnescape(raw)
		if err != nil {
			// Fragment failed to decode — keep the raw form rather
			// than refuse the whole URL. Labels are cosmetic.
			label = raw
		} else {
			label = dec
		}
		rest = rest[:i]
	}
	if rest == "" {
		return Profile{}, fmt.Errorf("%w: empty payload", ErrInvalidURL)
	}

	js, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil {
		return Profile{}, fmt.Errorf("%w: base64 decode: %w", ErrInvalidURL, err)
	}
	var w wireFormat
	if err := json.Unmarshal(js, &w); err != nil {
		return Profile{}, fmt.Errorf("%w: json: %w", ErrInvalidURL, err)
	}
	if w.V != formatVersion {
		return Profile{}, fmt.Errorf("%w: unsupported version %d (this binary handles v%d)", ErrInvalidURL, w.V, formatVersion)
	}

	p := Profile{
		Label:               label,
		ServerPublicKey:     w.SP,
		ClientPrivateKey:    w.CP,
		PresharedKey:        w.PSK,
		Endpoint:            w.EP,
		MTU:                 w.MTU,
		PersistentKeepalive: time.Duration(w.KA) * time.Second,
	}
	if w.AD != "" {
		ad, err := netip.ParsePrefix(w.AD)
		if err != nil {
			return Profile{}, fmt.Errorf("%w: address %q: %w", ErrInvalidURL, w.AD, err)
		}
		p.Address = ad
	}
	for _, a := range w.AI {
		pp, err := netip.ParsePrefix(a)
		if err != nil {
			return Profile{}, fmt.Errorf("%w: AllowedIPs %q: %w", ErrInvalidURL, a, err)
		}
		p.AllowedIPs = append(p.AllowedIPs, pp)
	}
	for _, d := range w.DNS {
		addr, err := netip.ParseAddr(d)
		if err != nil {
			return Profile{}, fmt.Errorf("%w: DNS %q: %w", ErrInvalidURL, d, err)
		}
		p.DNS = append(p.DNS, addr)
	}
	if err := p.Validate(); err != nil {
		return Profile{}, err
	}
	return p, nil
}
