# pkg/wgturn/provider/yandex — Yandex Telemost provider

⚠️ **DO NOT USE AS A VPN BACKEND.** ⚠️

The credential-extraction logic is correct, but Yandex's TURN service
is a **walled garden** — it refuses to relay UDP to any peer IP outside
their own SFU AS. Verified end-to-end (tcpdump on receiver, 0 packets).

Kept in tree because:
1. Cred extraction is genuine engineering — useful for non-relay
   research.
2. The CLI's `routedProvider` dispatches `telemost.yandex.*` hints
   somewhere — better an explicit "peer-IP denied" failure than a
   misroute.
3. If Yandex relaxes their TURN filter (e.g., for a future diagnostics
   endpoint) the provider works without code change.

See `docs/FINDINGS.md` "Yandex Telemost is a walled garden" for the
empirical proof.

## What's here

- `api.go` — HTTP step 1: `POST cloud-api.yandex.ru/telemost_front/v2/
  telemost/conferences/<URL-ENCODED-LINK>/connection`. Returns
  `client_configuration.media_server_url` (a `wss://goloom.strm.yandex.net/join`
  URL) and a bootstrap `credentials` token.
- `ws.go` — WebSocket step 2 (the GOLOOM media-server protocol):
  - Hello payload extracted from telemost.yandex.ru's web SPA JS bundle
    (May 2026)
  - Ack-loop: server sends mid-stream frames; client must echo
    `{uid: theirs, ack: {status: {code: OK}}}` or server gives up
  - Walk `serverHello.rtcConfiguration.iceServers` for the first
    `turn:` URL with username + credential
- `parse.go` — link parser for `https://telemost.yandex.ru/j/<id>`
  forms + `IsTelemostLink(s)` for routing.
- `provider_test.go` — covers inline-creds + WS-creds happy paths,
  bare-id assembly, ConferenceNotFound mapping.

## GOLOOM protocol invariants (don't drift)

- Frames are `{uid: <uuid>, <kind>: {...}}` JSON.
- Every server frame with non-empty `uid` and no `ack` field requires
  an ack reply.
- ICE servers live at `serverHello.rtcConfiguration.iceServers`
  (camelCase). Older Telemost deployments used `rtc_configuration`
  (snake_case) — walker is tolerant of both.
- `capabilitiesOffer: {}` works in practice — server fills sensible
  defaults. Don't try to send a "complete" capabilities object; it'd
  require enumerating ~40 internal Goloom enums.

## Don't regress

- Don't remove the ack-loop. Without it, server gives up after ~5 sec.
- Don't change `media_server_url` parsing — it's nested under
  `client_configuration`, not at root (a 2026-Q1 GOLOOM rollout moved
  it).
- Don't expect a usable VPN tunnel from this. Cred-extraction is the
  ceiling of what this provider does.
