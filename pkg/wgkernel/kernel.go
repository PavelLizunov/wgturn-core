// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgkernel

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"

	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
)

// Kernel is one running WireGuard userspace instance. Construct with
// New, call Start once and Stop once. Kernels are single-shot;
// re-starting requires a fresh Kernel.
type Kernel struct {
	cfg    Config
	tun    tun.Device
	bind   conn.Bind
	logger wgturn.Logger
	dev    *device.Device

	mu      sync.Mutex
	state   state
	turnTun *wgturn.Tunnel // optional — set by WithTurnTunnel
}

type state int

const (
	stateNew state = iota
	stateStarted
	stateStopped
)

// Option mutates a Kernel during construction. Apply via wgkernel.New(cfg, tun, opts...).
type Option func(*Kernel)

// WithLogger overrides the default NoopLogger. The kernel forwards
// wireguard-go's verbose/error log streams to Debugf and Errorf
// respectively.
func WithLogger(l wgturn.Logger) Option {
	return func(k *Kernel) {
		if l != nil {
			k.logger = l
		}
	}
}

// WithBind overrides the default conn.NewDefaultBind(). Useful for
// tests that want a deterministic UDP carrier or for embedding a
// custom transport (e.g. pure-Go memory bind).
func WithBind(b conn.Bind) Option {
	return func(k *Kernel) {
		if b != nil {
			k.bind = b
		}
	}
}

// WithTurnTunnel rewrites every peer's Endpoint to the local listen
// address of the given wgturn.Tunnel before applying the IPC config.
// The tunnel must already be Started so its LocalAddr is known.
//
// Composition shape:
//
//	tn, _ := wgturn.New(turnCfg)
//	_ = tn.Start(ctx)
//	k, _ := wgkernel.New(wgCfg, tunDev, wgkernel.WithTurnTunnel(tn))
//	_ = k.Start(ctx)
func WithTurnTunnel(t *wgturn.Tunnel) Option {
	return func(k *Kernel) { k.turnTun = t }
}

// New constructs a Kernel from a typed Config and a TUN device.
// Validate is called; Start must be called separately to actually
// launch the WG state machine.
func New(cfg Config, t tun.Device, opts ...Option) (*Kernel, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("wgkernel: %w", err)
	}
	if t == nil {
		return nil, errors.New("wgkernel: nil tun device")
	}
	k := &Kernel{
		cfg:    cfg.withDefaults(),
		tun:    t,
		logger: wgturn.NoopLogger{},
		bind:   conn.NewDefaultBind(),
	}
	for _, o := range opts {
		o(k)
	}
	return k, nil
}

// Start brings up the WG device. Returns once the IPC config has been
// applied and the device transitions to "up". Subsequent Reads /
// Writes on the TUN drive the data plane.
//
// ctx is honored only during Start setup; once running the device is
// driven by goroutines internal to wireguard-go which exit on Stop.
func (k *Kernel) Start(ctx context.Context) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.state != stateNew {
		return errors.New("wgkernel: already started")
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	// Apply WithTurnTunnel rewrite if requested.
	cfg := k.cfg
	if k.turnTun != nil {
		addr := k.turnTun.LocalAddr()
		if addr == nil {
			return errors.New("wgkernel: WithTurnTunnel: tunnel not started (LocalAddr is nil)")
		}
		newPeers := make([]PeerConfig, len(cfg.Peers))
		for i, p := range cfg.Peers {
			p.Endpoint = addr.String()
			newPeers[i] = p
		}
		cfg.Peers = newPeers
	}

	ipc, err := cfg.IPC()
	if err != nil {
		return fmt.Errorf("wgkernel: build IPC: %w", err)
	}

	dev := device.NewDevice(k.tun, k.bind, &device.Logger{
		Verbosef: func(format string, args ...any) { k.logger.Debugf("[wg] "+format, args...) },
		Errorf:   func(format string, args ...any) { k.logger.Errorf("[wg] "+format, args...) },
	})

	if err := dev.IpcSet(ipc); err != nil {
		dev.Close()
		return fmt.Errorf("wgkernel: IpcSet: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return fmt.Errorf("wgkernel: device.Up: %w", err)
	}

	k.dev = dev
	k.state = stateStarted
	k.logger.Infof("wgkernel: device up, listen_port=%d, peers=%d", cfg.ListenPort, len(cfg.Peers))
	return nil
}

// Stop tears down the device. Idempotent.
func (k *Kernel) Stop() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.state != stateStarted {
		return nil
	}
	k.state = stateStopped
	k.dev.Close()
	k.logger.Infof("wgkernel: device closed")
	return nil
}

// ActualListenPort returns the UDP port the bind ended up on. Useful
// when ListenPort was zero in the Config (let the kernel pick) and
// the caller needs to point a peer at the chosen port. Returns 0
// before Start.
func (k *Kernel) ActualListenPort() (uint16, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.state != stateStarted || k.dev == nil {
		return 0, errors.New("wgkernel: not started")
	}
	got, err := k.dev.IpcGet()
	if err != nil {
		return 0, fmt.Errorf("ipc get: %w", err)
	}
	for _, line := range strings.Split(got, "\n") {
		if rest, ok := strings.CutPrefix(line, "listen_port="); ok {
			n, err := strconv.ParseUint(strings.TrimSpace(rest), 10, 16)
			if err != nil {
				return 0, fmt.Errorf("parse listen_port: %w", err)
			}
			return uint16(n), nil
		}
	}
	return 0, errors.New("listen_port not found in IpcGet output")
}

// Stats returns a coarse summary of the WG device's runtime state.
// Currently we extract the last-handshake timestamp per peer; this
// is the canonical "is the tunnel actually up" signal.
func (k *Kernel) Stats() (Stats, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.state != stateStarted || k.dev == nil {
		return Stats{}, errors.New("wgkernel: not started")
	}
	got, err := k.dev.IpcGet()
	if err != nil {
		return Stats{}, fmt.Errorf("ipc get: %w", err)
	}

	out := Stats{}
	var cur PeerStats
	for _, line := range strings.Split(got, "\n") {
		switch {
		case strings.HasPrefix(line, "public_key="):
			if cur.PublicKey != "" {
				out.Peers = append(out.Peers, cur)
			}
			cur = PeerStats{PublicKey: strings.TrimPrefix(line, "public_key=")}
		case strings.HasPrefix(line, "last_handshake_time_sec="):
			cur.LastHandshakeTimeSec, _ = strconv.ParseInt(strings.TrimPrefix(line, "last_handshake_time_sec="), 10, 64)
		case strings.HasPrefix(line, "rx_bytes="):
			cur.RxBytes, _ = strconv.ParseUint(strings.TrimPrefix(line, "rx_bytes="), 10, 64)
		case strings.HasPrefix(line, "tx_bytes="):
			cur.TxBytes, _ = strconv.ParseUint(strings.TrimPrefix(line, "tx_bytes="), 10, 64)
		}
	}
	if cur.PublicKey != "" {
		out.Peers = append(out.Peers, cur)
	}
	return out, nil
}

// Stats is a snapshot of the WG device's runtime counters.
type Stats struct {
	Peers []PeerStats
}

// PeerStats is per-peer counters from IpcGet.
type PeerStats struct {
	PublicKey            string // hex
	LastHandshakeTimeSec int64
	RxBytes              uint64
	TxBytes              uint64
}
