# wgturn-core

`wgturn-core` is an embeddable Go library that tunnels arbitrary UDP
traffic — typically WireGuard — through a public TURN relay using
DTLS 1.2 obfuscation and STUN ChannelData. It is the platform-agnostic
extraction of the proxy kernel found in
[`kiper292/wireguard-turn-android`](https://github.com/kiper292/wireguard-turn-android),
de-Androidified so the same Go code can ship inside desktop binaries,
iOS/Android apps via gomobile, and (eventually) inside `sing-box` as a
custom endpoint.

> **Educational and research purposes only.** Using third-party TURN
> infrastructure to relay traffic that is not a real-time-comms call may
> violate the terms of service of the providers involved. See `NOTICE`.

## Status

| | |
|---|---|
| Version | `v0.0.1-alpha` (pre-1.0; API may change) |
| Go      | 1.25+ |
| License | Apache-2.0 |
| Tests   | `go test ./... -race` clean (incl. real `pion/turn` server in-proc) |

## Project layout

```
pkg/
  wgturn/              Public API: Tunnel, Config, SocketProtector, Logger,
                       CredentialsProvider, Stats. Stable surface; subpackages
                       under provider/ host concrete creds providers.
    provider/stub/     Trivial fixed-creds provider for tests and smoke runs.
  wgconf/              Parser for #@wgt: metadata in WireGuard .conf files
                       (the kiper292/wireguard-turn-android convention).

internal/
  proxy/               Hub + Stream: the actual TURN proxy. Multi-stream
                       round-robin, per-stream DTLS, optional Session-ID +
                       Stream-ID handshake (proxy_v2 / proxy_v1 / wireguard
                       framing modes).
  creds/               Per-stream-group credentials cache with TTL and
                       auth-error invalidation.

cmd/
  wgturn-cli/          Reference desktop binary (config-driven or flag-driven).
```

## Architecture

```
┌──────────────────┐  UDP    ┌──────────────────────────────────────────┐
│ WireGuard client │ ──────▶ │ wgturn Hub (this library)                │
│ Endpoint:        │ ◀────── │  - listens on ListenAddr (e.g. 127:9000) │
│  127.0.0.1:9000  │         │  - N parallel "streams"                  │
└──────────────────┘         │  - RR scheduler                          │
                             └────────┬─────────────────────────────────┘
                                      │ each stream:
                                      ▼
   ┌────────────┐    DTLS 1.2     ┌──────────────────┐
   │  TURN      │ ◀───wrapped────▶│ pion/turn client │
   │  server    │                 │ + STUN ChannelData│
   │ (VK / OK / │                 └────────┬─────────┘
   │  Yandex /  │                          │ Allocate()
   │  WB)       │ ◀── ChannelData          │
   └────┬───────┘                          ▼
        │ relayed UDP                  relayConn
        ▼
   ┌────────────────────┐
   │ wgturn server      │
   │ (PeerAddr,         │
   │  e.g. your VPS)    │
   │ unwraps DTLS,      │
   │ aggregates streams │
   │ per Session-ID,    │
   │ forwards to local  │
   │ WireGuard daemon   │
   └────────────────────┘
```

Three framing modes are wire-compatible with `kiper292/vk-turn-proxy` v2:

| `PeerType` | DTLS | Session-ID handshake | Use case |
|---|---|---|---|
| `proxy_v2` (default) | yes | yes (16-byte UUID + 1-byte stream-id) | multi-user server, recommended |
| `proxy_v1` | yes | no | legacy single-user servers |
| `wireguard` | no | no | direct relay, ban-prone, debugging |

## Quick start (desktop)

```go
package main

import (
    "context"
    "log"

    "github.com/slovn/wgturn-core/pkg/wgturn"
    "github.com/slovn/wgturn-core/pkg/wgturn/provider/stub"
)

func main() {
    tn, err := wgturn.New(wgturn.Config{
        PeerAddr:   "vps.example.com:56000",
        ListenAddr: "127.0.0.1:9000",
        Streams:    4,
        PeerType:   wgturn.PeerTypeProxyV2,
        Provider:   stub.New("u", "p", "turn.example.com:3478"),
        Protector:  wgturn.NoopProtector{},
        Logger:     wgturn.StdLogger{MinLevel: wgturn.LevelInfo},
    })
    if err != nil {
        log.Fatal(err)
    }

    if err := tn.Start(context.Background()); err != nil {
        log.Fatal(err)
    }
    defer tn.Stop()

    // Point your WireGuard client at 127.0.0.1:9000.
    select {}
}
```

For a fully-fledged binary (config-file driven, signal handling, stats
poller) see [`cmd/wgturn-cli`](cmd/wgturn-cli/main.go).

## `#@wgt:` config metadata

`pkg/wgconf` parses standard `wg-quick` config files extended with a
single-prefix metadata convention:

```ini
[Interface]
PrivateKey = ...
Address    = 10.7.0.2/32
#@wgt:EnableTURN     = true
#@wgt:Mode           = vk_link
#@wgt:VkLink         = https://vk.com/call/join/abcdef
#@wgt:PeerType       = proxy_v2
#@wgt:Streams        = 4
#@wgt:WatchdogTimeout= 30
#@wgt:Peer           = vps.example.com:56000
#@wgt:LocalListen    = 127.0.0.1:9000

[Peer]
PublicKey  = ...
Endpoint   = 127.0.0.1:9000  ; client points WG at the local hub
AllowedIPs = 0.0.0.0/0
```

Vanilla WireGuard tools see only comments; `wgturn-core` reads the
metadata.

## Platform integration

The **`SocketProtector`** interface is the only platform-specific seam.

| Platform | Protector implementation |
|---|---|
| Linux/Win/macOS | `wgturn.NoopProtector{}` |
| Android | call `VpnService.protect(fd)` via JNI |
| iOS | rely on `NEPacketTunnelProvider`'s automatic exclusion |

A `FuncProtector` adapter is provided for one-line glue.

## Building / testing

```bash
make            # vet + test
make build      # go build ./...
make race       # go test -race (needs CGO + gcc)
make cover      # coverage
make cli        # bin/wgturn-cli

# Override Go toolchain location (e.g. local install):
make GO=/tmp/go-toolchain/go/bin/go test
```

## What's NOT in v0.0.1-alpha

- Real `vk_link` and `wb` credentials providers. These ship next as
  separate sub-modules so the core dependency surface stays small (the
  upstream uses `bogdanfinn/tls-client` + utls fork for browser-fingerprint
  bypass, ~12 transitive deps; we keep that out of the core build).
- DNS-cascade resolver (kiper292's UDP→DoH→DoT failover for credential
  fetches over a VPN). It will live next to the providers that need it.
- gomobile bindings (Android `.aar` / iOS `.xcframework`). They are
  trivial wrappers around `pkg/wgturn`; will land as a sibling repo.
- A full `pkg/wgkernel` that bundles `wireguard-go` userspace into the
  same process so callers don't have to run a separate WG client. The
  proxy library is sufficient for now since most real apps already
  embed `wireguard-go` themselves.

## Provenance & licensing

`wgturn-core` is licensed Apache-2.0. It is derived from work in the
"VK TURN proxy" lineage; the full attribution chain is in [`NOTICE`](NOTICE).
The wire protocol matches [`kiper292/vk-turn-proxy`](https://github.com/kiper292/vk-turn-proxy)
(GPL-3.0) on the server side; **this repository does not vendor or copy
GPL-3.0 sources**, only re-implements the same protocol from public
documentation and observable wire format.

## See also

- [kiper292/vk-turn-proxy](https://github.com/kiper292/vk-turn-proxy) — server
- [kiper292/wireguard-turn-android](https://github.com/kiper292/wireguard-turn-android) — Android wrapper, source of inspiration
- [cacggghp/vk-turn-proxy](https://github.com/cacggghp/vk-turn-proxy) — original VK adaptation
- [KillTheCensorship/Turnel](https://github.com/KillTheCensorship/Turnel) — original Yandex Telemost variant (archived 2026-01)
