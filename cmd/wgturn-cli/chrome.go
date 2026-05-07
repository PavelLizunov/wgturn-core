// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
)

// chromeDefaultDebugPort is the loopback port we tell Chrome to listen
// on for the DevTools Protocol. Hard-coded to match the manual-mode
// instruction in handoff-readme so users can see "this is the same
// thing the README told you to run".
const chromeDefaultDebugPort = 9222

// chromeReadyTimeout caps how long we wait for /json/version to start
// answering after spawning Chrome. Empirically Chrome is ready well
// under a second on modern hardware; 5 s leaves headroom for slow VMs
// without leaving the user staring at a black terminal.
const chromeReadyTimeout = 5 * time.Second

// chromePathCandidates are the executable names we hunt for in $PATH.
// Order is "best fingerprint first": real Google Chrome lines up with
// VK's bot heuristics better than vanilla Chromium, which sometimes
// trips a slider-mode escalation. We still fall through to chromium /
// chromium-browser because that's all most Linux distro packages ship.
var chromePathCandidates = []string{
	"google-chrome",
	"google-chrome-stable",
	"chromium-browser",
	"chromium",
}

// chromeMacOSAppBundles list well-known .app bundle entrypoints. macOS
// users typically install Chrome via .dmg without putting the binary
// on $PATH, so we have to look for the bundle directly.
var chromeMacOSAppBundles = []string{
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
}

// chromeWindowsInstallPaths covers the standard MSI installer
// locations. Per-user installs end up under %LOCALAPPDATA% but we
// don't probe there until someone reports needing it.
var chromeWindowsInstallPaths = []string{
	`C:\Program Files\Google\Chrome\Application\chrome.exe`,
	`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	`C:\Program Files\Chromium\Application\chrome.exe`,
}

// goosDarwin / goosWindows mirror runtime.GOOS values. Named so the
// goconst linter and human readers both have a single source of truth.
const (
	goosDarwin  = "darwin"
	goosWindows = "windows"
)

// extractEmbeddedChrome is wired up by cmd/wgturn-cli/embedded_yes.go
// (build tag `embedded`) to extract a bundled chrome-headless-shell
// archive on first use and return its path. Default builds leave this
// nil; findChromeOnPath then falls through to the install-hint error.
var extractEmbeddedChrome func() (string, error)

// findChromeOnPath returns the first Chrome / Chromium binary found,
// hunting in this order:
//
//  1. $PATH for well-known executable names (Linux + chrome-on-PATH on
//     macOS / Windows).
//  2. Platform-specific standard install locations (macOS .app bundle,
//     Windows %ProgramFiles%).
//  3. Embedded chrome-headless-shell extracted into the user cache —
//     ONLY when the binary was built with `-tags embedded` (otherwise
//     extractEmbeddedChrome is nil and this step is skipped). The
//     extract is idempotent: subsequent calls are a single stat.
//
// Returns "" with a non-nil error if nothing was found, with a message
// the CLI surfaces verbatim.
//
// Exposed for testing; the runtime caller is launchChrome.
func findChromeOnPath() (string, error) {
	for _, name := range chromePathCandidates {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	var extra []string
	switch runtime.GOOS {
	case goosDarwin:
		extra = chromeMacOSAppBundles
	case goosWindows:
		extra = chromeWindowsInstallPaths
	}
	for _, p := range extra {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	if extractEmbeddedChrome != nil {
		if path, err := extractEmbeddedChrome(); err == nil {
			return path, nil
		}
		// Fall through on embedded-extract failure (e.g. unsupported
		// platform like linux/arm64) so the user gets the install-hint
		// message — that's still the correct next step.
	}
	return "", errors.New(
		"no Chrome / Chromium binary found in $PATH or standard install locations; " +
			"install Chrome / Chromium, or pass --vk-chrome-url pointing at a running instance")
}

// chromeProcess is the handle the CLI keeps for a Chrome we launched
// ourselves. It carries the temporary --user-data-dir so we can wipe
// it on shutdown — Chrome scribbles ~10 MB of profile junk per launch.
type chromeProcess struct {
	cmd     *exec.Cmd
	url     string // CDP base URL, e.g. "http://127.0.0.1:9222"
	dataDir string // temporary --user-data-dir, removed on Stop
}

// URL returns the CDP base URL the running Chrome is listening on.
// Suitable for vk.captchasolve.CDPSolver.ChromeURL.
func (c *chromeProcess) URL() string { return c.url }

// launchChrome spawns headless Chrome with --remote-debugging-port and
// returns once the DevTools endpoint answers /json/version. The caller
// owns the returned process and MUST call Stop on shutdown to reap the
// child and clean the user-data dir.
//
// Failures during start (binary missing, exec failure, never-ready)
// are wrapped with enough context for the user to act:
//
//	chrome auto-launch: <step>: <underlying error>
//
// On any error, any partially-created scratch dir is cleaned up before
// returning; callers do not need to call Stop on a failed launch.
func launchChrome(ctx context.Context, log wgturn.Logger) (*chromeProcess, error) {
	bin, err := findChromeOnPath()
	if err != nil {
		return nil, fmt.Errorf("chrome auto-launch: %w", err)
	}

	dataDir, err := os.MkdirTemp("", "wgturn-chrome-")
	if err != nil {
		return nil, fmt.Errorf("chrome auto-launch: scratch dir: %w", err)
	}

	args := []string{
		"--headless=new",
		"--no-sandbox", // safe under headless; required in many container envs
		"--disable-gpu",
		fmt.Sprintf("--remote-debugging-port=%d", chromeDefaultDebugPort),
		"--remote-debugging-address=127.0.0.1",
		"--user-data-dir=" + dataDir,
		"--disable-dev-shm-usage", // /dev/shm too small in some containers
		"--no-first-run",
		"--no-default-browser-check",
		"about:blank",
	}

	cmd := exec.Command(bin, args...) //nolint:gosec // bin came from LookPath/Stat
	// Chrome is extremely chatty on stderr even with --headless. Discard
	// to keep our log signal-to-noise tolerable; users with debug needs
	// can launch Chrome manually and pass --vk-chrome-url.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dataDir)
		return nil, fmt.Errorf("chrome auto-launch: start %q: %w", bin, err)
	}
	log.Infof("chrome auto-launch: spawned %s pid=%d data-dir=%s",
		bin, cmd.Process.Pid, dataDir)

	cp := &chromeProcess{
		cmd:     cmd,
		url:     fmt.Sprintf("http://127.0.0.1:%d", chromeDefaultDebugPort),
		dataDir: dataDir,
	}

	if err := waitChromeReady(ctx, cp.url, chromeReadyTimeout); err != nil {
		_ = cp.Stop()
		return nil, fmt.Errorf("chrome auto-launch: %w "+
			"(if port %d is already taken by another Chrome instance, "+
			"pass --vk-chrome-url http://127.0.0.1:%d to use it directly)",
			err, chromeDefaultDebugPort, chromeDefaultDebugPort)
	}
	log.Infof("chrome auto-launch: ready at %s", cp.url)
	return cp, nil
}

// waitChromeReady polls cdpURL+"/json/version" until it returns 200 OK
// or the timeout elapses. The first successful response means Chrome's
// DevTools listener is up and ready to take CDP commands.
func waitChromeReady(ctx context.Context, cdpURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 1 * time.Second}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cdpURL+"/json/version", nil)
		if err != nil {
			return fmt.Errorf("build probe request: %w", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Chrome did not answer at %s within %v", cdpURL, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// Stop kills the Chrome process, reaps it, and removes the scratch
// user-data dir. Idempotent and safe to call on a partially-started
// process.
func (c *chromeProcess) Stop() error {
	if c == nil {
		return nil
	}
	var firstErr error
	if c.cmd != nil && c.cmd.Process != nil {
		if err := c.cmd.Process.Kill(); err != nil &&
			!errors.Is(err, os.ErrProcessDone) {
			firstErr = fmt.Errorf("kill chrome: %w", err)
		}
		// Wait reaps the zombie; the error here is the exit status of a
		// killed process, which we don't care about.
		_, _ = c.cmd.Process.Wait()
	}
	if c.dataDir != "" {
		if err := os.RemoveAll(c.dataDir); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("rm chrome data-dir: %w", err)
		}
	}
	return firstErr
}
