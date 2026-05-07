// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// wgturn-cli is the reference binary for wgturn-core on desktop. Three
// subcommands plus the legacy mode:
//
//	wgturn-cli connect <wireguard.conf>
//	    Single-command VPN: brings up both the wgturn proxy hub and an
//	    embedded WireGuard kernel from one .conf file. Auto-launches
//	    headless Chrome for the CDP captcha solver unless --vk-chrome-url
//	    points at an existing instance. ROADMAP N1.
//
//	wgturn-cli serve <server.conf>
//	    Server-side counterpart to `connect`: terminates wgturn proxy_v2
//	    sessions on a UDP listener and forwards inner payload to a
//	    Backend (typically a local WireGuard daemon). Intended to replace
//	    the legacy GPL upstream on a VPS. ROADMAP N8.
//
//	wgturn-cli [-config wireguard.conf] [-peer host:port] [-vk-link url] ...
//	    Legacy hub-only mode: only runs the wgturn proxy. The user must
//	    bring up WireGuard separately (e.g. `wg-quick up`). Kept for
//	    backward compatibility with the handoff bundle's existing
//	    instructions.
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

	"github.com/PavelLizunov/wgturn-core/pkg/wgconf"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn/provider/stub"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn/provider/vk"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn/provider/vk/captchasolve"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn/provider/yandex"
)

func main() {
	// Subcommand dispatch. The legacy mode (no subcommand, top-level
	// flags) is preserved verbatim so handoff-bundle instructions still
	// work; new functionality goes under named subcommands.
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "connect":
			if err := runConnect(os.Args[2:]); err != nil {
				log.Fatalf("connect: %v", err)
			}
			return
		case "connect-url":
			if err := runConnectURL(os.Args[2:]); err != nil {
				log.Fatalf("connect-url: %v", err)
			}
			return
		case "serve":
			if err := runServe(os.Args[2:]); err != nil {
				log.Fatalf("serve: %v", err)
			}
			return
		case "provision-url":
			if err := runProvisionURL(os.Args[2:]); err != nil {
				log.Fatalf("provision-url: %v", err)
			}
			return
		case "revoke-url":
			if err := runRevokeURL(os.Args[2:]); err != nil {
				log.Fatalf("revoke-url: %v", err)
			}
			return
		}
	}
	if err := runProxy(os.Args[1:]); err != nil {
		log.Fatalf("%v", err)
	}
}

// runProxy is the legacy "hub only" entry point: it parses top-level
// flags, builds a wgturn.Tunnel, and runs it until SIGINT/SIGTERM.
// The user is responsible for bringing up WireGuard separately.
//
// Extracted from main() so the new `connect` subcommand can share the
// process lifecycle without forking the codebase.
func runProxy(args []string) error {
	fs := flag.NewFlagSet("wgturn-cli", flag.ContinueOnError)
	var (
		configPath = fs.String("config", "", "WireGuard config file with #@wgt: metadata")
		peer       = fs.String("peer", "", "wgturn server (host:port)")
		listen     = fs.String("listen", "127.0.0.1:9000", "local UDP listen address")
		streams    = fs.Int("streams", 24,
			"parallel TURN streams. Default 24 = 6 cred-groups × 4 streams "+
				"each, the empirical sweet spot for VK Calls TURN per source IP "+
				"(~200 KB/s aggregate, ~6 captcha solves at startup). Drop to "+
				"4-16 for fewer captchas at the cost of throughput; raising "+
				"past 32 starts hitting VK's per-IP anonymous-token rate limit.")
		peerType = fs.String("peer-type", string(wgturn.PeerTypeProxyV2),
			"peer type: proxy_v2 / proxy_v1 / wireguard")
		watchdog = fs.Duration("watchdog", 0, "stream watchdog timeout (0 disables)")
		udp      = fs.Bool("udp", false, "dial TURN over UDP instead of TCP")

		// VK provider parameters.
		vkLink = fs.String("vk-link", "",
			"VK Calls invite link (https://vk.com/call/join/<id>) — selects the VK provider. "+
				"Pass multiple comma-separated links to fan out across distinct VK call sessions; "+
				"each cred-group of streams (-streams / 4 by default) gets the next link round-robin, "+
				"multiplying per-call bandwidth shaping.")
		vkChromeURL = fs.String("vk-chrome-url", "",
			"Chrome DevTools URL (e.g. http://192.168.0.142:9222) — enables the CDP captcha solver. "+
				"Without this flag captchas are read from stdin (slider mode will fail).")
		vkChromeUA = fs.String("vk-chrome-ua", "",
			"Override navigator.userAgent in the captcha tab (default: whatever Chrome launched with)")

		// Stub-provider parameters: useful for local testing.
		stubUser   = fs.String("stub-user", "", "stub provider TURN username")
		stubPass   = fs.String("stub-pass", "", "stub provider TURN password")
		stubServer = fs.String("stub-server", "", "stub provider TURN host:port")

		statsEvery = fs.Duration("stats", 5*time.Second, "stats print interval (0 disables)")
		verbose    = fs.Bool("v", false, "verbose logging")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

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
		return fmt.Errorf("config: %w", err)
	}

	tn, err := wgturn.New(cfg)
	if err != nil {
		return fmt.Errorf("new: %w", err)
	}

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	startCtx, startCancel := context.WithTimeout(rootCtx, 60*time.Second)
	if err := tn.Start(startCtx); err != nil {
		startCancel()
		return fmt.Errorf("start: %w", err)
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
	return nil
}

// routedProvider dispatches Fetch calls between the VK and Yandex
// Telemost providers based on the shape of the hint URL.
//
//   - Anything with "telemost.yandex." or a "telemost:" prefix → yandex.
//   - Anything else → vk (the historical default).
//
// A single Tunnel can therefore mix VK + Telemost links via
// wgturn.Config.Hints; cred-groups round-robin through the pool and
// each group's hint picks the right backend automatically.
type routedProvider struct {
	vk     wgturn.CredentialsProvider
	yandex wgturn.CredentialsProvider
	logger wgturn.Logger
}

func (r *routedProvider) Fetch(ctx context.Context, hint string, streamID int) (wgturn.Credentials, error) {
	if yandex.IsTelemostLink(hint) {
		return r.yandex.Fetch(ctx, hint, streamID)
	}
	return r.vk.Fetch(ctx, hint, streamID)
}

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

// splitLinks parses the -vk-link CLI argument: a single URL, or several
// URLs joined by commas / whitespace. Empty entries are dropped.
func splitLinks(raw string) []string {
	out := make([]string, 0, 4)
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
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
		cfg.Provider = &routedProvider{
			vk: vk.New(
				vk.WithLogger(a.logger),
				vk.WithCaptchaSolver(pickCaptchaSolver(a.vkChromeURL, a.vkChromeUA, a.logger)),
			),
			yandex: yandex.New(yandex.WithLogger(a.logger)),
			logger: a.logger,
		}
		cfg.Mode = wgturn.ModeVKLink
		links := splitLinks(a.vkLink)
		switch len(links) {
		case 0:
			return cfg, errors.New("-vk-link is empty after parsing")
		case 1:
			cfg.Hint = links[0]
		default:
			cfg.Hints = links
		}

	case a.stubUser != "" || a.stubPass != "" || a.stubServer != "":
		if a.stubUser == "" || a.stubPass == "" || a.stubServer == "" {
			return cfg, errors.New("stub provider requires -stub-user, -stub-pass, -stub-server together")
		}
		cfg.Provider = stub.New(a.stubUser, a.stubPass, a.stubServer)
		if cfg.Mode == "" {
			cfg.Mode = wgturn.ModeStub
		}

	case cfg.Hint != "" && cfg.Mode == wgturn.ModeVKLink:
		// File-driven: VkLink came from #@wgt:VkLink. Route through
		// the same multi-provider dispatcher so a Telemost URL in the
		// config file works without further plumbing.
		cfg.Provider = &routedProvider{
			vk: vk.New(
				vk.WithLogger(a.logger),
				vk.WithCaptchaSolver(pickCaptchaSolver(a.vkChromeURL, a.vkChromeUA, a.logger)),
			),
			yandex: yandex.New(yandex.WithLogger(a.logger)),
			logger: a.logger,
		}

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
