// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// vpn-client is a ~120-line embedder example showing how to drive the
// full wgturn stack from a single share URL plus a VK Calls link. It
// is functionally equivalent to `wgturn-cli connect-url` and exists
// as a copy-paste starting point for embedders building their own
// VPN client UI on top of wgturn-core.
//
// What this example does
//
//   - Parse a wgturn:// share URL into a Profile.
//   - Build wgturn.Tunnel and wgkernel.Kernel configurations from the
//     Profile, plug the VK provider in.
//   - Open a system TUN, start the proxy hub, start the embedded WG
//     kernel, wait for SIGINT/SIGTERM, tear everything down in
//     reverse order.
//
// What this example does NOT do, intentionally
//
//   - No host-side networking (no `ip link set up`, no addr/route
//     setup). Embedders own their UI and platform conventions; the
//     CLI flavour of this code (cmd/wgturn-cli/connect.go) does it
//     for Linux. macOS/Windows GUI clients typically wire the
//     interface up through their own VPN-config plumbing.
//   - No auto-Chrome launch. We assume the embedder either ships a
//     CDP-driving Chrome of their own (with --remote-debugging-port)
//     or uses a different captcha solver entirely.
//
// Usage:
//
//	go run ./examples/vpn-client \
//	    -url 'wgturn://...#alice' \
//	    -vk-link 'https://vk.com/call/join/...' \
//	    -chrome-url 'http://127.0.0.1:9222'
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/PavelLizunov/wgturn-core/pkg/wgkernel"
	"github.com/PavelLizunov/wgturn-core/pkg/wgshare"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn/provider/vk"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn/provider/vk/captchasolve"
)

func main() {
	var (
		shareURL  = flag.String("url", "", "wgturn:// share URL (required)")
		vkLink    = flag.String("vk-link", "", "VK Calls invite (required)")
		chromeURL = flag.String("chrome-url", "http://127.0.0.1:9222", "Chrome DevTools URL for the captcha solver")
		ifaceName = flag.String("iface", "wgturn0", "TUN interface name")
	)
	flag.Parse()
	if *shareURL == "" || *vkLink == "" {
		log.Fatal("--url and --vk-link are required (see -h)")
	}

	logger := wgturn.StdLogger{MinLevel: wgturn.LevelInfo}

	profile, err := wgshare.Parse(*shareURL)
	if err != nil {
		log.Fatalf("parse url: %v", err)
	}

	turnCfg := profile.ToTunnelConfig(*vkLink)
	turnCfg.Logger = logger
	turnCfg.Protector = wgturn.NoopProtector{}
	turnCfg.Provider = vk.New(
		vk.WithLogger(logger),
		vk.WithCaptchaSolver(&captchasolve.CDPSolver{ChromeURL: *chromeURL}),
	)

	tn, err := wgturn.New(turnCfg)
	if err != nil {
		log.Fatalf("new tunnel: %v", err)
	}
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	startCtx, startCancel := context.WithTimeout(rootCtx, 90*time.Second)
	if err := tn.Start(startCtx); err != nil {
		startCancel()
		log.Fatalf("start tunnel: %v", err)
	}
	startCancel()
	defer func() { _ = tn.Stop() }()
	log.Printf("hub up at %s", tn.LocalAddr())

	tunDev, err := wgkernel.NewSystemTUN(*ifaceName, profile.MTU)
	if err != nil {
		log.Fatalf("open TUN %q (root needed on Linux/macOS, wintun on Windows): %v", *ifaceName, err)
	}
	defer func() { _ = tunDev.Close() }()

	k, err := wgkernel.New(profile.ToKernelConfig(), tunDev,
		wgkernel.WithLogger(logger),
		wgkernel.WithTurnTunnel(tn),
	)
	if err != nil {
		log.Fatalf("new kernel: %v", err)
	}
	if err := k.Start(rootCtx); err != nil {
		log.Fatalf("start kernel: %v", err)
	}
	defer func() { _ = k.Stop() }()
	log.Printf("kernel up; iface=%s addr=%s peers=%d label=%s",
		*ifaceName, profile.Address, len(profile.AllowedIPs), profile.Label)

	// Embedders own the host-side bring-up. On Linux, the equivalent of:
	//
	//   ip link set <iface> up
	//   ip addr add <profile.Address> dev <iface>
	//   ip route add <each AllowedIPs> dev <iface>
	//
	// would go here. cmd/wgturn-cli/hostsetup.go has a working impl.

	log.Print("ready — send traffic through the WG interface")

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	log.Print("signal received; shutting down")
}
