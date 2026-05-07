# pkg/wgturn — public core API

The stable surface every embedder imports. Do not break backward
compatibility without bumping a version.

## What's here

- `types.go` — `PeerType`, `Mode`, public constants.
- `config.go` — `Config` struct (single source of truth for tunnel
  parameters). Note: `Hint string` + `Hints []string` coexist; `Hints`
  takes precedence when set. Empty `Hints` + non-empty `Hint` reproduces
  the legacy single-hint behaviour.
- `tunnel.go` — `Tunnel` runtime handle, `New / Start / Stop / Stats`.
- `provider.go` — `CredentialsProvider` interface that providers
  implement.
- `protector.go` — `SocketProtector` interface for VPN-fd protection
  on Android. `NoopProtector` for desktop/server.
- `logger.go` — Logger interface + `StdLogger`, `NoopLogger`.
- `errors.go` — public sentinels (`ErrCaptchaRequired`, `ErrAuthFailure`,
  `ErrInvalidLink`).

## Don't break

- Field renames in `Config`. Embedders rely on struct literal
  initialisation.
- Method names on `Tunnel`. `Stats()` returning a value not a pointer
  is intentional (zero-cost copy).
- `CredentialsProvider.Fetch(ctx, hint, streamID)` signature. Multiple
  providers implement it.

## Adding to Config

When adding a field:
- Don't make it required (zero-value must be valid).
- Document in the field's comment what the zero value means.
- Keep `Hint`/`Hints` precedence rule: `Hints` wins when non-empty.

## Tests

`go test ./pkg/wgturn/...` — uses stub provider, no network. Fast.

The real integration test is in `internal/proxy/integration_test.go`
(in-process pion/turn server). Run with `-race`.
