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
    provider/vk/       Real VK Calls provider — fetches anonymous TURN
                       creds from a vk.com/call/join/<id> link.
  wgconf/              Parser for #@wgt: metadata in WireGuard .conf files
                       (the kiper292/wireguard-turn-android convention).
  wgkernel/            Embedded wireguard-go userspace. Bring up a real
                       WG endpoint inside the same process; pair with
                       a wgturn.Tunnel for a single-binary VPN client.

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

## VK provider (real credentials)

`pkg/wgturn/provider/vk` fetches anonymous TURN credentials from VK
Calls' public API given a regular invite link. The 5-step flow
(matching `kiper292/wireguard-turn-android` v=5.275) is:

1. `login.vk.ru/?act=get_anonym_token` → primary anonymous token
2. `api.vk.ru/method/calls.getCallPreview` (best-effort fingerprinting step)
3. `api.vk.ru/method/calls.getAnonymousToken` → call-scoped token (**captcha-gated**)
4. `calls.okcdn.ru/fb.do auth.anonymLogin` → OK CDN session_key
5. `calls.okcdn.ru/fb.do vchat.joinConversationByLink` → TURN credentials

To pass VK's bot heuristics the provider:

- Uses [`refraction-networking/utls`](https://github.com/refraction-networking/utls)
  for the TLS handshake, mimicking Chrome 133 JA3. Without this the
  default Go `crypto/tls` fingerprint trips an immediate captcha
  even before any HTTP-level checks.
- Sends the full Chrome cross-origin POST header set (`sec-ch-ua*`,
  `Sec-Fetch-*`, `Origin`, `Referer`, `Accept-Encoding: gzip, deflate, br, zstd`).
- Decompresses gzip / deflate / brotli / zstd response bodies in the
  custom Transport (stdlib's auto-decompression doesn't apply to our
  utls path).
- Rotates UA + Sec-CH-UA hints across the small "modern desktop browser"
  pool in `profiles.go`.

### Captcha handling

VK currently gates step 3 (`calls.getAnonymousToken`) behind a captcha
for every fresh anonymous request. The provider supports it via
`vk.WithCaptchaSolver(...)`:

```go
type CaptchaSolver interface {
    Solve(ctx context.Context, ch CaptchaChallenge) (Solution, error)
}
```

Inputs (`CaptchaChallenge`): `SID`, `ImgURL` (text-captcha image),
`RedirectURI` (slider-captcha page), `Attempt`. Outputs (`Solution`):
either `Key` (typed text from `ImgURL`) or `SuccessToken` (from the
slider page). The library does **not** ship a solver — embedders
choose how to surface the challenge (terminal prompt, mobile UI,
2captcha API, 2captcha-style services, etc.).

**Important caveat**: VK currently rotates between text and slider
captcha modes. Slider mode requires a JS/DOM solver (out of scope for
the core; see `kiper292/wireguard-turn-android` `slider_captcha.go`
for ~600 LOC of reference). When VK is in text-captcha mode (which
happens periodically), the bundled stdio solver in `cmd/wgturn-cli`
works end-to-end.

### Usage

```go
import (
    "github.com/slovn/wgturn-core/pkg/wgturn"
    "github.com/slovn/wgturn-core/pkg/wgturn/provider/vk"
)

cfg := wgturn.Config{
    PeerAddr:   "vps.example.com:56000",
    ListenAddr: "127.0.0.1:9000",
    Streams:    4,
    Mode:       wgturn.ModeVKLink,
    Hint:       "https://vk.com/call/join/abcdef",   // call invite
    Provider: vk.New(
        vk.WithLogger(myLogger),
        vk.WithCaptchaSolver(myStdioSolver{}),       // stdin/stdout prompt, etc.
    ),
    Protector:  wgturn.NoopProtector{},
}
```

CLI:

```sh
wgturn-cli -peer vps.example.com:56000 \
           -vk-link https://vk.com/call/join/abcdef \
           -streams 4
```

**Captcha handling.** When VK challenges a request the provider returns
`wgturn.ErrCaptchaRequired`. The library does not solve captchas —
embedders are expected to surface this to the user (UI prompt, retry
later, manual override, etc.). A solver may land later as
`pkg/wgturn/provider/vk/captcha`, kept opt-in to avoid pulling the
upstream's `bogdanfinn/tls-client` + uTLS dep tree into core.

## Embedded WireGuard kernel (`pkg/wgkernel`)

`pkg/wgkernel` wraps [`golang.zx2c4.com/wireguard`](https://git.zx2c4.com/wireguard-go/)
so an application can run a full WireGuard endpoint **in the same Go
process** as wgturn-core, with no external `wg-quick` or system
WireGuard daemon. Pair it with a `wgturn.Tunnel` and you get a
single-binary VPN client.

```go
import (
    "github.com/slovn/wgturn-core/pkg/wgkernel"
    "github.com/slovn/wgturn-core/pkg/wgturn"
    "github.com/slovn/wgturn-core/pkg/wgturn/provider/vk"
)

// 1. Bring up the TURN proxy.
tn, _ := wgturn.New(wgturn.Config{
    PeerAddr:   "vps.example.com:56000",
    ListenAddr: "127.0.0.1:0",
    Streams:    4,
    Mode:       wgturn.ModeVKLink,
    Hint:       "https://vk.com/call/join/abcdef",
    Provider:   vk.New(),
    Protector:  wgturn.NoopProtector{},
})
_ = tn.Start(ctx)

// 2. Bring up the WireGuard kernel; WithTurnTunnel rewrites every
//    peer Endpoint to the tunnel's local address.
tunDev, _ := wgkernel.NewSystemTUN("wg0", 1280)
k, _ := wgkernel.New(wgkernel.Config{
    PrivateKey: "<base64>",
    Address:    []netip.Prefix{netip.MustParsePrefix("10.7.0.2/32")},
    Peers: []wgkernel.PeerConfig{{
        PublicKey:  "<server pubkey base64>",
        AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
        PersistentKeepalive: 25 * time.Second,
    }},
}, tunDev, wgkernel.WithTurnTunnel(tn))
_ = k.Start(ctx)
```

Three TUN factories are provided:

| Factory | When to use |
|---|---|
| `NewSystemTUN(name, mtu)` | Linux/Windows/macOS desktop (root required) |
| `NewTUNFromFD(fd, mtu)` | Android `VpnService` / iOS `NEPacketTunnelProvider` |
| `NewMemoryTUNPair(...)` | Tests; no privileges, no OS interface |

End-to-end coverage in `pkg/wgkernel/kernel_test.go` includes a real
WG handshake between two in-process kernels using paired memory TUNs
and curve25519 keys — completes in ~100 ms.

## What's NOT yet in v0.0.1-alpha

- WB Stream API provider (analogous to VK's, different upstream).
- DNS-cascade resolver (kiper292's UDP→DoH→DoT failover for credential
  fetches over a VPN). Lands when we test on Android.
- gomobile bindings (Android `.aar` / iOS `.xcframework`). They are
  trivial wrappers around `pkg/wgturn` + `pkg/wgkernel`; will land as
  a sibling repo.
- Routing / DNS / firewall management around the system TUN — that is
  intentionally the host application's responsibility (it knows the
  platform conventions; we don't).

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

Last verified: 2026-05-04T18:28:41+00:00
