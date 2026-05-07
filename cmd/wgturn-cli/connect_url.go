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

	"github.com/PavelLizunov/wgturn-core/pkg/wgkernel"
	"github.com/PavelLizunov/wgturn-core/pkg/wgshare"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn/provider/vk"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn/provider/yandex"
)

// runConnectURL is the `wgturn-cli connect-url <wgturn://...>` subcommand:
// the URL form of `connect`. The user is expected to have a wgturn://
// share URL (issued by the server's `provision-url`) and a VK Calls
// invite link; the binary handles everything else.
//
// The lifecycle mirrors runConnect verbatim — only the source of the
// Profile changes. We deliberately don't share code between the two
// because the two flows have different "minimum required input"
// surfaces and the duplication is shallow.
func runConnectURL(args []string) error {
	fs := flag.NewFlagSet("wgturn-cli connect-url", flag.ContinueOnError)
	var (
		urlArg = fs.String("url", "",
			"wgturn:// share URL issued by the server (required positional or via -url)")
		vkLink = fs.String("vk-link", "",
			"VK Calls invite (https://vk.com/call/join/<id>); required for VK provider. "+
				"Multiple links comma-separated for multi-link fan-out.")
		ifaceName    = fs.String("iface", "wgturn0", "TUN interface name")
		vkChromeURL  = fs.String("vk-chrome-url", "", "DevTools URL of an existing Chrome (skips auto-launch)")
		vkChromeAuto = fs.Bool("vk-chrome-auto", true, "auto-spawn headless Chrome when --vk-chrome-url is empty")
		vkChromeUA   = fs.String("vk-chrome-ua", "", "navigator.userAgent override for the captcha tab")
		statsEvery   = fs.Duration("stats", 5*time.Second, "stats print interval (0 disables)")
		verbose      = fs.Bool("v", false, "verbose logging")
	)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(),
			"Usage: wgturn-cli connect-url [flags] <wgturn://...>\n\n"+
				"Single-command client driven by a share URL: brings up the wgturn\n"+
				"hub, an embedded WireGuard kernel, and host-side networking from\n"+
				"a wgturn:// URL plus a VK Calls invite. The URL bundles every key,\n"+
				"IP, and option except the VK link, which is a runtime parameter.\n\n"+
				"Flags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *urlArg == "" && fs.NArg() == 1 {
		*urlArg = fs.Arg(0)
	}
	if *urlArg == "" {
		fs.Usage()
		return errors.New("connect-url: share URL is required (positional or -url)")
	}

	logger := wgturn.StdLogger{MinLevel: wgturn.LevelInfo}
	if *verbose {
		logger.MinLevel = wgturn.LevelDebug
	}

	profile, err := wgshare.Parse(*urlArg)
	if err != nil {
		return fmt.Errorf("connect-url: parse URL: %w", err)
	}

	vkLinks := splitLinks(*vkLink)
	if len(vkLinks) == 0 {
		return errors.New("connect-url: --vk-link <url> is required (the URL itself does NOT carry it)")
	}

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	// Chrome resolution: same precedence as runConnect.
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

	turnCfg := profile.ToTunnelConfig(vkLinks[0])
	turnCfg.Logger = logger
	turnCfg.Protector = wgturn.NoopProtector{}
	if len(vkLinks) > 1 {
		turnCfg.Hint = ""
		turnCfg.Hints = vkLinks
	}
	turnCfg.Provider = &routedProvider{
		vk: vk.New(
			vk.WithLogger(logger),
			vk.WithCaptchaSolver(pickCaptchaSolver(chromeURL, *vkChromeUA, logger)),
		),
		yandex: yandex.New(yandex.WithLogger(logger)),
		logger: logger,
	}

	tn, err := wgturn.New(turnCfg)
	if err != nil {
		return fmt.Errorf("connect-url: new tunnel: %w", err)
	}
	startCtx, startCancel := context.WithTimeout(rootCtx, connectStartTimeout)
	if err := tn.Start(startCtx); err != nil {
		startCancel()
		return fmt.Errorf("connect-url: start tunnel: %w", err)
	}
	startCancel()
	log.Printf("connect-url: hub up; local listener %s", tn.LocalAddr())
	defer func() {
		if err := tn.Stop(); err != nil {
			logger.Warnf("tunnel stop: %v", err)
		}
	}()

	kernelCfg := profile.ToKernelConfig()

	tunDev, err := wgkernel.NewSystemTUN(*ifaceName, kernelCfg.MTU)
	if err != nil {
		return fmt.Errorf("connect-url: open TUN %q: %w "+
			"(Linux/macOS need sudo; Windows needs wintun.dll next to the binary)",
			*ifaceName, err)
	}
	defer func() { _ = tunDev.Close() }()

	k, err := wgkernel.New(kernelCfg, tunDev,
		wgkernel.WithLogger(logger),
		wgkernel.WithTurnTunnel(tn),
	)
	if err != nil {
		return fmt.Errorf("connect-url: new kernel: %w", err)
	}
	kernelStartCtx, kernelStartCancel := context.WithTimeout(rootCtx, 10*time.Second)
	if err := k.Start(kernelStartCtx); err != nil {
		kernelStartCancel()
		return fmt.Errorf("connect-url: start kernel: %w", err)
	}
	kernelStartCancel()
	log.Printf("connect-url: kernel up; iface=%s address=%s peers=%d label=%s",
		*ifaceName, kernelCfg.Address, len(kernelCfg.Peers), profile.Label)
	defer func() {
		if err := k.Stop(); err != nil {
			logger.Warnf("kernel stop: %v", err)
		}
	}()

	teardown, err := setupHostIface(*ifaceName, kernelCfg, logger)
	if err != nil {
		if errors.Is(err, errHostSetupUnsupported) {
			log.Printf("connect-url: WARNING: %v", err)
			log.Print("connect-url: tunnel is up; configure addresses/routes manually then send traffic")
		} else {
			return fmt.Errorf("connect-url: host setup: %w", err)
		}
	} else {
		log.Print("connect-url: host configured (link up, addrs assigned, routes added)")
		defer teardown()
	}

	if *statsEvery > 0 {
		go runStatsLoop(rootCtx, tn, *statsEvery)
	}

	log.Print("connect-url: ready. Send traffic through the WG interface.")
	select {
	case sig := <-sigs:
		log.Printf("connect-url: received %v; shutting down", sig)
	case <-rootCtx.Done():
		log.Print("connect-url: context cancelled; shutting down")
	}
	return nil
}
