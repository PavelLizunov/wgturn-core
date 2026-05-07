# wgturn-core ROADMAP

What's done, what's next, what's parked. Dates are session-relative —
not calendar-precise. Strikethrough = no longer relevant.

## Done (and shipped to main)

### Q2 2026 — emergency channel ready

- ✅ TURN proxy hub (`pkg/wgturn`) + DTLS-over-STUN-ChannelData
- ✅ Embedded WireGuard userspace (`pkg/wgkernel`) — exists, used by tests,
  NOT yet wired into `cmd/wgturn-cli connect`
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
- ✅ Default `-streams 24` in CLI — empirical sweet spot
- ✅ wgturn-server on is-01 deployed, healthy
- ✅ Cross-compiled handoff bundle (linux/macOS/windows × amd64/arm64)
- ✅ CI green on Forgejo Actions (Go 1.25, race detector, golangci-lint)

## Next (priority-ordered, not yet started)

### N1 — `wgturn-cli connect` subcommand (≈2 h)

Single command: `sudo ./wgturn-cli connect /etc/wgturn/myvpn.conf` →
brings up everything in one process. Wires `pkg/wgkernel` (already
exists) into the CLI, so users no longer need separate `wg-quick up`.

Removes 3 of the 5 manual steps for end users. Increases binary size
by ~1 MB. Linux/macOS: requires `sudo` for TUN. Windows: requires
`wintun.dll` next to the binary.

### N2 — Auto-launch Chrome from CLI (≈30 min)

`-vk-chrome-auto` flag: search `$PATH` for `google-chrome` /
`chromium-browser` / `google-chrome-stable` etc.; spawn it with the
right flags into a tmp `--user-data-dir`; reap on signal.
Removes 1 more manual step. Doesn't help users without Chrome installed
at all — see N3 / N4.

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

### N4 — Embedded Chromium variant (≈3-4 h)

`pkg/wgturn/provider/vk/captchasolve/embedded/`:
- `go:embed` per-platform Chromium tarball (~80 MB each)
- Extract on first run to `~/.cache/wgturn/chromium/`
- Wraps existing `CDPSolver`

Adds ~80 MB per platform to release binary. Use case: standalone
"download once, no setup" consumer build. SDK embedders that don't
import this package are unaffected.

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
