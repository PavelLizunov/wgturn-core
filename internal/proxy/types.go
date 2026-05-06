// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// Package proxy implements the TURN proxy hub: N parallel streams that
// each maintain a TURN allocation (optionally wrapped in DTLS) and
// relay UDP between a local listener and a remote peer through the
// allocation.
//
// This package is internal: the public API lives in pkg/wgturn.
package proxy

import (
	"context"
	"syscall"
	"time"
)

// Credentials is the proxy package's view of TURN credentials. It mirrors
// the public wgturn.Credentials but intentionally lives here so the
// internal package has no upward dependency on the public one.
type Credentials struct {
	Username   string
	Password   string
	ServerAddr string
	ExpiresIn  time.Duration
}

// Provider is what the Hub needs from the outside world: a way to fetch
// fresh TURN credentials on demand.
type Provider interface {
	Fetch(ctx context.Context, hint string, streamID int) (Credentials, error)
}

// Logger is the proxy package's view of a logger.
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// ControlFunc is the *net.Dialer.Control / .ListenConfig.Control type:
// invoked once per opened socket so the embedder can call e.g.
// VpnService.protect(fd) on Android.
type ControlFunc func(network, address string, c syscall.RawConn) error

// HubConfig is the internal-package counterpart of wgturn.Config: it has
// concrete primitive types only, with the public wgturn package
// performing all defaulting and adapter wiring before reaching here.
type HubConfig struct {
	PeerAddr         string
	ListenAddr       string
	Streams          int
	PeerType         string // "proxy_v2" | "proxy_v1" | "wireguard"
	UDP              bool
	TURNHostOverride string
	TURNPortOverride int
	StreamsPerCred   int
	WatchdogTimeout  time.Duration

	// Hints is the round-robin pool of provider hints. Each cred-group
	// (StreamsPerCred streams) gets `Hints[groupID % len(Hints)]`.
	// Empty list is fine for ModeStub-style providers that ignore the
	// hint anyway. Single-element list reproduces the legacy single-
	// Hint behaviour.
	Hints []string

	Provider  Provider
	Protector ControlFunc
	Logger    Logger
}

// HubStats is the internal-package counterpart of wgturn.Stats.
type HubStats struct {
	StreamsRunning int
	StreamsTotal   int
	BytesTx        uint64
	BytesRx        uint64
	PacketsTx      uint64
	PacketsRx      uint64
	DropsTx        uint64
	ErrorsTx       uint64
	ErrorsRx       uint64
}
