# wgturn-core ARCHITECTURE

Why the code is shaped the way it is. Read this when extending the
project — adding a new provider, a new captcha solver, a new framing
mode, etc.

## High-level dataflow

```
┌──────────────────┐  WG/UDP  ┌──────────────────────────────────────┐
│ WireGuard client │ ───────▶ │ wgturn Hub (this library)            │
│ (system or       │ ◀─────── │  - listens on Config.ListenAddr      │
│  pkg/wgkernel)   │          │  - N parallel "streams" (round-robin)│
└──────────────────┘          │  - cred cache + auth-error retry     │
                              └────────┬─────────────────────────────┘
                                       │ each stream:
                                       ▼
   ┌────────────┐    DTLS 1.2     ┌──────────────────┐
   │  TURN      │ ◀──wrapped─────▶│ pion/turn client │
   │  server    │                 │ + STUN ChannelData│
   │ (VK Calls /│                 └────────┬─────────┘
   │  Yandex /  │                          │ Allocate()
   │  ...)      │ ◀── ChannelData          │
   └────┬───────┘                          ▼
        │ relayed UDP                  relayConn
        ▼
   ┌────────────────────┐
   │ wgturn server      │   `pkg/wgturnsrv` — Apache-2.0, in-tree
   │ (PeerAddr, your    │   (or legacy `slovn/wgturn-server` GPL fork
   │  foreign VPS)      │    pre-N8). Same wire format either way.
   │                    │   Aggregates streams by Session-ID,
   │                    │   forwards reassembled WG to local WG daemon.
   └────────────────────┘
```

## Layers (bottom-up)

### `pkg/wgturn` — public core

The stable API surface. Embedders import this; nothing else is
mandatory.

Key types:
- `Config` — declarative struct for everything: PeerAddr, ListenAddr,
  Streams, PeerType, Mode, Hint/Hints, Provider, Protector, Logger.
- `Tunnel` — runtime handle; lifecycle: `New(cfg) → Start(ctx) →
  Stats() → Stop()`.
- `CredentialsProvider` interface — implemented by provider/* packages.
  `Fetch(ctx, hint, streamID) (Credentials, error)`.
- `CaptchaSolver` is provider-specific — VK has `vk.CaptchaSolver`.
- `Logger` interface — Debugf/Infof/Warnf/Errorf. `StdLogger`,
  `NoopLogger` provided.
- `SocketProtector` — Android `VpnService.protect(fd)` hook;
  `NoopProtector` for desktop/server.
- Public errors: `ErrCaptchaRequired`, `ErrAuthFailure`, `ErrInvalidLink`.

### `internal/proxy` — Hub + Stream

Implements the actual TURN proxy. Internal because the wire format
should be stable but the implementation isn't part of the API contract.

- `Hub.Start()` opens `localConn` (UDP), spawns N `stream` goroutines.
- Each `stream` allocates a TURN session, optionally wraps in DTLS
  + Session-ID/Stream-ID framing, pumps packets bidirectionally.
- Packet routing uses a round-robin scheduler keyed off the local
  client's source addr.
- `peerType` selects framing:
  - `proxy_v2` (default) — DTLS + Session-ID handshake (16-byte UUID
    + 1-byte stream-id). Multi-stream-aware. Use for production.
  - `proxy_v1` — DTLS only, no session-id (legacy single-user servers).
  - `wireguard` — raw WG, no DTLS. Debugging only.

### `internal/framing` — wire-format primitives shared by both sides

Tiny package owning the proxy_v2 on-the-wire contract: the 17-byte
session+stream preamble (`WriteHandshake` / `ReadHandshake`,
constants `SessionIDSize=16`, `HandshakeSize=17`), and the DTLS
config builder (`NewDTLSConfig(role, cert)` plus
`GenerateCertificate`). Imported by both `internal/proxy` (client)
and `pkg/wgturnsrv` (server) so any future tweak to the wire format
lands in exactly one place.

`role` is a typed `Role` enum (`RoleClient` / `RoleServer`) — server
side gets `RandomCIDGenerator(8)` (CIDSize=8) for connection-IDs,
client side gets `OnlySendCIDGenerator()` (sends a CID but doesn't
need to receive one).

### `pkg/wgturnsrv` — server-side proxy (Apache-2.0)

The server-side counterpart to `pkg/wgturn`. Imported by
`cmd/wgturn-cli serve` and any embedder running their own VPS exit
node.

Key types:
- `Server` — runtime handle; `New(cfg) → Start(ctx) → Stats() → Stop()`.
- `Config` — declarative struct: `ListenAddr`, `Backend`, `Logger`,
  three timeout overrides (`HandshakeTimeout`, `StreamReadTimeout`,
  `BackendWriteTimeout`).
- `Backend` interface — `Open(ctx, sessionID) (net.Conn, error)`.
  Called once per session on first stream. Returned conn is owned
  by that session.
- `UDPBackend{Addr}` — production path: dials a fresh per-session
  UDP socket to a local WG daemon. Per-session sources keep
  WireGuard's session table happy.
- `WGKernelBackend` — bridges into an in-process `wgkernel.Kernel`
  via a custom `conn.Bind`. Single-session today; multi-peer
  fan-out is future work. Used by the in-process pair test (S5)
  and by single-client all-in-one experiments.

Lifecycle inside `Server`:
1. Start binds DTLS listener, spawns accept loop.
2. Per-conn `handleConn`: SetReadDeadline(HandshakeTimeout) →
   `framing.ReadHandshake` → `getOrCreateSession` (lazy backend
   Open) → `addStream` (eviction-on-duplicate-streamID) → blocking
   `runStream` until conn closes.
3. Per-session `runBackend` (1 goroutine): reads from backend,
   round-robins to streams via atomic counter. StreamReadTimeout
   per Read; expired = WG side silent = teardown.
4. Stop snapshots active sessions and `terminate()`s each one
   idempotently — closes backend, cancels ctx, closes streams,
   removes from map.

Apache-2.0 cleanliness: zero copied code from
`kiper292/vk-turn-proxy` (GPL). Wire format matches by spec — the
17-byte preamble, the cipher suite, the 5-min timeouts — but every
byte was written from a blank buffer. See `docs/N8-SERVER-PLAN.md`
"Mission constraints".

### `internal/creds` — credentials cache

- Per-stream-group cache (default `StreamsPerCred=4`).
- TTL = `creds.ExpiresIn` if set, else 10 min default with 60 s margin.
- Auth-error invalidation: 3 errors in 10 sec window invalidate the
  cached creds and force a refetch.
- `fetchMu` serialises concurrent fetches to a single in-flight call,
  preventing thundering herd against the upstream API.
- Cache is keyed by `(groupID, hint)` — different hints in same group
  produce independent entries. This is what makes multi-link work.

### `pkg/wgconf` — config-file parser

Parses standard `wg-quick` config files extended with `#@wgt:` metadata
comments. Used by `cmd/wgturn-cli -config <path>`.

### `pkg/wgkernel` — embedded WireGuard userspace

Wraps `golang.zx2c4.com/wireguard` (wireguard-go) so an application can
bring up a WireGuard endpoint **inside the same Go process**, without
needing system `wg-quick` / `wireguard-tools`. Pair with a
`wgturn.Tunnel` and you get a single-process VPN client.

`wgkernel.WithTurnTunnel(tn)` rewrites every peer Endpoint to the
tunnel's local listen address, so the WG kernel sends packets to
wgturn instead of out to the internet.

TUN factories: `NewSystemTUN(name, mtu)` (root required),
`NewTUNFromFD(fd, mtu)` (Android `VpnService` / iOS
`NEPacketTunnelProvider`), `NewMemoryTUNPair(...)` (tests).

### `pkg/wgturn/provider/*` — credential providers

Each provider implements `wgturn.CredentialsProvider`:
- `provider/stub` — fixed creds, for tests. Tiny, no deps.
- `provider/vk` — VK Calls anonymous-token API. Real, used in production.
- `provider/yandex` — Yandex Telemost. Cred-fetch correct but TURN
  walled-garden — see `docs/FINDINGS.md`.

Adding a new provider: implement `Fetch(ctx, hint, streamID)`. Don't
forget to handle context cancellation.

### `pkg/wgturn/provider/vk/captchasolve` — pluggable captcha solvers

Subpackage so the websocket dep is opt-in. Embedders that ship their
own solver (2captcha, in-app webview) ignore it.

- `CDPSolver` — drives external Chrome via DevTools Protocol. Works
  against real VK in ~1 sec. **Current production solver.**
- `ChainSolver` — try a list of inner solvers in order, return first
  success. Use to compose: native → CDP → 2captcha → stdin.
- (planned) `native` — pure-Go slider solver, no Chrome.
- (planned) `embedded` — bundled Chromium via go:embed, ~80 MB.
- (planned) `twocaptcha` — paid API client.

### `cmd/wgturn-cli` — reference CLI

Thin wrapper. Not part of the API. Useful for testing and as a
zero-config tool for tech users. Three subcommands plus the legacy
flag-driven mode:

1. `wgturn-cli connect <wireguard.conf>` — single-command client:
   wgturn hub + embedded `pkg/wgkernel` + (Linux) host-side
   networking. Auto-spawns Chrome for the CDP captcha solver.
2. `wgturn-cli serve <server.conf>` — single-command server:
   `pkg/wgturnsrv.Server` against a `Backend` derived from
   `#@wgt:Backend` in the .conf. The same binary as the client,
   sing-box-style.
3. `-config <path>` legacy mode — wgconf .conf with `#@wgt:`
   metadata, hub only (the user brings up WG separately).
4. Direct flags (`-peer`, `-vk-link`, `-streams`, etc.) — same as
   legacy, no .conf file required.

## Why these specific abstractions

### Why is `Provider` an interface?
- Multiple credential sources possible (VK, Yandex, future)
- Embedders may have their own creds source (fixed config / their backend
  API / etc.) — they implement the interface
- Tests use `stub` provider

### Why is `CaptchaSolver` an interface?
- Multiple solving strategies possible (CDP, native, paid API, manual)
- Embedders may want to integrate with their existing UI (mobile webview,
  desktop GUI dialog) — they implement the interface
- `ChainSolver` lets you compose strategies at runtime

### Why `Hints []string` instead of `Hint string`?
- Multi-link fan-out — each cred-group gets its own hint, round-robin
- Backward-compatible: empty `Hints` falls back to single `Hint`
- See `internal/proxy.Hub.hintFor(streamID)` for the dispatcher

### Why DTLS over STUN ChannelData?
- TURN servers MUST support ChannelData (RFC 8656 §11) — universal.
- Wrapping WG packets in DTLS hides the WG handshake fingerprint that
  RKN DPI looks for.
- The TURN server sees DTLS-encrypted bytes inside ChannelData frames —
  looks indistinguishable from real WebRTC media (which is SRTP, also
  encrypted bytes).
- We don't need a real TLS handshake to look legit; pion/dtls's default
  is fine.

### Why session-id / stream-id framing?
- Multiple TURN allocations for one logical user → server needs to
  reassemble.
- Session-ID = the user's session UUID (16 bytes).
- Stream-ID = which physical TURN allocation this packet went through
  (1 byte, 0-255).
- Server demuxes by Session-ID, ignores Stream-ID for routing
  (it's used by the client to know which return path).

## Extension recipes

### Add a new credentials provider

1. Create `pkg/wgturn/provider/<name>/` with at least:
   - `provider.go` exporting `New(opts...) *Provider` returning
     something that implements `wgturn.CredentialsProvider`.
   - `parse.go` for hint format.
   - `*_test.go` against `httptest.NewServer`.
2. Add per-package `CLAUDE.md`.
3. Wire into `cmd/wgturn-cli` if you want CLI support — extend the
   `routedProvider` dispatcher.

### Add a new captcha solver

1. Subpackage under `provider/vk/captchasolve/<name>/`. Implement
   `vk.CaptchaSolver`.
2. Tests against a httptest fake.
3. Document trade-offs in package doc-comment.

### Add a new TURN framing mode

(Rarely needed. The wgturn-server compatibility constraint is real.)

1. Add a new `PeerType` constant in `pkg/wgturn/types.go`.
2. Implement framing in `internal/proxy/stream.go`'s switch.
3. Update server to match.
4. Cross-version test in `internal/proxy/integration_test.go`.

## What's intentionally NOT here

- No mDNS / service discovery — the WG client points at a fixed local
  port, no "find the service" complexity.
- No DNS-over-HTTPS / DNS resolution layer — that's the embedder's
  problem, not ours.
- No connection retry / backoff at the embedder level — `Tunnel.Start`
  blocks until first stream is up, then `runOnce` does its own retry
  loop on stream failure. Embedders see "up or down".
- No metrics / Prometheus — see ROADMAP P3. Solo use is fine without it.
- No GUI / TUI — that's the embedder's concern.
