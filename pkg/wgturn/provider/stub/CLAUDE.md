# pkg/wgturn/provider/stub — fixed-creds provider

Trivial provider that returns the same hardcoded `(username, password,
server_addr)` on every `Fetch` call. Used by tests and smoke runs
where you don't want to hit a real upstream API.

## When to use

- Integration tests that bring up a real in-process pion/turn server
  with known creds.
- Smoke runs of `cmd/wgturn-cli` against a third-party TURN you control
  (no captcha needed).
- Reference for "what does a minimal provider look like" — the entire
  package is ~30 lines.

## Don't add features

This package is intentionally minimal. If you want auth retry, TTL,
captcha, etc., write a new provider — don't extend stub. The point of
stub is being trivially predictable.
