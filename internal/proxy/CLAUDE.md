# internal/proxy — Hub + Stream

Internal because the wire format MUST be stable but the implementation
isn't part of the API contract. Don't import this from outside the
module.

## What's here

- `types.go` — `HubConfig` (proxy-internal counterpart of public
  `wgturn.Config`), `Credentials`, `Provider`, `Logger`,
  `ControlFunc`.
- `hub.go` — `Hub` struct (the central proxy), `NewHub`, `Start`,
  `Stop`, `Stats`, `LocalAddr`, `Ready`. Plus `hintFor(streamID)`
  for multi-link round-robin.
- `stream.go` — single TURN stream lifecycle: dial creds, allocate,
  optionally wrap in DTLS, pump packets in both directions.
- `framing.go` — proxy_v2 wire format (Session-ID + Stream-ID prefix).
- `dtls.go` — DTLS 1.2 wrapper using pion/dtls.
- `cert.go` — generates ephemeral self-signed cert for DTLS
  (the cert isn't validated by either side; DTLS provides obfuscation
  not auth).
- `integration_test.go` — flagship E2E test using a real in-process
  pion/turn server.
- `hub_internal_test.go` — `hintFor` round-robin coverage.

## Critical invariants

### proxy_v2 framing (what `wgturn-server` expects)

Every payload-bearing UDP packet has a 17-byte prefix:

```
[16 bytes: session UUID][1 byte: stream-id]
```

Server demuxes by session UUID, ignores stream-id for routing (the
client uses it to know which path the response came on).

Don't change this without coordinating a server-side update.

### Round-robin scheduler

`Hub.routeFromLocal` picks a stream by hashing the local client's
source addr modulo the stream count. This means:
- Same client → same stream (sticky).
- Different clients → different streams.
- One WG client = one stream effectively (which is fine for typical
  usage).

For multi-link bandwidth fan-out, use `Hints []string` to make the
streams hit DIFFERENT TURN allocations on different VK call sessions.

### Cred-fetch serialisation

`internal/creds.Cache.fetchMu` is held during the ENTIRE provider Fetch
call (including captcha solve, which can take ~1-2 sec). This prevents
thundering herd against VK's API. With 6 cred-groups that's ~6-12 sec
of serial fetches at startup — acceptable.

### Auth-error retry

`creds.HandleAuthError` counts errors per group; 3 errors in 10 sec
window invalidates the group's creds and forces a refetch. Without
this, a server-side cred rotation (every ~10 min from VK) would
cascade into a forever-failure if the in-flight allocation breaks
mid-stream.

## Don't regress

- Don't change the 17-byte session+stream prefix without a server
  upgrade.
- Don't widen `fetchMu` to per-group — the global serial fetch is
  what tames VK's per-IP rate-limit.
- Don't remove the round-robin scheduler — even though one WG client
  uses one stream, the scheduler is what makes multi-stream meaningful
  for high-fanout apps.

## Tests

`go test -race ./internal/proxy/...` — runs the in-process pion/turn
end-to-end. ~1 s. Covers raw mode, DTLS mode, multi-stream, session-id
demux, cred rotation.
