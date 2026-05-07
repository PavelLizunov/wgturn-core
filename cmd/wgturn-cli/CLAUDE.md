# cmd/wgturn-cli — reference CLI binary

Thin wrapper around `pkg/wgturn` and `pkg/wgkernel`. **Not the API
contract** — embedders shouldn't depend on the CLI's flag stability,
only on the public packages.

## Modes

### `connect` subcommand — one-command VPN (the recommended path)

```sh
sudo wgturn-cli connect /path/to/wireguard.conf
```

Single command stands up:
- the wgturn proxy hub (`pkg/wgturn`),
- the embedded WireGuard kernel (`pkg/wgkernel`) with `WithTurnTunnel`
  rewriting peer Endpoint to the hub's local listener,
- headless Chrome for the CDP captcha solver (auto-spawned unless
  `--vk-chrome-url` overrides),
- Linux host-side networking (`ip link set up; ip addr add; ip route
  add`); macOS / Windows print the manual commands instead of failing.

Lifecycle is strictly LIFO on shutdown: routes deleted, addrs removed,
link down, kernel stopped, hub stopped, Chrome killed, scratch
`--user-data-dir` wiped.

Flags:

| Flag | Default | Notes |
|---|---|---|
| `-config <path>` / positional | required | wg-quick `.conf` with `#@wgt:` metadata |
| `-iface <name>` | `wgturn0` | TUN interface name (Linux/macOS) |
| `--vk-chrome-url <url>` | empty | Use existing Chrome at this CDP URL; skips auto-launch |
| `--vk-chrome-auto` | `true` | Spawn headless Chrome ourselves when no URL is given |
| `--vk-chrome-ua <string>` | empty | Override `navigator.userAgent` in the captcha tab |
| `-stats <duration>` | `5s` | Stats print interval (`0` disables) |
| `-v` | `false` | Verbose logging |

### Legacy hub-only mode (no subcommand)

```sh
wgturn-cli -config /path/to/wireguard.conf
wgturn-cli -peer 1.2.3.4:56000 -vk-link https://vk.com/call/join/...
```

Runs only the wgturn proxy. The user is responsible for bringing up
WireGuard separately (e.g. `wg-quick up`). Kept verbatim for
backward compatibility with the handoff bundle's existing instructions.

## Files

- `main.go` — subcommand dispatch + legacy `runProxy()` (formerly
  the entire `main`).
- `connect.go` — `runConnect()`, `parseWGConfig()`, `resolveChromeURL()`,
  `buildKernelConfig()`.
- `chrome.go` — `findChromeOnPath()`, `launchChrome()`, `chromeProcess`,
  `waitChromeReady()`. Self-contained; could be promoted to a public
  subpackage if other embedders ever need it.
- `hostsetup.go` — `setupHostIface()` + Linux implementation +
  `isCoveredByConnectedRoute()` route-collision check.
- `*_test.go` — unit-tests including the PATH-stub fake `ip` for the
  Linux host-setup happy path and rollback-on-error path.

## Defaults that matter

- `-streams 24` (legacy mode) — empirical sweet spot for VK Calls TURN
  per source IP. See `docs/FINDINGS.md` "Bandwidth ceiling". Don't
  change without re-measuring.
- `--vk-chrome-auto=true` (connect mode) — turning this off without
  also passing `--vk-chrome-url` falls back to the stdio captcha
  solver, which only works for VK's legacy text-captcha mode (no
  longer in rotation; slider mode will fail).
- Connect-mode start timeout = 90 s. Generous because cold-start
  captcha solving can take a few seconds × N cred-groups.

## Captcha solver wiring

`pickCaptchaSolver(chromeURL, ua, log)` returns:
- `&captchasolve.CDPSolver{...}` if a Chrome URL is set (either via
  `--vk-chrome-url` or auto-launched in connect mode)
- `stdioCaptchaSolver{}` otherwise

Future (ROADMAP N3-N5): wire `ChainSolver` here so the CLI tries
native → CDP → 2captcha → stdio in order.

## Provider routing

`routedProvider` dispatches per Hint:
- Hint matches `telemost.yandex.*` or `telemost:*` → yandex provider
- Otherwise → vk provider

This lets a single Tunnel mix VK + Telemost links via
`-vk-link "url1,telemost:url2"`. Note that Telemost won't actually
yield a usable tunnel (walled-garden TURN), but the routing is still
correct.

## Host-setup: Linux only in v0

`setupHostIfaceLinux` shells out to `/sbin/ip` rather than netlink to
keep the dep tree CGO-free. macOS / Windows return
`errHostSetupUnsupported` with a message listing the manual commands —
the user runs them after `connect` brings the tunnel up. ROADMAP
N1.5 covers porting this to those platforms.

Idempotency policy: half-state from a previous crashed run gets a
"File exists" error rather than silent reuse. User cleans up via
`ip link del wgturn0` and retries. This is rough but honest for v0.

## Don't regress

- Don't change `-streams` default away from 24 without re-measuring
  bandwidth. We empirically picked it.
- Don't break `-config` / `-peer` / `-listen` etc. — handoff bundle
  README and example configs depend on them.
- Don't introduce default-on options that hit network (e.g. don't
  auto-enable telemetry).
- Don't move chrome.go / hostsetup.go to public packages without a
  concrete second consumer. They're CLI-private until proven otherwise.
- Don't make `connect` swallow errors silently. The user reads stderr
  to debug; clean error wrapping with the `connect:` prefix is the
  contract.
