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

	"github.com/PavelLizunov/wgturn-core/pkg/wgconf"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturnsrv"
)

// runServe is the `wgturn-cli serve <server.conf>` subcommand:
// terminates wgturn proxy_v2 sessions on a UDP listener and forwards
// inner payload to a configurable backend (typically a local
// WireGuard daemon listening on 127.0.0.1:51820).
//
// Lifecycle, in order:
//
//  1. parse flags + .conf
//  2. resolve listen + backend (CLI overrides win over .conf metadata)
//  3. build wgturnsrv.Server, Start it
//  4. stats loop (optional)
//  5. wait for SIGINT/SIGTERM, then Stop the server
//
// The server is single-binary, sing-box-style: one process, one
// listener, no external daemon. To replace the legacy GPL upstream
// on a VPS, drop the binary, point Backend at the local WG daemon's
// listen port, and run.
func runServe(args []string) error {
	fs := flag.NewFlagSet("wgturn-cli serve", flag.ContinueOnError)
	var (
		configPath = fs.String("config", "",
			"server config file with #@wgt:EnableServer/Listen/Backend (required positional or via -config)")
		listenOverride = fs.String("listen", "",
			"override #@wgt:Listen (e.g. :56001 for parallel-port soak)")
		backendOverride = fs.String("backend", "",
			"override #@wgt:Backend (e.g. udp:127.0.0.1:51820)")
		statsEvery = fs.Duration("stats", 30*time.Second,
			"stats print interval (0 disables)")
		verbose = fs.Bool("v", false, "verbose logging")
	)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(),
			"Usage: wgturn-cli serve [flags] <server.conf>\n\n"+
				"Run the server-side wgturn proxy: terminates DTLS sessions on a UDP\n"+
				"listener and forwards inner payload to a configurable Backend.\n\n"+
				"Config keys (in #@wgt: metadata):\n"+
				"  EnableServer = true\n"+
				"  Listen       = :56000\n"+
				"  Backend      = udp:127.0.0.1:51820  ; or 'wgkernel' (experimental)\n\n"+
				"Flags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Allow positional config path: `wgturn-cli serve myserver.conf`.
	if *configPath == "" && fs.NArg() == 1 {
		*configPath = fs.Arg(0)
	}
	if *configPath == "" {
		fs.Usage()
		return errors.New("serve: config path is required (positional or -config)")
	}

	logger := wgturn.StdLogger{MinLevel: wgturn.LevelInfo}
	if *verbose {
		logger.MinLevel = wgturn.LevelDebug
	}

	settings, err := parseWGConfig(*configPath)
	if err != nil {
		return err
	}
	if !settings.EnableServer {
		return errors.New("serve: #@wgt:EnableServer must be true in the config")
	}

	// Resolve listen + backend with CLI overrides.
	listen := settings.Listen
	if *listenOverride != "" {
		listen = *listenOverride
	}
	if listen == "" {
		return errors.New("serve: #@wgt:Listen is required (or pass --listen)")
	}

	backendSpec := settings.Backend
	if *backendOverride != "" {
		backendSpec = *backendOverride
	}
	backend, err := buildServerBackend(backendSpec, logger)
	if err != nil {
		return err
	}

	srv, err := wgturnsrv.New(wgturnsrv.Config{
		ListenAddr: listen,
		Backend:    backend,
		Logger:     logger,
	})
	if err != nil {
		return fmt.Errorf("serve: new: %w", err)
	}

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	if err := srv.Start(rootCtx); err != nil {
		return fmt.Errorf("serve: start: %w", err)
	}
	defer func() {
		if err := srv.Stop(); err != nil {
			logger.Warnf("server stop: %v", err)
		}
	}()

	log.Printf("serve: listening on %s; backend=%s", srv.LocalAddr(), backendSpec)

	if *statsEvery > 0 {
		go runServerStatsLoop(rootCtx, srv, *statsEvery)
	}

	select {
	case sig := <-sigs:
		log.Printf("serve: received %v; shutting down", sig)
	case <-rootCtx.Done():
		log.Print("serve: context cancelled; shutting down")
	}
	return nil
}

// buildServerBackend turns a #@wgt:Backend spec into a concrete
// wgturnsrv.Backend. Today we recognise:
//
//	udp:host:port  -> UDPBackend (production path: separate WG daemon)
//	wgkernel       -> rejected: requires extra plumbing (TUN, sudo,
//	                  Iface/Peer wiring) that S6 doesn't pull in.
//	                  Slated for a future "all-in-one" mode.
func buildServerBackend(spec string, _ wgturn.Logger) (wgturnsrv.Backend, error) {
	kind, addr, err := wgconf.ParseBackendSpec(spec)
	if err != nil {
		return nil, fmt.Errorf("serve: backend: %w", err)
	}
	switch kind {
	case wgconf.BackendUDP:
		return wgturnsrv.UDPBackend{Addr: addr}, nil
	case wgconf.BackendWGKernel:
		return nil, errors.New("serve: Backend=wgkernel is not yet wired into the CLI; " +
			"use Backend=udp:127.0.0.1:51820 with a separate wireguard-tools setup for now")
	default:
		// ParseBackendSpec already returned every known kind; defensive
		// fall-through covers a future kind added without updating here.
		return nil, fmt.Errorf("serve: unsupported Backend kind %q", kind)
	}
}

// runServerStatsLoop logs SessionsActive / StreamsActive every period
// so an operator can see traffic shape without scraping logs.
func runServerStatsLoop(ctx context.Context, s *wgturnsrv.Server, period time.Duration) {
	tick := time.NewTicker(period)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			st, err := s.Stats()
			if err != nil {
				continue
			}
			log.Printf("serve.stats: sessions=%d streams=%d", st.SessionsActive, st.StreamsActive)
		}
	}
}
