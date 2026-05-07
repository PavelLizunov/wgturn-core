// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/slovn/wgturn-core/pkg/wgconf"
	"github.com/slovn/wgturn-core/pkg/wgkernel"
	"github.com/slovn/wgturn-core/pkg/wgturn"
	"github.com/slovn/wgturn-core/pkg/wgturn/provider/vk"
	"github.com/slovn/wgturn-core/pkg/wgturn/provider/yandex"
)

// connectStartTimeout caps how long we'll wait for the wgturn hub to
// open its first stream + the wg kernel to apply its IPC config.
// Generous because VK captcha solving can take a few seconds × N
// cred-groups on cold start.
const connectStartTimeout = 90 * time.Second

// runConnect is the `wgturn-cli connect <config.conf>` subcommand: a
// single command stands up the wgturn hub, an embedded WireGuard
// kernel, and (Linux only in v0) the host-side iface configuration.
// Auto-launches headless Chrome for the CDP captcha solver unless
// --vk-chrome-url points at one.
//
// Lifecycle, in order:
//
//  1. parse flags + .conf file
//  2. spawn Chrome (or use --vk-chrome-url override)
//  3. start wgturn hub
//  4. open TUN + start wgkernel (with WithTurnTunnel rewriting the peer
//     Endpoint to the hub's local listener)
//  5. configure host (link up, addrs, routes) — Linux only in v0
//  6. wait for SIGINT/SIGTERM, then teardown in reverse order
func runConnect(args []string) error {
	fs := flag.NewFlagSet("wgturn-cli connect", flag.ContinueOnError)
	var (
		configPath  = fs.String("config", "", "WireGuard config file with #@wgt: metadata (required positional or via -config)")
		ifaceName   = fs.String("iface", "wgturn0", "TUN interface name (Linux/macOS)")
		vkChromeURL = fs.String("vk-chrome-url", "",
			"DevTools URL of an already-running Chrome (e.g. http://127.0.0.1:9222). "+
				"When empty and --vk-chrome-auto=true, we spawn headless Chrome ourselves.")
		vkChromeAuto = fs.Bool("vk-chrome-auto", true,
			"Auto-spawn headless Chrome for the CDP captcha solver. Set false to require an "+
				"explicit --vk-chrome-url. No-op when --vk-chrome-url is provided.")
		vkChromeUA = fs.String("vk-chrome-ua", "",
			"Override navigator.userAgent in the captcha tab")
		statsEvery = fs.Duration("stats", 5*time.Second, "stats print interval (0 disables)")
		verbose    = fs.Bool("v", false, "verbose logging")
	)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(),
			"Usage: wgturn-cli connect [flags] <wireguard.conf>\n\n"+
				"Single-command VPN: brings up the wgturn hub, an embedded WireGuard\n"+
				"kernel, and host-side networking from one .conf file.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Allow positional config path: `wgturn-cli connect myvpn.conf`.
	if *configPath == "" && fs.NArg() == 1 {
		*configPath = fs.Arg(0)
	}
	if *configPath == "" {
		fs.Usage()
		return errors.New("connect: config path is required (positional or -config)")
	}

	logger := wgturn.StdLogger{MinLevel: wgturn.LevelInfo}
	if *verbose {
		logger.MinLevel = wgturn.LevelDebug
	}

	// 1. Parse the WireGuard .conf into Settings (wgturn metadata +
	//    [Interface] + [Peer]).
	settings, err := parseWGConfig(*configPath)
	if err != nil {
		return err
	}

	// 2. Build the wgturn hub config from the metadata. Provider gets
	//    set after we know which Chrome URL to point CDPSolver at.
	turnCfg, err := settings.ToTunnelConfig()
	if err != nil {
		return fmt.Errorf("config -> turn: %w", err)
	}
	turnCfg.Logger = logger
	turnCfg.Protector = wgturn.NoopProtector{}

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	// 3. Resolve the Chrome URL: explicit override > auto-launch > none.
	chromeURL, chrome, err := resolveChromeURL(rootCtx, *vkChromeURL, *vkChromeAuto, logger)
	if err != nil {
		return err
	}
	defer func() {
		if chrome != nil {
			if err := chrome.Stop(); err != nil {
				logger.Warnf("chrome stop: %v", err)
			}
		}
	}()

	// 4. Wire the captcha solver into the routed provider.
	turnCfg.Provider = &routedProvider{
		vk: vk.New(
			vk.WithLogger(logger),
			vk.WithCaptchaSolver(pickCaptchaSolver(chromeURL, *vkChromeUA, logger)),
		),
		yandex: yandex.New(yandex.WithLogger(logger)),
		logger: logger,
	}

	// 5. Build wgturn.Tunnel and start it. The hub MUST be started
	//    before wgkernel because WithTurnTunnel reads LocalAddr().
	tn, err := wgturn.New(turnCfg)
	if err != nil {
		return fmt.Errorf("new tunnel: %w", err)
	}
	startCtx, startCancel := context.WithTimeout(rootCtx, connectStartTimeout)
	if err := tn.Start(startCtx); err != nil {
		startCancel()
		return fmt.Errorf("start tunnel: %w", err)
	}
	startCancel()
	log.Printf("connect: hub up; local listener %s", tn.LocalAddr())
	defer func() {
		if err := tn.Stop(); err != nil {
			logger.Warnf("tunnel stop: %v", err)
		}
	}()

	// 6. Build wgkernel config from settings.Iface + settings.WGPeers.
	kernelCfg, err := buildKernelConfig(settings)
	if err != nil {
		return fmt.Errorf("config -> kernel: %w", err)
	}

	// 7. Open the system TUN. This is where root is required on
	//    Linux/macOS; on Windows wintun.dll must be co-located.
	tunDev, err := wgkernel.NewSystemTUN(*ifaceName, kernelCfg.MTU)
	if err != nil {
		return fmt.Errorf("open TUN %q: %w "+
			"(Linux/macOS need sudo; Windows needs wintun.dll next to the binary)",
			*ifaceName, err)
	}
	defer func() { _ = tunDev.Close() }()

	// 8. Bring up wgkernel. WithTurnTunnel rewrites every peer
	//    Endpoint to tn.LocalAddr() so wg sends to the hub instead
	//    of the public internet.
	k, err := wgkernel.New(kernelCfg, tunDev,
		wgkernel.WithLogger(logger),
		wgkernel.WithTurnTunnel(tn),
	)
	if err != nil {
		return fmt.Errorf("new kernel: %w", err)
	}
	kernelStartCtx, kernelStartCancel := context.WithTimeout(rootCtx, 10*time.Second)
	if err := k.Start(kernelStartCtx); err != nil {
		kernelStartCancel()
		return fmt.Errorf("start kernel: %w", err)
	}
	kernelStartCancel()
	log.Printf("connect: kernel up; iface=%s addresses=%v peers=%d",
		*ifaceName, kernelCfg.Address, len(kernelCfg.Peers))
	defer func() {
		if err := k.Stop(); err != nil {
			logger.Warnf("kernel stop: %v", err)
		}
	}()

	// 9. Host-side network setup (Linux v0; macOS/Windows print a
	//    helpful TODO instead of failing — the tunnel is technically up,
	//    user just has to manually `ip addr add` etc.).
	teardown, err := setupHostIface(*ifaceName, kernelCfg, logger)
	if err != nil {
		if errors.Is(err, errHostSetupUnsupported) {
			log.Printf("connect: WARNING: %v", err)
			log.Print("connect: tunnel is up; configure addresses/routes manually then send traffic")
		} else {
			return fmt.Errorf("host setup: %w", err)
		}
	} else {
		log.Print("connect: host configured (link up, addrs assigned, routes added)")
		defer teardown()
	}

	// 10. Stats loop + wait for shutdown signal.
	if *statsEvery > 0 {
		go runStatsLoop(rootCtx, tn, *statsEvery)
	}

	log.Print("connect: ready. Send traffic through the WG interface.")
	select {
	case sig := <-sigs:
		log.Printf("connect: received %v; shutting down", sig)
	case <-rootCtx.Done():
		log.Print("connect: context cancelled; shutting down")
	}
	return nil
}

// parseWGConfig opens path and runs wgconf.Parse over it.
// Factored out so the connect flow has a single place to mention the
// expected file format in error messages.
func parseWGConfig(path string) (wgconf.Settings, error) {
	f, err := os.Open(path) //nolint:gosec // user-supplied path; CLI input
	if err != nil {
		return wgconf.Settings{}, fmt.Errorf("open config: %w", err)
	}
	defer func() { _ = f.Close() }()
	settings, err := wgconf.Parse(f)
	if err != nil {
		return wgconf.Settings{}, fmt.Errorf("parse config: %w", err)
	}
	return settings, nil
}

// resolveChromeURL picks the CDP endpoint for the captcha solver.
// Returns (url, optional chromeProcess we own, error).
//
// Precedence (matches user expectations from the manual mode):
//
//  1. --vk-chrome-url is set: use it verbatim, never spawn.
//  2. --vk-chrome-auto=true (default): spawn our own headless Chrome.
//  3. Otherwise: empty URL, the captcha solver falls back to stdio
//     (which only works for VK's legacy text-captcha mode that is no
//     longer in rotation — but this matches the legacy CLI).
func resolveChromeURL(ctx context.Context, override string, auto bool, logger wgturn.Logger) (string, *chromeProcess, error) {
	if override != "" {
		logger.Infof("chrome: using existing instance at %s", override)
		return override, nil, nil
	}
	if !auto {
		logger.Warnf("chrome: --vk-chrome-auto=false and no --vk-chrome-url; " +
			"captcha solver will fall back to stdio (slider mode will fail)")
		return "", nil, nil
	}
	cp, err := launchChrome(ctx, logger)
	if err != nil {
		return "", nil, err
	}
	return cp.URL(), cp, nil
}

// buildKernelConfig converts the wgconf.Settings [Interface] / [Peer]
// data into a wgkernel.Config. This is the cross-package glue that
// keeps wgconf free of any wgkernel dependency.
//
// Errors are explicit so the user knows exactly which field is missing.
func buildKernelConfig(s wgconf.Settings) (wgkernel.Config, error) {
	if s.Iface.PrivateKey == "" {
		return wgkernel.Config{}, errors.New("[Interface] PrivateKey is required for the embedded kernel")
	}
	if len(s.WGPeers) == 0 {
		return wgkernel.Config{}, errors.New("at least one [Peer] section is required")
	}
	cfg := wgkernel.Config{
		PrivateKey: s.Iface.PrivateKey,
		Address:    s.Iface.Address,
		DNS:        s.Iface.DNS,
		MTU:        s.Iface.MTU,
		ListenPort: s.Iface.ListenPort,
	}
	for i, p := range s.WGPeers {
		if p.PublicKey == "" {
			return wgkernel.Config{}, fmt.Errorf("[Peer #%d] PublicKey is required", i)
		}
		cfg.Peers = append(cfg.Peers, wgkernel.PeerConfig{
			PublicKey:           p.PublicKey,
			PresharedKey:        p.PresharedKey,
			Endpoint:            p.Endpoint,
			AllowedIPs:          p.AllowedIPs,
			PersistentKeepalive: p.PersistentKeepalive,
		})
	}
	return cfg, nil
}
