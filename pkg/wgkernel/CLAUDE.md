# pkg/wgkernel — embedded WireGuard userspace

Wraps `golang.zx2c4.com/wireguard` (wireguard-go) so an application can
run a full WireGuard endpoint **inside the same Go process** — no
external `wg-quick` / `wireguard-tools` needed.

## Status

✅ Code exists and works. Test suite includes a real WG handshake between
two in-process kernels using paired memory TUNs and curve25519 keys.

❌ Not yet wired into `cmd/wgturn-cli`. ROADMAP N1 is the integration
work — adding a `connect` subcommand that brings up wgkernel + wgturn
together so end users don't need separate `wg-quick`.

## What's here

- `kernel.go` — `Kernel` runtime handle, `New(cfg, tunDev, opts...) →
  Start(ctx) → Stats() → Stop()`.
- `config.go` — `Config` (PrivateKey, Address, Peers) and `PeerConfig`.
- `tun.go` — three TUN factories:
  - `NewSystemTUN(name, mtu)` — Linux/Win/macOS desktop, root required
  - `NewTUNFromFD(fd, mtu)` — Android `VpnService.protect` / iOS
    `NEPacketTunnelProvider`
  - `NewMemoryTUNPair(name, mtu, buf)` — tests
- `kernel_test.go` — full WG-handshake-between-two-instances coverage
  using paired MemoryTUN.

## The `WithTurnTunnel` option

`wgkernel.WithTurnTunnel(tn)` rewrites every peer Endpoint to
`tn.LocalAddr()` so the embedded WG sends packets to wgturn instead of
out to the internet. This is what enables single-process VPN.

## Test coverage

```bash
go test ./pkg/wgkernel/...
```

The flagship test `TestRealHandshake` brings up two kernels with
paired memory TUNs, verifies they exchange a real WG handshake, then
shuts down cleanly. Completes in ~100 ms.

## Don't regress

- Don't change the TUN factory signatures — Android/iOS embedders use
  `NewTUNFromFD` directly, can't easily migrate.
- Don't break `WithTurnTunnel` — it's the integration point with the
  other half of the project.
- `MemoryTUN.Close` must unblock its own `Read` goroutine via the
  done-chan pattern. There was a regression here once (commit f32f328);
  the test catches it but be careful.
