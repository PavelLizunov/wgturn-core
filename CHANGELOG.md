# Changelog

All notable changes to wgturn-core are recorded here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versions are SemVer-ish; pre-1.0 we treat minor bumps as feature
gates and patch bumps as bug fixes, but reserve the right to ship
breaking changes on a minor bump until 1.0 lands.

## [v0.1.0] — 2026-05-07

First tagged release. Everything below has been in `main` for some
time; the tag captures a coherent snapshot.

### Added

- **Core proxy** (`pkg/wgturn`): TURN-relayed UDP tunnel with DTLS
  1.2 obfuscation and STUN ChannelData framing. Multi-stream
  (default 24) with multi-link fan-out (`Hints []string`) and a
  round-robin scheduler. Stable since Q2 2026, repeatedly verified
  end-to-end at ~200 KB/s through is-01.
- **VK provider** (`pkg/wgturn/provider/vk`): anonymous-token client
  for VK Calls with full Chrome JA3 fingerprint parity, the correct
  `success_token` retry envelope (`captcha_ts` + `captcha_attempt` +
  empty `captcha_key` + `is_sound_captcha=0` — getting any of those
  wrong silently re-challenges).
- **CDP captcha solver** (`pkg/wgturn/provider/vk/captchasolve`):
  drives external Chrome through DevTools Protocol, clicks the
  "I'm not a robot" checkbox, harvests the success token.
  ~1 s per solve when VK is in checkbox mode.
- **Embedded Chromium variant** (`-tags embedded`): bundles
  chrome-headless-shell 148.0.7778.97 via go:embed, extracts on
  first use. +~100 MB per binary; linux/arm64 unsupported.
- **Yandex Telemost provider** (`pkg/wgturn/provider/yandex`):
  credential extraction works, but the TURN is a walled garden —
  retained as a research artefact, not a working VPN backend.
- **Embedded WireGuard kernel** (`pkg/wgkernel`): wireguard-go
  wrapper that pairs with the proxy via `WithTurnTunnel`. Same
  process, no `wg-quick` dependency.
- **wg-quick config parser** (`pkg/wgconf`): parses `#@wgt:`
  metadata + standard `[Interface]` / `[Peer]` sections; same
  file drives both proxy and kernel.
- **Apache-2.0 server-side proxy** (`pkg/wgturnsrv`): clean-room
  re-implementation of the legacy GPL `kiper292/vk-turn-proxy`
  server, exposed via `wgturn-cli serve`. Per-session demuxer with
  eviction-on-duplicate-streamID, round-robin to streams,
  idempotent termination. Replaces the GPL fork on the server
  side; the wire protocol stays compatible with existing clients.
- **Shared wire-format primitives** (`internal/framing`): 17-byte
  handshake encoder/decoder + DTLS config builder. Both client
  (`internal/proxy`) and server (`pkg/wgturnsrv`) import this so
  the wire format has exactly one source of truth.
- **Single-string distribution** (`pkg/wgshare`): `wgturn://`
  share URLs bundling every key, IP, and option a client needs.
  The VK Calls invite is intentionally NOT in the URL — it's a
  runtime parameter. Format is versioned JSON (v=1), opaque to
  humans, ~350 chars typical.
- **Server-side provisioning API** (`pkg/wgadmin`): pure-Go
  curve25519 keygen + IP allocator + atomic `wg0.conf` editor +
  `wg syncconf` runner. Provision/Revoke/List API for embedders;
  CLI shells out to it through `provision-url` / `revoke-url`.
- **CLI subcommands**: `connect` (legacy `.conf` flow), `serve`
  (server-side proxy), `connect-url` (URL-driven client),
  `provision-url` (batch URL generator), `revoke-url`. Plus the
  legacy flag-driven mode preserved for backward compatibility.
- **Embedder example**: `examples/vpn-client/` — ~120 lines,
  shows how to take a `wgturn://` URL + a VK link and bring up
  the full stack from inside a custom Go app.
- **Operator scripts** (`scripts/`): `provision-user.sh` /
  `list-users.sh` / `revoke-user.sh` for the legacy shell-driven
  flow; coexist with `wgturn-cli provision-url` on the same
  `wg0.conf` (both use the same `# wgturn-name = …` tag).
- **Documentation**: top-level `README.md` with operator
  disclaimer, `SECURITY.md` reporting policy, `docs/HANDBOOK.md`
  operator runbook, `docs/ARCHITECTURE.md` design rationale,
  `docs/FINDINGS.md` empirical results, `docs/ROADMAP.md`
  status, `CLAUDE.md` per-package gotchas.

### Verified

- End-to-end exit IP through is-01 confirmed against the real
  VK TURN infrastructure.
- In-process `pair_test.go` drives a real WireGuard handshake
  through `wgkernel#1 → Hub → in-process pion/turn → Server →
  wgkernel#2` in ~150 ms, race-clean.
- CI green on Forgejo Actions: `go build`, `go vet`,
  `go test -race`, `golangci-lint run`. Go 1.25, `CGO_ENABLED=0`.
- Cross-compile matrix: linux/amd64, linux/arm64, darwin/amd64,
  darwin/arm64, windows/amd64. Embedded-Chromium variants for all
  except linux/arm64.

### Known limits

- ~200 KB/s per source IP, hard. Adding streams / call links /
  servers does not raise it.
- `Backend = wgkernel` in `wgturn-cli serve` is single-session only;
  multi-peer all-in-one fan-out is future work. Production
  deployments use `Backend = udp:127.0.0.1:51820` with a separate
  `wg-quick`-managed daemon.
- macOS / Windows host-side networking under `wgturn-cli connect`
  prints manual `ip`/`ifconfig`/`route` commands; auto-config is
  Linux-only in v0.1.

[v0.1.0]: https://github.com/PavelLizunov/wgturn-core/releases/tag/v0.1.0
