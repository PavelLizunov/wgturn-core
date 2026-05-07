# pkg/wgshare — share-URL codec

Public package owning the `wgturn://` URL format used by
`wgturn-cli provision-url` (server-side admin) and
`wgturn-cli connect-url` (client) — and any embedder that wants to
distribute a wgturn profile as a single string.

## What's here

- `doc.go` — format spec, threat model, versioning policy.
- `share.go` — `Profile` struct, `Encode`, `Parse`, `ErrInvalidURL`,
  `wireFormat` (private JSON shape), `Validate`.
- `convert.go` — `Profile.ToTunnelConfig(vkLink)` and
  `Profile.ToKernelConfig()` for embedders going from URL to
  running tunnel.
- `share_test.go` / `convert_test.go` — round-trip + bad-input + every
  field-level Validate path.

## URL grammar

```
wgturn://<base64url(payload)>[#label]
```

`payload` is the base64url-encoded JSON of an internal `wireFormat`
struct (private — embedders must use Encode/Parse, never construct
the JSON by hand). Field tags are deliberately short to keep URLs
under ~350 characters total — phone screenshots and QR codes survive,
human-typed transcription doesn't (and shouldn't be attempted).

## Versioning

Every payload carries a `v` field, currently `1`. Bumping it is the
ONLY way to change the wire format. Old binaries Parse-error with
"unsupported version N (this binary handles v1)" rather than
silently misinterpreting renamed fields.

If you find yourself wanting to add a NEW field that defaults to
zero, you DON'T need to bump — JSON `omitempty` handles it. Bump
only when removing, renaming, or changing the semantic of an
existing field.

## What goes in the URL

- Server's WG public key
- Freshly generated client WG private key (the server hands this
  out per `provision-url` call; the same value never goes to two
  users)
- Optional preshared key
- wgturn DTLS endpoint (`host:port` of the server's `serve`
  listener — NOT the underlying WG endpoint)
- Client's assigned tunnel address (CIDR with the subnet's prefix
  length, e.g. `10.7.0.5/24`)
- AllowedIPs / DNS / MTU / PersistentKeepalive

## What does NOT go in the URL — and why

- **VK Calls invite link.** The VK link is a runtime parameter the
  user supplies on every `connect-url` call. Reasons:
  1. VK links rotate orders of magnitude more often than wg keys
     (per-call, not per-user).
  2. The same share URL is portable across users / devices — they
     each pick whatever VK link is current at the moment.
  3. If the VK link were embedded, every link rotation would
     invalidate every issued URL.
- **Server's private key.** Obviously. The server keeps it; only
  the public side leaves the box.

## Threat model

The URL is sensitive: anyone who reads it gets full tunnel access
(same property as a wg-quick `.conf` attachment). Distribute through
a trust channel (Signal, Threema, in-person paper) and rotate by
revoking the corresponding `[Peer]` server-side via
`wgturn-cli revoke-url <name>`. There's no in-band revocation
primitive — once issued, a URL stays valid until the server stops
recognising the keypair.

## Don't regress

- Don't rename JSON tag fields without bumping `formatVersion`.
- Don't put the VK link or the server's private key into the URL.
- Don't add a "send everything through HTTPS to a fancy revocation
  endpoint" — the design point is operating offline / out-of-band,
  matching how vless:// / wg-quick conf distribution works today.
- Don't make `Profile.Encode` lossy. Round-trip `Encode` → `Parse`
  → `Encode` must produce identical strings; this is what the
  pair_test for the CLI implicitly relies on.

## Tests

`go test -race ./pkg/wgshare/...` covers:

- Round-trip with all fields set (label, server pub, client priv,
  PSK, endpoint, address, AllowedIPs ipv4+ipv6, DNS, MTU, keepalive).
- Round-trip with only the four required fields (omitempty leaves
  optionals out of the JSON, URL stays compact).
- Label with whitespace / special chars (escaped via url.QueryEscape).
- Every malformed shape: empty, wrong scheme, missing payload, bad
  base64, bad JSON, unsupported version, missing required fields,
  bad CIDR/Addr inside otherwise-OK JSON. All chain to
  `ErrInvalidURL`.
- ToTunnelConfig / ToKernelConfig field copy + AllowedIPs default
  fallback when the Profile is sparse.
