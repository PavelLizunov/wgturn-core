# cmd/wgturn-cli — reference CLI binary

Thin wrapper around `pkg/wgturn`. Useful for testing and as a
zero-config tool for tech users. **Not the API contract** — embedders
shouldn't depend on the CLI's flag stability, only on `pkg/wgturn`.

## Modes

1. **Config-file**: `wgturn-cli -config /path/to/wireguard.conf`
   Parses `#@wgt:` metadata via `pkg/wgconf`. The canonical mode.
2. **Direct flags**: `-peer`, `-listen`, `-streams`, `-vk-link`,
   `-vk-chrome-url`, etc. Useful for testing without a config file.

## Defaults that matter

- `-streams 24` — empirical sweet spot for VK Calls TURN per source
  IP. See `docs/FINDINGS.md` "Bandwidth ceiling". Don't change without
  re-measuring.
- `-listen 127.0.0.1:9000` — local UDP listen address. Loopback by
  default; users who want LAN exposure pass `-listen 0.0.0.0:9000`.

## Captcha solver wiring

`pickCaptchaSolver(chromeURL, ua, log)` returns:
- `&captchasolve.CDPSolver{...}` if `-vk-chrome-url` is set
- `stdioCaptchaSolver{}` otherwise (only useful when VK is in legacy
  text-captcha mode, which it isn't in 2026 — slider mode is permanent)

Future (ROADMAP N3-N5): wire `ChainSolver` here so the CLI tries
native → CDP → 2captcha → stdin in order.

## Provider routing

`routedProvider` dispatches per Hint:
- Hint matches `telemost.yandex.*` or `telemost:*` → yandex provider
- Otherwise → vk provider

This lets a single Tunnel mix VK + Telemost links via
`-vk-link "url1,telemost:url2"`. Note that Telemost won't actually
yield a usable tunnel (walled-garden TURN), but the routing is still
correct.

## Future: `connect` subcommand

ROADMAP N1 — adds a `connect` subcommand that ALSO brings up
`pkg/wgkernel` for the WG endpoint, so end users don't need separate
`wg-quick`. Not done yet.

## Tests

`main_test.go` — `splitLinks` parser table. Other behaviour is covered
indirectly via `pkg/wgturn` unit tests + integration tests.

## Don't regress

- Don't change `-streams` default away from 24 without re-measuring
  bandwidth. We empirically picked it.
- Don't break `-config` / `-peer` / `-listen` etc. — handoff bundle
  README and example configs depend on them.
- Don't introduce default-on options that hit network (e.g. don't
  auto-enable telemetry).
