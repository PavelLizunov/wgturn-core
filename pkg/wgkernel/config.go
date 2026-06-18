// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgkernel

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// DefaultMTU is the conservative MTU used when Config.MTU is zero.
// 1280 is the WireGuard recommendation when the carrier itself is a
// tunnel (DTLS over TURN over the public internet); it leaves enough
// headroom for the encapsulation chain.
const DefaultMTU = 1280

// MaxMTU is the largest interface MTU wgturn-core will hand to the TUN
// device. A TUN packet of M bytes becomes M+~109 on the wire once the
// WireGuard data header+tag (~32), DTLS record+CID (~45), TURN ChannelData
// (4) and UDP/IP (28) encapsulation are added; above this a full-size packet
// exceeds a 1500-byte path and is IP-fragmented or dropped on the carrier.
// Larger configured values are clamped down to this ceiling rather than
// silently overflowing. Paths below 1500 (some mobile carriers) still want
// the 1280 default or lower set explicitly.
const MaxMTU = 1380

// clampMTU resolves the effective TUN MTU: DefaultMTU when unset/non-positive,
// and never above MaxMTU so an over-large configured value cannot overflow the
// DTLS-over-TURN carrier path.
func clampMTU(mtu int) int {
	if mtu <= 0 {
		return DefaultMTU
	}
	if mtu > MaxMTU {
		return MaxMTU
	}
	return mtu
}

// Config describes one WireGuard interface. Field semantics map to
// the wg-quick `[Interface]` and `[Peer]` sections; we omit the
// platform-specific bits (PostUp, Table, FwMark) since this kernel
// runs entirely in userspace and leaves OS-level routing to the host.
type Config struct {
	// PrivateKey is the WireGuard private key. Accepted formats:
	// 64-character lowercase hex OR 44-character base64 (the
	// wg-quick `.conf` format). Required.
	PrivateKey string

	// Address is the list of IP CIDRs the local end of the tunnel
	// claims. wgkernel does NOT configure these on the host — that
	// is the caller's job (typically via the platform's TUN setup).
	// Stored here purely for diagnostics and so the host code can
	// look them up from one place.
	Address []netip.Prefix

	// ListenPort is the UDP port the WG bind listens on. Zero means
	// "let the kernel pick". When using WithTurnTunnel, the actual
	// port matters very little since the only traffic to/from this
	// port is the carrier loopback.
	ListenPort uint16

	// MTU defaults to DefaultMTU when zero.
	MTU int

	// DNS servers are recorded for the host's TUN setup. wgkernel
	// itself does NOT operate a resolver — the host controls
	// routing and DNS. This is informational metadata.
	DNS []netip.Addr

	// Peers is the list of remote WireGuard peers.
	Peers []PeerConfig
}

// PeerConfig is the wg-quick `[Peer]` section in a typed form.
type PeerConfig struct {
	// PublicKey of the remote peer. Hex (64 chars) or base64 (44).
	// Required.
	PublicKey string

	// PresharedKey, optional, for an extra symmetric layer. Hex or
	// base64.
	PresharedKey string

	// Endpoint is the remote IP:port. May be empty for "reactive"
	// peers (server-side, which only learns endpoints from
	// successful handshakes). For TURN-tunneled deployments,
	// WithTurnTunnel rewrites this to point at the local hub.
	Endpoint string

	// AllowedIPs is the set of CIDRs whose packets should be sent
	// to this peer. The host's routing table mirrors this list.
	AllowedIPs []netip.Prefix

	// PersistentKeepalive sends a heartbeat every interval.
	// Required when the peer is behind NAT and the local side is
	// the initiator.
	PersistentKeepalive time.Duration
}

// Validate runs basic sanity checks. Returns the first failure.
func (c Config) Validate() error {
	if _, err := decodeWGKey(c.PrivateKey); err != nil {
		return fmt.Errorf("PrivateKey: %w", err)
	}
	if c.MTU < 0 {
		return fmt.Errorf("MTU must be >= 0 (zero defaults to %d)", DefaultMTU)
	}
	for i, p := range c.Peers {
		if _, err := decodeWGKey(p.PublicKey); err != nil {
			return fmt.Errorf("peer %d PublicKey: %w", i, err)
		}
		if p.PresharedKey != "" {
			if _, err := decodeWGKey(p.PresharedKey); err != nil {
				return fmt.Errorf("peer %d PresharedKey: %w", i, err)
			}
		}
		for j, a := range p.AllowedIPs {
			if !a.IsValid() {
				return fmt.Errorf("peer %d AllowedIPs[%d] invalid", i, j)
			}
		}
		if p.PersistentKeepalive < 0 {
			return fmt.Errorf("peer %d PersistentKeepalive must be >= 0", i)
		}
	}
	return nil
}

// withDefaults returns a copy of c with zero-valued fields filled in.
func (c Config) withDefaults() Config {
	if c.MTU == 0 {
		c.MTU = DefaultMTU
	}
	return c
}

// IPC builds the wireguard-go IpcSet payload for this Config. Format:
//
//	private_key=<hex>
//	listen_port=<int>
//	replace_peers=true
//	public_key=<hex>          # for each peer
//	preshared_key=<hex>
//	endpoint=<ip:port>
//	persistent_keepalive_interval=<seconds>
//	replace_allowed_ips=true
//	allowed_ip=<cidr>
//
// One key=value per line, separated by '\n'. Trailing newline is
// optional; wireguard-go is lenient.
func (c Config) IPC() (string, error) {
	priv, err := decodeWGKey(c.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("PrivateKey: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", hex.EncodeToString(priv))
	fmt.Fprintf(&b, "listen_port=%d\n", c.ListenPort)
	b.WriteString("replace_peers=true\n")

	for i, p := range c.Peers {
		pub, err := decodeWGKey(p.PublicKey)
		if err != nil {
			return "", fmt.Errorf("peer %d PublicKey: %w", i, err)
		}
		fmt.Fprintf(&b, "public_key=%s\n", hex.EncodeToString(pub))
		if p.PresharedKey != "" {
			psk, err := decodeWGKey(p.PresharedKey)
			if err != nil {
				return "", fmt.Errorf("peer %d PresharedKey: %w", i, err)
			}
			fmt.Fprintf(&b, "preshared_key=%s\n", hex.EncodeToString(psk))
		}
		if p.Endpoint != "" {
			fmt.Fprintf(&b, "endpoint=%s\n", p.Endpoint)
		}
		if p.PersistentKeepalive > 0 {
			fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n",
				int(p.PersistentKeepalive.Seconds()))
		}
		if len(p.AllowedIPs) > 0 {
			b.WriteString("replace_allowed_ips=true\n")
			for _, a := range p.AllowedIPs {
				fmt.Fprintf(&b, "allowed_ip=%s\n", a.String())
			}
		}
	}
	return b.String(), nil
}

// decodeWGKey accepts a 32-byte WireGuard key in either hex (64 chars)
// or base64 (44 chars, the wg-quick convention) form. Returns the raw
// 32-byte key on success.
func decodeWGKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty key")
	}
	switch len(s) {
	case 64: // hex
		k, err := hex.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("hex decode: %w", err)
		}
		if len(k) != 32 {
			return nil, fmt.Errorf("hex key wrong length: %d", len(k))
		}
		return k, nil
	case 44: // base64 std (incl. trailing '=')
		k, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("base64 decode: %w", err)
		}
		if len(k) != 32 {
			return nil, fmt.Errorf("base64 key wrong length: %d", len(k))
		}
		return k, nil
	default:
		return nil, fmt.Errorf("key must be 64-char hex or 44-char base64, got %d chars", len(s))
	}
}

// encodeWGKeyBase64 is the inverse of decodeWGKey for tests / display.
// (Not used internally — kept available for callers building keys from
// raw bytes.)
func encodeWGKeyBase64(raw []byte) string {
	return base64.StdEncoding.EncodeToString(raw)
}
