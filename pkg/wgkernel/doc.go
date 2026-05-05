// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// Package wgkernel embeds wireguard-go's userspace WireGuard
// implementation into the same process as wgturn-core. It exists so
// applications can ship a single binary that does both:
//
//   - Tunnel WireGuard traffic through a public TURN relay
//     (the wgturn-core proxy hub).
//   - Run the WireGuard endpoint itself, with no external wg-quick
//     or system-level WireGuard daemon required.
//
// # Pieces
//
// A Kernel wraps three things:
//
//  1. A *device.Device from golang.zx2c4.com/wireguard/device — the
//     userspace WG state machine.
//  2. A tun.Device — the IP-layer interface. Production code passes a
//     real OS TUN (NewSystemTUN) or, on mobile, an FD handed in by
//     the host VPN service (NewTUNFromFD). Tests use the in-memory
//     paired TUN from this package.
//  3. A conn.Bind — the UDP-layer carrier. Defaults to wireguard-go's
//     standard bind, which opens a UDP socket on the host. For TURN
//     integration, point each peer's Endpoint at the local address of
//     a wgturn.Tunnel; the WithTurnTunnel option does this automatically.
//
// # Lifecycle
//
//	k, err := wgkernel.New(cfg, tunDevice)
//	if err != nil { ... }
//	if err := k.Start(ctx); err != nil { ... }
//	defer k.Stop()
//
// # Composition with wgturn
//
//	tn, _ := wgturn.New(turnCfg)
//	_ = tn.Start(ctx)
//
//	k, _ := wgkernel.New(wgCfg, tunDevice, wgkernel.WithTurnTunnel(tn))
//	_ = k.Start(ctx)
//	// All Wg traffic now exits via tn → TURN relay → remote wgturn-server.
//
// # Stability
//
// Pre-1.0. The Config shape is intentionally narrower than the full
// WireGuard wg-quick syntax — we accept what we know how to validate.
package wgkernel
