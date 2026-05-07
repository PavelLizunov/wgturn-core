# CLAUDE.md — wgturn-core entry point

This file is the first thing a Claude session should read when working on
`wgturn-core`. It pins the *current state of the world* so subsequent
sessions don't have to re-learn it.

## Read in this order

1. **This file** — high-level positioning, current status, gotchas.
2. **`docs/HANDBOOK.md`** — concrete commands: how to build, test, deploy,
   debug. Where the infrastructure lives.
3. **`docs/ROADMAP.md`** — what's done, what's next.
4. **`docs/FINDINGS.md`** — things we learned the hard way, do NOT re-test.
5. **`docs/ARCHITECTURE.md`** — design rationale, why the code is shaped
   the way it is.
6. Module-level `CLAUDE.md` files (e.g. `pkg/wgturn/provider/vk/CLAUDE.md`)
   — package-specific gotchas; read when touching that package.

## What this project is

Embeddable Go library that tunnels WireGuard through VK Calls' anonymous
TURN infrastructure. Positioned as an **emergency channel for РКН
white-list mode** — when `xray`/`OpenVPN`/`WireGuard` are all blocked,
this still works because VK is government-mandated and stays whitelisted.

**Hard ceiling: ~200 KB/s (~1.6 Mbps) per source IP.** This is an
empirical bandwidth-shaping constant on VK's side, not a software
limitation. Multiple call links / streams hit the same per-IP cap.

## What this project is NOT

- **Not a daily-driver VPN.** ~1.6 Mbps is voice-grade, not video-grade.
  For high-bandwidth use, point users at xray/REALITY through a RU VPS
  they own — that's a different stack.
- **Not a multi-source bandwidth aggregator.** A single device cannot
  exceed the per-IP cap regardless of architecture (verified). Multi-IP
  scaling requires multiple physical devices or RU VPSes the user
  separately maintains.

## Current status (2026-05-07)

| Component | State |
|---|---|
| `pkg/wgturn` (TURN proxy core) | ✅ stable |
| `pkg/wgturn/provider/vk` (VK creds) | ✅ stable, utls + Chrome headers + correct success_token submit |
| `pkg/wgturn/provider/vk/captchasolve` (CDPSolver) | ✅ works against real VK, ~1 sec per solve |
| `pkg/wgturn/provider/yandex` (Telemost) | ⚠️ creds extract correctly, but TURN is walled-garden — UNUSABLE as VPN backend |
| `pkg/wgconf` (config parser) | ✅ parses `#@wgt:` metadata + standard wg-quick `[Interface]` / `[Peer]` sections |
| `pkg/wgkernel` (embedded WG userspace) | ✅ stable; wired into the CLI's `connect` subcommand |
| `cmd/wgturn-cli` legacy mode | ✅ working, default `-streams 24` (kept for handoff backward compat) |
| `cmd/wgturn-cli connect` subcommand | ✅ Linux auto host-setup; macOS/Windows print manual `ip`/`ifconfig` hints |
| Server (`wgturn-server` on is-01) | ✅ Up, healthcheck disabled |
| CI (Forgejo Actions) | ✅ green; transient `data.forgejo.org` checkout timeouts ~10% — retrigger via empty commit |

## Hard rules

- **Don't reintroduce `captcha_token` field name.** VK rejects it. Use
  `success_token` per cacggghp/vk-turn-proxy reference. See
  `pkg/wgturn/provider/vk/captcha.go applySolution`.
- **Don't run more than ~5 captcha-solving fetches against VK from
  the same source IP within 10 minutes** — they rate-limit hard. Wait
  ~30 min for cooldown.
- **Don't use `localhost` in `-vk-chrome-url`** — Go's HTTP client
  prefers IPv6 and Chrome listens on IPv4-only. Use `127.0.0.1`.
- **Don't break the `vk.CaptchaSolver` interface contract.** Embedders
  rely on it. If extending, add new fields with sensible defaults; never
  remove or rename existing ones.
- **Don't ship handoff bundle with WG private keys + IPs into the public
  repo** — it lives in `~/wgturn-handoff/` on the homelab .207, NOT in
  git. Keep it that way.

## Commit style

- Subject ≤ 72 chars: `<type>(<scope>): <imperative>`
- Body: explain *why*, not *what*. Reference observed behaviour, links
  to upstream issues if any.
- Always end with `Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>`.
- Never commit on someone's behalf without `--allow-empty` or explicit
  consent if the commit is mechanical (e.g., CI retrigger, gofmt).

## Push targets

- `origin` = `ssh://git@192.168.0.207:18222/slovn/wgturn-core` (Forgejo,
  internal). This is the **only** authoritative remote.
- Mirror to GitHub / Codeberg has been DISCUSSED but NOT done. See
  `docs/ROADMAP.md` for the security/legal trade-off.

## When in doubt

Read `docs/FINDINGS.md` first — it has the dead ends we already
explored. Don't re-research things that are documented as failed.
