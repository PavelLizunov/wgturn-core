// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// wgturn-cli is a small reference binary that exercises wgturn-core on
// desktop. It accepts either:
//
//   - A WireGuard config file (with #@wgt: metadata) via -config <path>.
//     This is the canonical mode and the format is documented in
//     pkg/wgconf/doc.go.
//
//   - Direct flags (-peer, -listen, -turn, -user, -pass, etc.) for the
//     stub credentials provider, useful for local testing without any
//     real VK/WB API.
//
// The CLI does NOT bring up WireGuard for you — it only runs the
// wgturn proxy hub. Point your existing WireGuard client at the local
// listen address to use the tunnel.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/slovn/wgturn-core/pkg/wgconf"
	"github.com/slovn/wgturn-core/pkg/wgturn"
	"github.com/slovn/wgturn-core/pkg/wgturn/provider/stub"
	"github.com/slovn/wgturn-core/pkg/wgturn/provider/vk"
	"github.com/slovn/wgturn-core/pkg/wgturn/provider/vk/captchasolve"
)

// stdioCaptchaSolver prints the captcha URL to stderr and reads the
// answer from stdin. Sufficient for terminal use; mobile/headless
// callers ship their own CaptchaSolver implementation.
type stdioCaptchaSolver struct{}

func (stdioCaptchaSolver) Solve(ctx context.Context, ch vk.CaptchaChallenge) (vk.Solution, error) {
	fmt.Fprintf(os.Stderr,
		"\n══════════════════════════════════════════════════════════════════\n"+
			"VK requires a captcha to issue the call-scoped token.\n"+
			"Open this image in any browser:\n\n  %s\n\n"+
			"Then type the characters you see and press Enter.\n"+
			"(attempt %d, sid=%s)\n══════════════════════════════════════════════════════════════════\n> ",
		ch.ImgURL, ch.Attempt, ch.SID)
	type result struct {
		key string
		err error
	}
	ch1 := make(chan result, 1)
	go func() {
		s := bufio.NewScanner(os.Stdin)
		if s.Scan() {
			ch1 <- result{key: strings.TrimSpace(s.Text())}
			return
		}
		ch1 <- result{err: s.Err()}
	}()
	select {
	case <-ctx.Done():
		return vk.Solution{}, ctx.Err()
	case r := <-ch1:
		if r.err != nil {
			return vk.Solution{}, fmt.Errorf("read stdin: %w", r.err)
		}
		if r.key == "" {
			return vk.Solution{}, fmt.Errorf("empty captcha key")
		}
		return vk.Solution{Key: r.key}, nil
	}
}

func main() {
	var (
		configPath = flag.String("config", "", "WireGuard config file with #@wgt: metadata")
		peer       = flag.String("peer", "", "wgturn server (host:port)")
		listen     = flag.String("listen", "127.0.0.1:9000", "local UDP listen address")
		streams    = flag.Int("streams", 1, "parallel TURN streams")
		peerType   = flag.String("peer-type", string(wgturn.PeerTypeProxyV2),
			"peer type: proxy_v2 / proxy_v1 / wireguard")
		watchdog = flag.Duration("watchdog", 0, "stream watchdog timeout (0 disables)")
		udp      = flag.Bool("udp", false, "dial TURN over UDP instead of TCP")

		// VK provider parameters.
		vkLink = flag.String("vk-link", "",
			"VK Calls invite link (https://vk.com/call/join/<id>) — selects the VK provider")
		vkChromeURL = flag.String("vk-chrome-url", "",
			"Chrome DevTools URL (e.g. http://192.168.0.142:9222) — enables the CDP captcha solver. "+
				"Without this flag captchas are read from stdin (slider mode will fail).")
		vkChromeUA = flag.String("vk-chrome-ua", "",
			"Override navigator.userAgent in the captcha tab (default: whatever Chrome launched with)")

		// Stub-provider parameters: useful for local testing.
		stubUser   = flag.String("stub-user", "", "stub provider TURN username")
		stubPass   = flag.String("stub-pass", "", "stub provider TURN password")
		stubServer = flag.String("stub-server", "", "stub provider TURN host:port")

		statsEvery = flag.Duration("stats", 5*time.Second, "stats print interval (0 disables)")
		verbose    = flag.Bool("v", false, "verbose logging")
	)
	flag.Parse()

	logger := wgturn.StdLogger{MinLevel: wgturn.LevelInfo}
	if *verbose {
		logger.MinLevel = wgturn.LevelDebug
	}

	cfg, err := buildConfig(buildArgs{
		configPath:  *configPath,
		peer:        *peer,
		listen:      *listen,
		streams:     *streams,
		peerType:    *peerType,
		watchdog:    *watchdog,
		udp:         *udp,
		vkLink:      *vkLink,
		vkChromeURL: *vkChromeURL,
		vkChromeUA:  *vkChromeUA,
		stubUser:    *stubUser,
		stubPass:    *stubPass,
		stubServer:  *stubServer,
		logger:      logger,
	})
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	tn, err := wgturn.New(cfg)
	if err != nil {
		log.Fatalf("new: %v", err)
	}

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	startCtx, startCancel := context.WithTimeout(rootCtx, 60*time.Second)
	if err := tn.Start(startCtx); err != nil {
		startCancel()
		log.Fatalf("start: %v", err)
	}
	startCancel()
	log.Printf("wgturn up; local listener: %s", tn.LocalAddr())

	if *statsEvery > 0 {
		go runStatsLoop(rootCtx, tn, *statsEvery)
	}

	<-sigs
	log.Print("signal received; stopping")
	if err := tn.Stop(); err != nil {
		log.Printf("stop: %v", err)
	}
}

type buildArgs struct {
	configPath  string
	peer        string
	listen      string
	streams     int
	peerType    string
	watchdog    time.Duration
	udp         bool
	vkLink      string
	vkChromeURL string
	vkChromeUA  string
	stubUser    string
	stubPass    string
	stubServer  string
	logger      wgturn.Logger
}

// pickCaptchaSolver returns a CDP-driven solver if the CLI was given a
// Chrome URL, otherwise falls back to the stdio prompt. The CDP solver
// is the only one that can pass slider-mode captchas, which VK enforces
// in 2026.
func pickCaptchaSolver(chromeURL, ua string, log wgturn.Logger) vk.CaptchaSolver {
	if chromeURL == "" {
		return stdioCaptchaSolver{}
	}
	return &captchasolve.CDPSolver{
		ChromeURL: chromeURL,
		UserAgent: ua,
		Logger:    log,
	}
}

// buildConfig resolves the user's flags / config file into a wgturn.Config.
// It tries the config file first; if that path was supplied it is the
// authoritative source. Otherwise we fall back to flag-driven mode.
func buildConfig(a buildArgs) (wgturn.Config, error) {
	cfg := wgturn.Config{
		Logger:    a.logger,
		Protector: wgturn.NoopProtector{},
	}

	if a.configPath != "" {
		f, err := os.Open(a.configPath)
		if err != nil {
			return cfg, fmt.Errorf("open config: %w", err)
		}
		defer f.Close()
		settings, err := wgconf.Parse(f)
		if err != nil {
			return cfg, fmt.Errorf("parse config: %w", err)
		}
		fileCfg, err := settings.ToTunnelConfig()
		if err != nil {
			return cfg, fmt.Errorf("config -> tunnel: %w", err)
		}
		// Carry over Logger/Protector + provider info from flags into
		// the file-derived config.
		fileCfg.Logger = cfg.Logger
		fileCfg.Protector = cfg.Protector
		cfg = fileCfg
	} else {
		cfg.PeerAddr = a.peer
		cfg.ListenAddr = a.listen
		cfg.Streams = a.streams
		cfg.PeerType = wgturn.PeerType(a.peerType)
		cfg.WatchdogTimeout = a.watchdog
		cfg.UDP = a.udp
	}

	// Provider selection: VK takes priority if -vk-link is given,
	// then stub, then config-file Hint (for ModeVKLink).
	switch {
	case a.vkLink != "":
		cfg.Provider = vk.New(
			vk.WithLogger(a.logger),
			vk.WithCaptchaSolver(pickCaptchaSolver(a.vkChromeURL, a.vkChromeUA, a.logger)),
		)
		cfg.Mode = wgturn.ModeVKLink
		cfg.Hint = a.vkLink

	case a.stubUser != "" || a.stubPass != "" || a.stubServer != "":
		if a.stubUser == "" || a.stubPass == "" || a.stubServer == "" {
			return cfg, errors.New("stub provider requires -stub-user, -stub-pass, -stub-server together")
		}
		cfg.Provider = stub.New(a.stubUser, a.stubPass, a.stubServer)
		if cfg.Mode == "" {
			cfg.Mode = wgturn.ModeStub
		}

	case cfg.Hint != "" && cfg.Mode == wgturn.ModeVKLink:
		// File-driven: VkLink came from #@wgt:VkLink.
		cfg.Provider = vk.New(
			vk.WithLogger(a.logger),
			vk.WithCaptchaSolver(pickCaptchaSolver(a.vkChromeURL, a.vkChromeUA, a.logger)),
		)

	default:
		return cfg, errors.New("no credentials provider configured: " +
			"pass -vk-link <url> for VK, or -stub-{user,pass,server} for tests, " +
			"or use a config file with #@wgt:Mode = vk_link + #@wgt:VkLink = ...")
	}

	return cfg, nil
}

func runStatsLoop(ctx context.Context, tn *wgturn.Tunnel, period time.Duration) {
	tick := time.NewTicker(period)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s, err := tn.Stats()
			if err != nil {
				continue
			}
			log.Printf("stats: streams=%d/%d tx=%d/%dB rx=%d/%dB drops=%d errs=%d/%d",
				s.StreamsRunning, s.StreamsTotal,
				s.PacketsTx, s.BytesTx,
				s.PacketsRx, s.BytesRx,
				s.DropsTx,
				s.ErrorsTx, s.ErrorsRx)
		}
	}
}
