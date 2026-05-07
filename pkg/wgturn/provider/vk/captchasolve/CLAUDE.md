# pkg/wgturn/provider/vk/captchasolve — captcha solver implementations

Optional subpackage with concrete `vk.CaptchaSolver` implementations.
Embedders import only what they need; unused implementations don't
bloat the binary.

## What's here

- `cdp.go` — `CDPSolver` drives an external headless Chrome via DevTools
  Protocol. Currently the **only working production solver**.
- `chain.go` — `ChainSolver` tries an ordered list of inner solvers,
  returns first success. Use to compose fallbacks.
- `cdp_test.go` — fake CDP server (httptest + ws-accept) for testing
  the solver against a controlled Chrome.
- `chain_test.go` — table-driven coverage of fan-out behaviour, hooks,
  context cancellation.

## Planned

- `native/` — pure-Go slider solver, port of `cacggghp/vk-turn-proxy/
  client/slider_captcha.go`. Needed for SDK use cases where embedders
  don't want a Chrome dependency. See `docs/ROADMAP.md` N3.
- `embedded/` — bundled Chromium via `go:embed`, ~80 MB per platform.
  For standalone "download and run" desktop builds. See ROADMAP N4.
- `twocaptcha/` — paid 2captcha.com API client. Cheapest fallback when
  Chrome is absent and native solver is broken. ROADMAP N5.

## Why CDP and not native today

VK's not-a-robot widget runs JS proof-of-work, ships browser
fingerprint via `componentDone`, and AES-encrypts the answer with keys
baked into a 800 KB JS bundle. Re-implementing all of that out-of-browser
is a multi-week job that breaks every time VK rotates keys.

Letting real Chrome run the page is leaner. Costs an external dep,
saves ~6-8 hours of engineering and keeps reliability at ~99%.

## CDPSolver requires

- A running Chrome / Chromium with `--remote-debugging-port=N`.
- Chrome ≥ 122 (M111+ requires PUT for `/json/new`).
- `--remote-allow-origins=*` flag if your CDP client is from a different
  origin (we hit it via `http://127.0.0.1:9222` → "any origin" works
  with this flag).

## Don't regress

- Keep the `findCheckboxJS` JS expression in sync with telemost.yandex.ru
  / id.vk.ru DOM. The selector is intentionally tolerant (multiple
  fallback selectors); don't remove the fallbacks.
- Don't remove the `mouseMoved → mousePressed → mouseReleased` sequence
  with timing — VK's anti-bot looks for sub-millisecond press/release
  deltas. We pause ~80 ms between press and release, ~250 ms pre-hover.
  See `simulateHumanClick`.
- Don't change `awaitSuccessToken` deadline below ~30 s. VK has been
  seen to take 10-15 s to respond when its captcha-check service is
  loaded.
