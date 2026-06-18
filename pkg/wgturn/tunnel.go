// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturn

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/PavelLizunov/wgturn-core/internal/proxy"
)

// Tunnel is one running wgturn instance. Construct with New, then call
// Start once and Stop once. A Tunnel is single-use; create a new one to
// restart.
type Tunnel struct {
	cfg Config

	mu     sync.Mutex
	hub    *proxy.Hub
	cancel context.CancelFunc
	state  state
}

type state int

const (
	stateNew state = iota
	stateStarted
	stateStopped
)

// New constructs a Tunnel from a Config. Validate is called; on error it
// is wrapped with %w so callers can errors.Is(err, ErrInvalidConfig).
func New(cfg Config) (*Tunnel, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Tunnel{cfg: cfg.withDefaults()}, nil
}

// Start brings up the streams and returns once at least one stream is
// ready (i.e. has a working TURN allocation and, for proxy_v* modes, a
// completed DTLS handshake). It blocks for at most StartTimeout (default
// 30s; override via Tunnel.Start with a deadline-bearing context).
//
// Start is non-idempotent: calling it twice on the same Tunnel returns
// ErrAlreadyStarted.
//
// The context governs the lifetime of all background goroutines: when
// ctx is cancelled or Tunnel.Stop is called, every stream is torn down.
func (t *Tunnel) Start(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != stateNew {
		return ErrAlreadyStarted
	}

	hubCtx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel

	// Resolve hints: Hints wins if set, else fall back to single Hint.
	hints := t.cfg.Hints
	if len(hints) == 0 && t.cfg.Hint != "" {
		hints = []string{t.cfg.Hint}
	}

	hubCfg := proxy.HubConfig{
		PeerAddr:         t.cfg.PeerAddr,
		ListenAddr:       t.cfg.ListenAddr,
		Streams:          t.cfg.Streams,
		PeerType:         string(t.cfg.PeerType),
		UDP:              t.cfg.UDP,
		TURNHostOverride: t.cfg.TURNHostOverride,
		TURNPortOverride: t.cfg.TURNPortOverride,
		StreamsPerCred:   t.cfg.StreamsPerCred,
		WatchdogTimeout:  t.cfg.WatchdogTimeout,
		Hints:            hints,
		Provider:         providerAdapter{t.cfg.Provider},
		Protector:        ControlFunc(t.cfg.Protector),
		Logger:           loggerAdapter{t.cfg.Logger},
	}

	hub, err := proxy.NewHub(hubCfg)
	if err != nil {
		cancel()
		return fmt.Errorf("wgturn: build hub: %w", err)
	}

	// Start hub asynchronously, wait up to StartTimeout for the first
	// "ready" signal.
	startDeadline := 30 * time.Second
	startCtx, startCancel := context.WithTimeout(ctx, startDeadline)
	defer startCancel()

	if err := hub.Start(hubCtx); err != nil {
		cancel()
		return fmt.Errorf("wgturn: start hub: %w", err)
	}

	select {
	case <-hub.Ready():
		// at least one stream is up
	case <-startCtx.Done():
		_ = hub.Stop()
		cancel()
		if startCtx.Err() == context.DeadlineExceeded {
			return ErrStartTimeout
		}
		return startCtx.Err()
	}

	t.hub = hub
	t.state = stateStarted
	return nil
}

// Stop tears down the Tunnel, cancelling all in-flight work. Subsequent
// calls are no-ops. Stop is safe to call from any goroutine, including
// from inside a Logger callback.
func (t *Tunnel) Stop() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != stateStarted {
		return nil
	}
	t.state = stateStopped
	if t.cancel != nil {
		t.cancel()
	}
	if t.hub != nil {
		return t.hub.Stop()
	}
	return nil
}

// LocalAddr returns the actual UDP address the Tunnel's local listener
// is bound to. Useful when ListenAddr in Config used port 0 (kernel
// chooses). Returns nil before Start.
func (t *Tunnel) LocalAddr() net.Addr {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.hub == nil {
		return nil
	}
	return t.hub.LocalAddr()
}

// Stats returns a snapshot of runtime counters. Returns ErrNotStarted
// before Start.
func (t *Tunnel) Stats() (Stats, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != stateStarted || t.hub == nil {
		return Stats{}, ErrNotStarted
	}
	hs := t.hub.Stats()
	return Stats{
		StreamsRunning: hs.StreamsRunning,
		StreamsTotal:   hs.StreamsTotal,
		BytesTx:        hs.BytesTx,
		BytesRx:        hs.BytesRx,
		PacketsTx:      hs.PacketsTx,
		PacketsRx:      hs.PacketsRx,
		DropsTx:        hs.DropsTx,
		ErrorsTx:       hs.ErrorsTx,
		ErrorsRx:       hs.ErrorsRx,
	}, nil
}

// Healthy reports whether at least min streams are currently running.
//
// Start returns as soon as ONE stream is up — the carrier is usable
// immediately and the rest allocate in the background — so a running Tunnel
// can be silently degraded (e.g. 1 of 16 streams up, the other 15 looping on
// allocation failure). Healthy surfaces that: an embedder can poll
// Healthy(want) to distinguish a fully-up tunnel from a barely-alive one and
// react (alert, re-provision, widen the call-link pool). min <= 0 means "at
// least one stream". Returns false before Start / after Stop.
func (t *Tunnel) Healthy(min int) bool {
	s, err := t.Stats()
	if err != nil {
		return false
	}
	if min <= 0 {
		min = 1
	}
	return s.StreamsRunning >= min
}

// providerAdapter bridges the public CredentialsProvider into the
// internal proxy package's narrower view.
type providerAdapter struct {
	p CredentialsProvider
}

func (a providerAdapter) Fetch(ctx context.Context, hint string, streamID int) (proxy.Credentials, error) {
	c, err := a.p.Fetch(ctx, hint, streamID)
	if err != nil {
		return proxy.Credentials{}, err
	}
	return proxy.Credentials{
		Username:   c.Username,
		Password:   c.Password,
		ServerAddr: c.ServerAddr,
		ExpiresIn:  c.ExpiresIn,
	}, nil
}

// loggerAdapter adapts the public Logger to internal/proxy's logger contract.
type loggerAdapter struct{ l Logger }

func (a loggerAdapter) Debugf(f string, args ...any) { a.l.Debugf(f, args...) }
func (a loggerAdapter) Infof(f string, args ...any)  { a.l.Infof(f, args...) }
func (a loggerAdapter) Warnf(f string, args ...any)  { a.l.Warnf(f, args...) }
func (a loggerAdapter) Errorf(f string, args ...any) { a.l.Errorf(f, args...) }
