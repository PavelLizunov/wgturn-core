# internal/creds — per-stream-group credentials cache

Memoizes the (username, password, server) tuple a `CredentialsProvider`
returns, with a TTL and auth-error invalidation. Internal-only.

## What's here

- `cache.go` — `Cache`, `Provider`, `Credentials`, `entry`. The whole
  module.
- `cache_test.go` — covers cache hit/miss, expiry, auth-error
  invalidation, concurrent access.

## Group semantics

Streams are grouped by `streamID / streamsPerCred` (default
`streamsPerCred=4`). All streams in a group share one cred entry.

With multi-link (`Hints []string`), each cred-group dial gets the next
hint in round-robin. So group 0 hits link 0, group 1 hits link 1, etc.
See `internal/proxy/hub.go hintFor`.

## Critical invariant: `fetchMu`

The mutex is held during the ENTIRE `provider.Fetch` call, including
captcha solve. This serialises N parallel cred-fetches at startup
(N = number of cred-groups). Otherwise:

1. N parallel HTTP calls to login.vk.ru → VK rate-limits → fails for some
2. N parallel CDP solver instances → opens N Chrome tabs at once → race
   conditions (we tested this — DNS lookups got cancelled)

With fetchMu, fetches go one-at-a-time. With 6 groups × ~2 sec/captcha =
~12 sec startup. Acceptable.

## Auth-error invalidation

`HandleAuthError(streamID)` is called by the proxy when a TURN
allocation returns 401/403 (creds rejected, e.g. expired mid-stream).
Counts errors per group. After 3 errors within `ErrorWindow` (10 sec)
the entry is invalidated and the next `Get` triggers a refetch.

## TTL handling

`Credentials.ExpiresIn` from the provider sets the cache TTL. If the
provider returns 0, `DefaultLifetime` (10 min) is used with
`DefaultSafetyMargin` (60 sec) shaved off — so we refetch slightly
before VK's expiry to avoid a window where in-flight requests use
about-to-expire creds.

## Don't regress

- Don't widen `fetchMu` to per-group. Global serial fetch is what
  protects VK from rate-limiting us.
- Don't reduce `MaxCacheErrors` (3) — transient TURN errors happen,
  3 is the working threshold to distinguish from sustained auth failure.
- Don't cache by hint alone — cache is keyed by `groupID`. The hint
  is stored in `entry.link` so a hint change forces refetch.
