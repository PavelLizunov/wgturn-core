// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturn

import (
	"errors"
	"fmt"
	"net"
	"time"
)

// PeerType selects how the tunnel frames packets between the client and
// the wgturn server. The wire format must match the server-side
// implementation; see kiper292/vk-turn-proxy server for reference.
type PeerType string

const (
	// PeerTypeProxyV2 wraps each stream in DTLS and prefixes a 17-byte
	// session-id (16 byte UUID) + stream-id (1 byte) header on the first
	// post-handshake packet so the server can aggregate parallel streams
	// of one client into a single backend UDP connection. This is the
	// default and the only mode that supports multi-user servers.
	PeerTypeProxyV2 PeerType = "proxy_v2"

	// PeerTypeProxyV1 wraps each stream in DTLS but does not send the
	// session-id handshake. Each stream becomes its own backend UDP
	// connection on the server. Kept for backwards compatibility with
	// older deployments.
	PeerTypeProxyV1 PeerType = "proxy_v1"

	// PeerTypeWireGuard sends raw WireGuard packets through the TURN
	// relay with no DTLS wrapping. Easier to detect, may attract bans,
	// but useful for debugging and cases where the server is a vanilla
	// WireGuard endpoint reachable via TURN only.
	PeerTypeWireGuard PeerType = "wireguard"
)

// Mode selects which CredentialsProvider mode the Tunnel runs in. It is
// purely informational at the Tunnel level; the provider itself decides
// what link / hint string means.
type Mode string

const (
	// ModeVKLink uses VK Calls anonymous tokens derived from a call link.
	ModeVKLink Mode = "vk_link"

	// ModeWB uses the WB Stream API (alternative TURN credentials source).
	ModeWB Mode = "wb"

	// ModeStub is a built-in mode for testing; the Tunnel will accept
	// whatever the provider returns without semantic interpretation.
	ModeStub Mode = "stub"
)

// Config controls Tunnel construction. All required fields must be set;
// see Validate() for the complete contract.
type Config struct {
	// PeerAddr is the UDP host:port of the wgturn server (the matching
	// counterpart of this client). Required.
	PeerAddr string

	// ListenAddr is the local UDP listen address. WireGuard (or any other
	// UDP consumer) is pointed here. Required.
	ListenAddr string

	// Streams is the number of parallel TURN streams. Must be >= 1.
	// Recommended: 4 for VK-based providers (load balancing + failover);
	// 1 for stub testing or when a provider rate-limits aggressively.
	Streams int

	// PeerType selects framing. Defaults to PeerTypeProxyV2 if zero.
	PeerType PeerType

	// Mode is the operating mode label. Defaults to ModeStub if zero.
	Mode Mode

	// Hint is a free-form string passed to CredentialsProvider.Fetch.
	// For ModeVKLink it is the full https://vk.com/call/join/<id> URL.
	// For ModeWB it is provider-specific. For ModeStub it is unused.
	//
	// If Hints is non-empty it takes precedence over Hint.
	Hint string

	// Hints is the multi-source variant of Hint: each cred-group of
	// streams (StreamsPerCred streams share one group) gets the next
	// hint in round-robin order, so a group of 16 streams across 4
	// hints fans out to 4 independent provider sessions.
	//
	// For ModeVKLink this lets you spread streams across multiple VK
	// call invite links — each link allocates on (potentially) a
	// different VK TURN host, multiplying the per-call bandwidth
	// shaping. Empirically with 4 distinct links × 4 streams each we
	// see ~3-4× throughput vs a single link with 16 streams.
	//
	// Empty Hints + non-empty Hint behaves exactly like the old
	// single-hint API. Both empty is fine for ModeStub.
	Hints []string

	// UDP forces TURN transport to UDP. Default false (TCP).
	UDP bool

	// TURNHostOverride, if non-empty, overrides the host portion of the
	// TURN server address returned by the provider. Useful when a
	// provider's TURN selection algorithm picks a poor IP and you have a
	// known-good one.
	TURNHostOverride string

	// TURNPortOverride overrides the port portion of the provider-supplied
	// TURN address (zero means: use what the provider returned).
	TURNPortOverride int

	// StreamsPerCred is how many streams share a single cached
	// CredentialsProvider response. Must be >= 1; defaults to 4.
	// Higher values reduce calls to the provider at the cost of all
	// streams in the group failing together if the credentials become
	// invalid. The same value MUST be configured on the server.
	StreamsPerCred int

	// WatchdogTimeout aborts and reconnects a stream that has not received
	// any RX bytes for this duration. Zero disables the watchdog.
	WatchdogTimeout time.Duration

	// Provider supplies TURN credentials. Required.
	Provider CredentialsProvider

	// Protector is invoked for every outgoing socket so the carrier
	// traffic can be excluded from a host VPN routing table. Required;
	// pass NoopProtector{} on platforms where this is not needed.
	Protector SocketProtector

	// Logger receives structured log messages. Optional; NoopLogger is
	// used when nil.
	Logger Logger
}

// ErrInvalidConfig is returned by Validate when the Config is malformed.
var ErrInvalidConfig = errors.New("wgturn: invalid config")

// Validate checks the Config and returns ErrInvalidConfig (wrapped with a
// descriptive message) on the first problem found.
//
// Validate has a value receiver so callers can validate temporary values
// inline (e.g. wgturn.Config{...}.Validate()).
func (c Config) Validate() error {
	if c.PeerAddr == "" {
		return fmt.Errorf("%w: PeerAddr is required", ErrInvalidConfig)
	}
	if _, _, err := net.SplitHostPort(c.PeerAddr); err != nil {
		return fmt.Errorf("%w: PeerAddr %q: %s", ErrInvalidConfig, c.PeerAddr, err.Error())
	}
	if c.ListenAddr == "" {
		return fmt.Errorf("%w: ListenAddr is required", ErrInvalidConfig)
	}
	if _, _, err := net.SplitHostPort(c.ListenAddr); err != nil {
		return fmt.Errorf("%w: ListenAddr %q: %s", ErrInvalidConfig, c.ListenAddr, err.Error())
	}
	if c.Streams < 0 {
		return fmt.Errorf("%w: Streams must be >= 0 (zero defaults to 1)", ErrInvalidConfig)
	}
	if c.StreamsPerCred < 0 {
		return fmt.Errorf("%w: StreamsPerCred must be >= 0 (zero defaults to 4)", ErrInvalidConfig)
	}
	if c.WatchdogTimeout < 0 {
		return fmt.Errorf("%w: WatchdogTimeout must be >= 0", ErrInvalidConfig)
	}
	if c.Provider == nil {
		return fmt.Errorf("%w: Provider is required", ErrInvalidConfig)
	}
	if c.Protector == nil {
		return fmt.Errorf("%w: Protector is required (pass NoopProtector{})", ErrInvalidConfig)
	}
	switch c.PeerType {
	case "", PeerTypeProxyV2, PeerTypeProxyV1, PeerTypeWireGuard:
		// ok
	default:
		return fmt.Errorf("%w: unknown PeerType %q", ErrInvalidConfig, c.PeerType)
	}
	return nil
}

// withDefaults returns a copy of c with zero-valued fields filled in.
func (c Config) withDefaults() Config {
	if c.Streams == 0 {
		c.Streams = 1
	}
	if c.StreamsPerCred == 0 {
		c.StreamsPerCred = 4
	}
	if c.PeerType == "" {
		c.PeerType = PeerTypeProxyV2
	}
	if c.Mode == "" {
		c.Mode = ModeStub
	}
	if c.Logger == nil {
		c.Logger = NoopLogger{}
	}
	return c
}
