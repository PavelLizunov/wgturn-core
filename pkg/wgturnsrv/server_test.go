// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturnsrv_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/PavelLizunov/wgturn-core/pkg/wgturnsrv"
)

// TestServer_StartStop confirms the lifecycle invariants of S2:
// New succeeds with a valid Config, Start binds a real UDP listener
// (LocalAddr is non-nil and resolvable), Stop drains every spawned
// goroutine without blocking, and a second Start refuses to come up.
func TestServer_StartStop(t *testing.T) {
	srv, err := wgturnsrv.New(wgturnsrv.Config{
		ListenAddr: "127.0.0.1:0",
		Backend:    wgturnsrv.UDPBackend{Addr: "127.0.0.1:1"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	addr := srv.LocalAddr()
	if addr == nil {
		t.Fatal("LocalAddr after Start = nil")
	}
	if _, _, err := net.SplitHostPort(addr.String()); err != nil {
		t.Errorf("LocalAddr %q is not host:port: %v", addr, err)
	}

	stats, err := srv.Stats()
	if err != nil {
		t.Errorf("Stats after Start: unexpected %v", err)
	}
	if stats.SessionsActive != 0 || stats.StreamsActive != 0 {
		t.Errorf("Stats = %+v, want zero counters", stats)
	}

	if err := srv.Start(ctx); !errors.Is(err, wgturnsrv.ErrAlreadyStarted) {
		t.Errorf("second Start returned %v, want ErrAlreadyStarted", err)
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- srv.Stop() }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Errorf("Stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return within 5s")
	}

	// Idempotency: Stop after Stop is a no-op.
	if err := srv.Stop(); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

// TestServer_StatsBeforeStart returns ErrNotStarted instead of a
// zero-value Stats so callers can distinguish "fresh server" from
// "running but empty".
func TestServer_StatsBeforeStart(t *testing.T) {
	srv, err := wgturnsrv.New(wgturnsrv.Config{
		ListenAddr: "127.0.0.1:0",
		Backend:    wgturnsrv.UDPBackend{Addr: "127.0.0.1:1"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := srv.Stats(); !errors.Is(err, wgturnsrv.ErrNotStarted) {
		t.Errorf("Stats before Start = %v, want ErrNotStarted", err)
	}
	if got := srv.LocalAddr(); got != nil {
		t.Errorf("LocalAddr before Start = %v, want nil", got)
	}
}

// TestServer_ContextCancelStops verifies that the ctx passed to Start
// is the lifetime cap: cancelling it tears down the accept loop without
// requiring an explicit Stop call. Stop afterwards must still be a
// no-op.
func TestServer_ContextCancelStops(t *testing.T) {
	srv, err := wgturnsrv.New(wgturnsrv.Config{
		ListenAddr: "127.0.0.1:0",
		Backend:    wgturnsrv.UDPBackend{Addr: "127.0.0.1:1"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	cancel()
	stopDone := make(chan error, 1)
	go func() { stopDone <- srv.Stop() }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Errorf("Stop after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop after cancel did not return within 5s")
	}
}

// TestNew_ValidatesConfig rejects Configs missing ListenAddr or
// Backend with ErrInvalidConfig in the chain. Catching this at New
// keeps boot-time misconfiguration loud.
func TestNew_ValidatesConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  wgturnsrv.Config
	}{
		{
			name: "missing ListenAddr",
			cfg:  wgturnsrv.Config{Backend: wgturnsrv.UDPBackend{Addr: "127.0.0.1:1"}},
		},
		{
			name: "missing Backend",
			cfg:  wgturnsrv.Config{ListenAddr: "127.0.0.1:0"},
		},
		{
			name: "both missing",
			cfg:  wgturnsrv.Config{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := wgturnsrv.New(tc.cfg)
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if !errors.Is(err, wgturnsrv.ErrInvalidConfig) {
				t.Errorf("err = %v, want chain containing ErrInvalidConfig", err)
			}
		})
	}
}

// TestNew_AppliesDefaults makes sure timeouts default to the Default*
// constants when the caller leaves them zero. Indirect check: the
// server starts and stops cleanly with only ListenAddr+Backend set,
// which would fail if a zero StreamReadTimeout caused immediate
// deadline expiry inside the demuxer (S3) — but it's harmless to
// pin the contract early.
func TestNew_AppliesDefaults(t *testing.T) {
	srv, err := wgturnsrv.New(wgturnsrv.Config{
		ListenAddr: "127.0.0.1:0",
		Backend:    wgturnsrv.UDPBackend{Addr: "127.0.0.1:1"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := srv.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
}
