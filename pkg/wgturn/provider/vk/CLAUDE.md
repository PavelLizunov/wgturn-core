# pkg/wgturn/provider/vk — VK Calls credentials provider

Fetches anonymous TURN creds from the VK Calls API. Production-ready;
works against real `vk.ru` / `api.vk.ru` / `calls.okcdn.ru`.

## 5-step flow (matches kiper292 reference at v=5.275)

1. `POST login.vk.ru/?act=get_anonym_token` → primary anonymous token
2. `POST api.vk.ru/method/calls.getCallPreview` (best-effort, fingerprint
   step VK uses to gate step 3)
3. `POST api.vk.ru/method/calls.getAnonymousToken` → call-scoped token
   **(captcha-gated)**
4. `POST calls.okcdn.ru/fb.do auth.anonymLogin` → OK CDN session_key
5. `POST calls.okcdn.ru/fb.do vchat.joinConversationByLink` → TURN creds

## Critical invariants

### utls JA3 fingerprint is mandatory (`utls_transport.go`)

The default Go `crypto/tls` fingerprint trips an immediate captcha on
step 3. We use `refraction-networking/utls` with `HelloChrome_Auto` and
manually pin ALPN to `["http/1.1"]` (Chrome's default advertises h2,
but our `req.Write/ReadResponse` only support HTTP/1.1).

### Chrome cross-origin headers are mandatory (`api.go post()`)

`Origin: https://vk.com`, `Referer: https://vk.com/`,
`Sec-Fetch-Site: cross-site`, plus the full `sec-ch-ua-*` triplet from
the active browser profile. Missing any of these triggers captcha
regardless of TLS handshake.

### Response decompression must be in our custom transport

stdlib's auto-decompression doesn't apply to the utls path. See
`utlsResponseBody` wrapper in `utls_transport.go` — handles
gzip/deflate/brotli/zstd and strips Content-Encoding header.

### Captcha submit envelope (`captcha.go applySolution`)

For not-a-robot redirect flow (the slider mode VK uses in 2026):

```
captcha_sid       = SID from error envelope
captcha_ts        = TS from error envelope (echo verbatim)
captcha_attempt   = Attempt from error envelope (echo verbatim)
is_sound_captcha  = "0"
captcha_key       = ""  (must be present empty — VK uses presence to disambiguate)
success_token     = the JWT-shaped string from captchaNotRobot.check
```

**Do NOT use `captcha_token`** — that's the legacy text-mode field.
VK silently re-challenges if you use it. See `docs/FINDINGS.md`
"Captcha submit field-name footgun".

## Constants you can override (`api.go`)

- `defaultAppID` / `defaultClientSecret` — VK Web App credentials. The
  defaults are PUBLIC (used by VK web client). Override via
  `WithAppCredentials` if VK rotates them.
- `apiVersion = "5.275"` — VK API version. Older versions return
  "Rate limit reached" on getAnonymousAccessTokenPayload.
- `capabilities = "2F7F"` — bitmask for vchat.joinConversationByLink.
  Magic value from kiper292 reference.

## Tests

`provider_test.go` uses `httptest.NewServer` mocking each step. Covers:
- Happy path with valid creds
- Bare ID hint
- Invalid link
- Captcha required without solver (returns `ErrCaptchaRequired`)
- Captcha required with solver (verifies retry envelope, success_token
  flow)
- Auth failure mapping (HTTP 401 + VK code 5)

Run: `go test ./pkg/wgturn/provider/vk/`.

## Don't regress

- Don't remove `is_sound_captcha=0` or empty `captcha_key=""` from
  retry envelope — they're not optional.
- Don't change the order of header set in `post()` — VK has been seen
  to be picky about header ordering in some bot heuristics (anecdotal).
- Don't disable utls — Go's default TLS gets caught immediately.
