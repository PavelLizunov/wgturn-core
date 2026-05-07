// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestFindChromeOnPath_FindsByName plants a fake "google-chrome" in a
// scratch dir, points $PATH at that dir alone, and asserts findChromeOnPath
// resolves to our fake. Confirms the $PATH branch independently of any
// real Chrome install.
func TestFindChromeOnPath_FindsByName(t *testing.T) {
	if runtime.GOOS == goosWindows {
		// On Windows exec.LookPath needs .exe and PATHEXT semantics; the
		// fake-binary trick below isn't worth porting just for tests.
		t.Skip("skipping $PATH probe on Windows; relies on POSIX semantics")
	}

	dir := t.TempDir()
	fake := filepath.Join(dir, "google-chrome")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake chrome: %v", err)
	}
	t.Setenv("PATH", dir)

	got, err := findChromeOnPath()
	if err != nil {
		t.Fatalf("findChromeOnPath: %v", err)
	}
	if got != fake {
		t.Errorf("findChromeOnPath = %q, want %q", got, fake)
	}
}

// TestFindChromeOnPath_NoneFound confirms the user-facing error
// message includes the install hint — that's what the user reads when
// `wgturn-cli connect` refuses to start.
func TestFindChromeOnPath_NoneFound(t *testing.T) {
	if runtime.GOOS == "darwin" {
		// We can't credibly stub /Applications/* in a test, and a real
		// Chrome install on the dev machine would make this flaky.
		t.Skip("skipping on macOS: cannot mask /Applications bundle search")
	}
	if runtime.GOOS == goosWindows {
		t.Skip("skipping on Windows: cannot mask C:\\Program Files probes")
	}

	t.Setenv("PATH", t.TempDir()) // empty dir => nothing on $PATH

	_, err := findChromeOnPath()
	if err == nil {
		t.Fatal("expected error when no Chrome is reachable")
	}
	msg := err.Error()
	for _, want := range []string{
		"$PATH",
		"--vk-chrome-url",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q missing hint %q", msg, want)
		}
	}
}

// TestFindChromeOnPath_PrefersChromeOverChromium pins the candidate
// order: real Google Chrome first, Chromium second, because vk.com's
// JA3 fingerprinting trips slider-mode escalation slightly more often
// against Chromium's TLS stack. If we ever need to flip this for any
// reason, this test forces an explicit decision.
func TestFindChromeOnPath_PrefersChromeOverChromium(t *testing.T) {
	if runtime.GOOS == goosWindows {
		t.Skip("Windows uses install-path probe, not $PATH order")
	}

	dir := t.TempDir()
	for _, name := range []string{"google-chrome", "chromium-browser"} {
		f := filepath.Join(dir, name)
		if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	t.Setenv("PATH", dir)

	got, err := findChromeOnPath()
	if err != nil {
		t.Fatalf("findChromeOnPath: %v", err)
	}
	if !strings.HasSuffix(got, "google-chrome") {
		t.Errorf("findChromeOnPath = %q, want path ending in google-chrome", got)
	}
}

// TestWaitChromeReady_RetriesUntilOK starts an httptest.Server that
// answers the first probe with 503 and the second with 200, simulating
// "Chrome still booting" → "Chrome ready". waitChromeReady must poll
// past the 503 and only return on the 200.
func TestWaitChromeReady_RetriesUntilOK(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := waitChromeReady(context.Background(), srv.URL, 2*time.Second); err != nil {
		t.Fatalf("waitChromeReady: %v", err)
	}
	if got := hits.Load(); got < 2 {
		t.Errorf("expected ≥ 2 probe attempts, got %d", got)
	}
}

// TestWaitChromeReady_TimeoutOnDeadServer points the helper at a closed
// listener (httptest.NewServer + immediate Close) and asserts we get a
// "Chrome did not answer" error within the timeout — not a hang, not a
// nil error.
func TestWaitChromeReady_TimeoutOnDeadServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // immediately — connections will be refused

	start := time.Now()
	err := waitChromeReady(context.Background(), srv.URL, 300*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	elapsed := time.Since(start)
	if elapsed > 1*time.Second {
		t.Errorf("waitChromeReady took %v, want <= ~300ms+slack", elapsed)
	}
	if !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestWaitChromeReady_HonorsContext ensures a cancelled ctx unblocks
// the poll immediately rather than running the timeout to completion.
func TestWaitChromeReady_HonorsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	start := time.Now()
	err := waitChromeReady(ctx, srv.URL, 5*time.Second)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Errorf("did not return promptly on cancel (took %v)", time.Since(start))
	}
}

// TestChromeProcess_StopOnNilSafe verifies Stop() is robust to being
// called on a partially-initialised or nil receiver, which matters
// because launchChrome's error path defers cleanup.
func TestChromeProcess_StopOnNilSafe(t *testing.T) {
	var cp *chromeProcess
	if err := cp.Stop(); err != nil {
		t.Errorf("Stop on nil: %v", err)
	}
	cp = &chromeProcess{} // no cmd, no dataDir
	if err := cp.Stop(); err != nil {
		t.Errorf("Stop on empty: %v", err)
	}
}

// TestLaunchChrome_PropagatesNoBrowserError points launchChrome at an
// empty $PATH so findChromeOnPath fails, and asserts the user-facing
// "chrome auto-launch:" prefix is wrapped around the install hint.
// We don't try to actually spawn Chrome in CI — that requires a
// browser binary the runner doesn't have.
func TestLaunchChrome_PropagatesNoBrowserError(t *testing.T) {
	if runtime.GOOS == goosDarwin || runtime.GOOS == goosWindows {
		t.Skip("cannot mask system Chrome install paths on this platform")
	}
	t.Setenv("PATH", t.TempDir())

	_, err := launchChrome(context.Background(), noopLogger{})
	if err == nil {
		t.Fatal("expected error when no Chrome is reachable")
	}
	if !strings.HasPrefix(err.Error(), "chrome auto-launch:") {
		t.Errorf("error %q missing 'chrome auto-launch:' prefix", err)
	}
	if !strings.Contains(err.Error(), "--vk-chrome-url") {
		t.Errorf("error %q missing the --vk-chrome-url install hint", err)
	}
}

// noopLogger swallows all log calls. Local to the chrome tests so we
// don't pull a heavier logger into the test surface.
type noopLogger struct{}

func (noopLogger) Debugf(string, ...any) {}
func (noopLogger) Infof(string, ...any)  {}
func (noopLogger) Warnf(string, ...any)  {}
func (noopLogger) Errorf(string, ...any) {}
