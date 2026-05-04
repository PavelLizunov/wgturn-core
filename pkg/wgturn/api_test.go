// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturn_test

import (
	"errors"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/slovn/wgturn-core/pkg/wgturn"
	"github.com/slovn/wgturn-core/pkg/wgturn/provider/stub"
)

// --- Config validation -----------------------------------------------------

func validBaseConfig() wgturn.Config {
	return wgturn.Config{
		PeerAddr:   "1.2.3.4:56000",
		ListenAddr: "127.0.0.1:9000",
		Streams:    1,
		Provider:   stub.New("u", "p", "turn:3478"),
		Protector:  wgturn.NoopProtector{},
	}
}

func TestConfigValidate_HappyPath(t *testing.T) {
	if err := validBaseConfig().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestConfigValidate_RequiredFields(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*wgturn.Config)
	}{
		{"missing peer", func(c *wgturn.Config) { c.PeerAddr = "" }},
		{"bad peer", func(c *wgturn.Config) { c.PeerAddr = "no-port" }},
		{"missing listen", func(c *wgturn.Config) { c.ListenAddr = "" }},
		{"bad listen", func(c *wgturn.Config) { c.ListenAddr = "no-port" }},
		{"missing provider", func(c *wgturn.Config) { c.Provider = nil }},
		{"missing protector", func(c *wgturn.Config) { c.Protector = nil }},
		{"unknown peer type", func(c *wgturn.Config) { c.PeerType = "exotic" }},
		{"negative streams", func(c *wgturn.Config) { c.Streams = -1 }},
		{"negative streamsPerCred", func(c *wgturn.Config) { c.StreamsPerCred = -1 }},
		{"negative watchdog", func(c *wgturn.Config) { c.WatchdogTimeout = -time.Second }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validBaseConfig()
			tc.mut(&c)
			err := c.Validate()
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !errors.Is(err, wgturn.ErrInvalidConfig) {
				t.Errorf("err = %v, want wrapping ErrInvalidConfig", err)
			}
		})
	}
}

func TestNew_RejectsBadConfig(t *testing.T) {
	_, err := wgturn.New(wgturn.Config{})
	if err == nil || !errors.Is(err, wgturn.ErrInvalidConfig) {
		t.Errorf("New: err = %v", err)
	}
}

// --- SocketProtector -------------------------------------------------------

func TestNoopProtector(t *testing.T) {
	if err := (wgturn.NoopProtector{}).Protect(42); err != nil {
		t.Errorf("noop returned %v", err)
	}
}

func TestFuncProtector(t *testing.T) {
	var seen uintptr
	p := wgturn.FuncProtector(func(fd uintptr) error {
		seen = fd
		return nil
	})
	if err := p.Protect(99); err != nil {
		t.Fatal(err)
	}
	if seen != 99 {
		t.Errorf("seen = %d", seen)
	}
}

// fakeRawConn is the smallest syscall.RawConn that hands a single FD to its
// Control closure. We need this to exercise ControlFunc end-to-end.
type fakeRawConn struct{ fd uintptr }

func (f *fakeRawConn) Control(fn func(uintptr)) error { fn(f.fd); return nil }
func (f *fakeRawConn) Read(_ func(uintptr) bool) error {
	return errors.New("not implemented")
}
func (f *fakeRawConn) Write(_ func(uintptr) bool) error {
	return errors.New("not implemented")
}

func TestControlFunc_InvokesProtector(t *testing.T) {
	var got uintptr
	p := wgturn.FuncProtector(func(fd uintptr) error {
		got = fd
		return nil
	})
	cf := wgturn.ControlFunc(p)
	if err := cf("udp", "127.0.0.1:9000", &fakeRawConn{fd: 7}); err != nil {
		t.Fatal(err)
	}
	if got != 7 {
		t.Errorf("got %d, want 7", got)
	}
}

func TestControlFunc_NilProtectorBecomesNoop(t *testing.T) {
	cf := wgturn.ControlFunc(nil)
	if err := cf("udp", "x", &fakeRawConn{fd: 1}); err != nil {
		t.Errorf("nil-protector path: %v", err)
	}
}

func TestControlFunc_PropagatesProtectorError(t *testing.T) {
	want := errors.New("denied")
	cf := wgturn.ControlFunc(wgturn.FuncProtector(func(uintptr) error { return want }))
	err := cf("udp", "x", &fakeRawConn{fd: 1})
	if !errors.Is(err, want) {
		t.Errorf("err = %v", err)
	}
}

// Sanity: ControlFunc returns a value of the correct concrete signature.
var _ func(string, string, syscall.RawConn) error = wgturn.ControlFunc(nil)

// --- Logger ----------------------------------------------------------------

func TestNoopLogger_Silent(t *testing.T) {
	// If NoopLogger panics or writes anywhere, we'll find out via test
	// runner output. This is mainly a smoke + interface-conformance test.
	var l wgturn.Logger = wgturn.NoopLogger{}
	l.Debugf("d")
	l.Infof("i")
	l.Warnf("w")
	l.Errorf("e")
}

func TestStdLogger_LevelFilter(t *testing.T) {
	// We can't easily capture log.Print output without redirection.
	// Just verify the code path doesn't panic for each level.
	l := wgturn.StdLogger{MinLevel: wgturn.LevelWarn}
	l.Debugf("ignored")
	l.Infof("ignored")
	l.Warnf("kept")
	l.Errorf("kept")
}

func TestLevel_String(t *testing.T) {
	if !strings.Contains(wgturn.LevelInfo.String(), "INFO") {
		t.Errorf("Level INFO String = %q", wgturn.LevelInfo.String())
	}
}

// --- StubProvider ----------------------------------------------------------

func TestStubProvider_Counts(t *testing.T) {
	p := stub.New("u", "p", "s:3478")
	for i := 0; i < 5; i++ {
		got, err := p.Fetch(t.Context(), "ignored", i)
		if err != nil {
			t.Fatal(err)
		}
		if got.Username != "u" {
			t.Errorf("got %+v", got)
		}
	}
	if n := p.Calls.Load(); n != 5 {
		t.Errorf("Calls = %d", n)
	}
}

func TestStubProvider_Err(t *testing.T) {
	want := errors.New("nope")
	p := &stub.Provider{Err: want}
	_, err := p.Fetch(t.Context(), "", 0)
	if !errors.Is(err, want) {
		t.Errorf("err = %v", err)
	}
}

// --- Tunnel lifecycle (without real network) -------------------------------

func TestTunnel_StatsBeforeStart_ReturnsErrNotStarted(t *testing.T) {
	tn, err := wgturn.New(validBaseConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tn.Stats(); !errors.Is(err, wgturn.ErrNotStarted) {
		t.Errorf("Stats: err = %v", err)
	}
}

func TestTunnel_StopBeforeStart_NoOp(t *testing.T) {
	tn, err := wgturn.New(validBaseConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := tn.Stop(); err != nil {
		t.Errorf("Stop on never-started Tunnel: %v", err)
	}
}

// Suppress staticcheck unused-import nag if we ever drop atomic from tests.
var _ atomic.Uint64
