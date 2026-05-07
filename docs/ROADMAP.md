# wgturn-core ROADMAP

What's done, what's next, what's parked. Dates are session-relative —
not calendar-precise. Strikethrough = no longer relevant.

## Done (and shipped to main)

### Q2 2026 — emergency channel ready

- ✅ TURN proxy hub (`pkg/wgturn`) + DTLS-over-STUN-ChannelData
- ✅ Embedded WireGuard userspace (`pkg/wgkernel`) — wired into
  `cmd/wgturn-cli connect`
- ✅ `wgturn-cli connect <wg-quick.conf>` subcommand: one-command VPN
  startup. Auto-spawns headless Chrome (overridable via
  `--vk-chrome-url`), auto-configures Linux host-side networking
  (link/addr/routes), graceful LIFO teardown on SIGINT/SIGTERM. macOS
  and Windows print the manual `ip`/`ifconfig`/`route` commands the
  user must run themselves until the auto-host-setup gets ported. (N1+N2)
- ✅ `pkg/wgconf` parses standard wg-quick `[Interface]` / `[Peer]`
  sections in addition to `#@wgt:` metadata, so a single .conf drives
  both hub and kernel without changes from `wg-quick up` users.
- ✅ VK Calls anonymous-token API client (`pkg/wgturn/provider/vk`):
  - utls Chrome 133 JA3 fingerprint
  - Full Chrome cross-origin headers (sec-ch-ua-*, Sec-Fetch-*, Origin, Referer)
  - gzip/deflate/brotli/zstd response decompression
  - 5-step flow at `v=5.275`
  - Correct retry envelope: `success_token` + `captcha_ts` +
    `captcha_attempt` + empty `captcha_key` + `is_sound_captcha=0`
- ✅ CDP captcha solver (`pkg/wgturn/provider/vk/captchasolve.CDPSolver`):
  - Drives external headless Chrome via DevTools Protocol
  - Clicks "I'm not a robot" checkbox
  - Captures `success_token` from `captchaNotRobot.check`
  - ~1 sec per solve when VK is in checkbox-only mode
- ✅ Multi-link fan-out via `wgturn.Config.Hints []string` —
  cred-groups round-robin through the pool
- ✅ ChainSolver primitive (`captchasolve.ChainSolver`) for multi-strategy
  fallback
- ✅ Yandex Telemost provider (`pkg/wgturn/provider/yandex`):
  - HTTP step 1 + WebSocket GOLOOM step 2 fully implemented
  - Cred extraction works
  - **TURN walled-garden — UNUSABLE as VPN backend** (peer-IP filter,
    confirmed via tcpdump)
  - Code kept in tree as research artifact + in case Yandex relaxes filter
- ✅ Embedded Chromium variant (`-tags embedded`): bundles
  chrome-headless-shell 148.0.7778.97 inside the binary via go:embed,
  extracts on first use into the user cache, wires into
  findChromeOnPath as a last-resort fallback. Default builds are
  unaffected (~9 MB); `-tags embedded` builds add ~95-115 MB. linux/arm64
  is unsupported (Chrome for Testing doesn't publish that combo). (N4)
- ✅ Default `-streams 24` in CLI — empirical sweet spot
- ✅ wgturn-server on is-01 deployed, healthy
- ✅ Server-side user provisioning: `scripts/provision-user.sh`,
  `list-users.sh`, `revoke-user.sh` — POSIX sh, ssh+wg-syncconf, no
  downtime. e2e roundtrip verified on is-01. (N7)
- ✅ Cross-compiled handoff bundle (linux/macOS/windows × amd64/arm64)
- ✅ CI green on Forgejo Actions (Go 1.25, race detector, golangci-lint)
- ✅ Server-side re-implementation as `pkg/wgturnsrv` under Apache-2.0,
  clean-room (no GPL upstream sources). Single binary `wgturn-cli`
  serves both `connect` (client) and `serve` (server) roles
  sing-box-style. Shared wire-format primitives in `internal/framing`
  (17-byte handshake + DTLS config builder) keep client and server in
  byte-for-byte agreement. Keystone proof: in-process `pair_test.go`
  drives a real WG handshake through wgkernel#1 → Hub → in-process
  pion/turn → Server → wgkernel#2 in ~150 ms, race-clean on CI. (N8
  coding; is-01 switch is a separate operational window — see
  `docs/HANDBOOK.md` "Switching is-01".)
- ✅ Single-string distribution via `wgturn://` URLs:
  - `pkg/wgshare` is the public URL codec. `Profile.Encode` produces
    `wgturn://<base64url-payload>#label`; `Parse` decodes back. The
    payload is versioned JSON (v=1) carrying server pubkey, freshly
    generated client privkey, optional PSK, DTLS endpoint, assigned
    /CIDR, AllowedIPs, DNS, MTU, keepalive. The VK Calls invite is
    intentionally NOT in the URL (rotates per session, supplied at
    connect-time).
  - `pkg/wgadmin.Server.Provision` is the Go port of
    `provision-user.sh`: pure-Go curve25519 keygen + PSK, atomic
    `wg0.conf` rewrite with `# wgturn-name = …` tagged [Peer] blocks,
    `wg syncconf` so existing sessions don't drop. Symmetric Revoke /
    List. Single source of truth stays in wg0.conf — embedders
    running their own provisioning UI import the same primitives.
  - CLI: `wgturn-cli provision-url <name>...` (batch URL emit),
    `wgturn-cli connect-url <wgturn://...> --vk-link <url>` (client
    by URL), `wgturn-cli revoke-url <name>...` (cleanup).
  - `examples/vpn-client/` is a ~120-line embedder template: take a
    URL + VK link, drive Tunnel → Kernel → TUN, exit on SIGINT.
    Documents the deliberate omissions (host networking, auto-Chrome,
    reconnect loop) embedders are expected to fill.

## Next (priority-ordered, not yet started)

### N1.5 — macOS / Windows host-side network setup (≈2-3 h)

`wgturn-cli connect` v0 only auto-configures the host (link, addrs,
routes) on Linux via `/sbin/ip`. macOS needs `ifconfig` + `route`,
Windows needs `netsh interface` and the wintun driver semantics.
Until then, non-Linux users see a printout of the manual commands
they need to run after `connect` brings the tunnel up.

Layered approach: small per-OS shell-out files (`hostsetup_darwin.go`,
`hostsetup_windows.go`) implementing the same `(func(), error)` shape
the existing Linux path uses. Idempotency policy stays the same: refuse
to reuse half-state from a crashed run; user cleans manually with
the matching del commands.

### N3 — Pure-Go slider captcha solver (≈6-8 h)

Port `cacggghp/vk-turn-proxy/client/slider_captcha.go` into
`pkg/wgturn/provider/vk/captchasolve/native/`:
- `captchaNotRobot.settings` + `getContent` API client
- Edge-detection / template-matching for puzzle gap (use
  `image/draw` and pure-Go CV; no OpenCV)
- AES-encrypted `answer` payload (reverse from VK JS bundle)
- Human-like mouse trajectory generator
- Unit tests against fixture PNGs

Eliminates Chrome dependency for SDK embedders. ~70-90% reliability.
Maintenance burden: VK rotates encryption keys ~quarterly.

### N5 — 2captcha API solver (≈1 h)

`captchasolve/twocaptcha.NewSolver(apiKey)` implements `vk.CaptchaSolver`
by uploading the captcha image / redirect URL to 2captcha.com and
polling for the answer. Fallback for users who can't run Chrome and
don't want to import `embedded`. Cost ~$0.0015 per solve.

### N6 — gomobile bindings (≈4-8 h)

`pkg/wgturn/mobile/` Go-mobile-friendly facade that exposes the API
surface as flat functions (no contexts, no interfaces, mobile binders
hate Go interfaces). Outputs:
- `wgturn-android.aar` for Android Studio import
- `wgturn-ios.xcframework` for Xcode import

Minimal Android demo app in `examples/android-demo/` showing how to
wire the lib into a `VpnService`.

## Future / parked

### P1 — Multi-source bandwidth aggregator
Architecturally possible but only useful for users who have multiple RU
VPSes themselves (rare). For most users, the per-IP cap is the
practical reality. **Not pursuing.** See `docs/FINDINGS.md` "Bandwidth
ceiling" for empirical proof.

### P2 — Public mirror (GitHub / Codeberg)
Trade-offs discussed but not done. See CLAUDE.md "Push targets". Risk:
Streisand effect (VK might patch faster), DMCA exposure, Russian law
276-FZ ambiguity. If needed, prefer **Codeberg under anonymous handle**
+ strip handoff bundle from public artifacts.

### P3 — Server-side metrics / observability
wgturn-server on is-01 has no Prometheus endpoint. For long-term
reliability, expose `/metrics` with stream/throughput/error counters.
Low priority — current monitoring (docker logs) is enough for solo use.

### P4 — Sing-box module
`wgturn-core` could be wrapped as a sing-box outbound. Discussed in
original session goal. Not started; sing-box's plugin API has changed
since 1.x and we'd need to track it. Lower priority than N1-N5.

### P5 — Mobile demo apps polished
After N6, build a minimum-viable Android app (one button: connect /
disconnect, status indicator) for Pavel's `vpn-crypto` integration.
Requires N6 first.

## Anti-roadmap (do NOT do)

- ❌ Add Yandex Telemost as a VPN backend — walled-garden TURN, won't
  work without Yandex relaxing peer-IP filter. Code stays as research
  artifact only.
- ❌ Build a multi-source UDP-aggregator that fans out from one device
  to multiple wgturn-cli instances. Doesn't help bandwidth (per-IP cap).
- ❌ Try to bypass VK captcha without solving it. VK is in slider mode
  permanently; the legacy `captcha_key` text field path no longer works.
- ❌ Rewrite the wgturn protocol. It's fixed at proxy_v2 (matches
  `kiper292/vk-turn-proxy` server). Any change breaks server compat.
