# pkg/wgadmin — server-side wireguard provisioning

Public package that manages a wgturn server's WireGuard plane:
generates client keypairs, allocates client IPs, edits `wg0.conf`,
and runs `wg syncconf` so the live interface picks up changes
without bouncing existing sessions.

The CLI subcommands `provision-url` and `revoke-url` are thin
wrappers around `Server.Provision` / `Server.Revoke`. Embedders
running their own admin UI (web form, k8s controller, internal
tool) can import this package directly.

## What's here

- `doc.go` — usage shape, persistence model.
- `keys.go` — `GenerateKeypair`, `GeneratePresharedKey`,
  `PublicKeyFor` — pure-Go WG-tools-compatible key primitives
  (`crypto/rand` + `golang.org/x/crypto/curve25519`).
- `subnet.go` — `AllocateClientIP`, `existingPeerAddrs`,
  `ErrSubnetExhausted`. Skips network/broadcast/gateway and the
  /32 host addresses already claimed by existing peers.
- `wgconf.go` — `parseConf`, `writeConf`, `formatPeer`,
  `parsePrefixList`. Round-trips arbitrary wg-quick configs while
  preserving comments, PostUp/PostDown lines, and the
  `# wgturn-name = …` tag we use for friendly-name recovery.
- `server.go` — `type Server`, `NewServer`, `Provision`, `Revoke`,
  `List`, `ErrPeerExists`, `ErrPeerNotFound`. Thread-safe within
  one process via internal mutex; cross-process callers add their
  own `flock`.

## Critical invariants

### `wg0.conf` is the single source of truth

There's no separate `state.json`. Every Provision/Revoke
read-modifies-writes the conf file atomically (temp → fsync →
rename). The friendly name comes from a `# wgturn-name = <name>`
comment we drop right after the `[Peer]` section header so List
can round-trip it; peers added by hand without our tag come back
with `Name == ""` but their PublicKey / AllowedIPs are preserved.

This matches the legacy `provision-user.sh` / `list-users.sh` /
`revoke-user.sh` shell scripts byte-for-byte. They can coexist with
the Go API on the same wg0.conf — neither side mangles the other's
peer blocks.

### Curve25519 clamping is non-optional

`GenerateKeypair` applies the WG-spec clamping bits (sk[0] &= 248;
sk[31] &= 127; sk[31] |= 64) before deriving the public key.
Without these, the public key is not on the right subgroup and
existing wg implementations reject the peer with an obscure
"protocol error". A test pins the clamping bits explicitly.

### `wg syncconf` runs the stripped form, not the file

`writeConf` emits the full wg-quick layout (Address, DNS, PostUp,
comments). `wg syncconf` rejects most of those keys — it expects
only `[Interface] PrivateKey/ListenPort` and `[Peer] PublicKey/
PresharedKey/Endpoint/AllowedIPs/PersistentKeepalive`. So
`Server.sync` re-renders state into a stripped buffer and pipes
THAT through `wg syncconf <iface> /dev/stdin`, not the file.

### Atomic write or no write

`Server.writeState` writes to a temp file in the same directory,
fsyncs, then renames over the original. Failed Provision /
Revoke calls (bad name, missing PrivateKey, sync command non-zero
exit) leave the conf unchanged. Tests verify the
"failed Provision must not call sync" property.

### Single-session is enforced; cross-process needs flock

Internal `*sync.Mutex` serialises Provision / Revoke / List within
one process (multi-name CLI batches, embedded use). Across processes
(two operators racing two `wgturn-cli provision-url` calls), use
`flock /var/lock/wgturn-provision.lock` — same convention the
legacy shell scripts use.

## API shape

```go
srv := wgadmin.NewServer(wgadmin.Server{
    ConfPath:  "/etc/wireguard/wg0.conf",
    Interface: "wg0",
    Subnet:    netip.MustParsePrefix("10.7.0.0/24"),
    Endpoint:  "is-01.example.com:56000",
})

profile, err := srv.Provision("alice")
// profile is wgshare.Profile — Encode it for the wgturn:// URL.

err = srv.Revoke("alice")

peers, err := srv.List()
```

## Don't regress

- Don't write to `wg0.conf` non-atomically. Crashes mid-write would
  leave half-formed peer blocks that wg-quick (or wg syncconf via
  `Server.sync`) reads as garbage.
- Don't change the `# wgturn-name = …` tag format. The legacy shell
  scripts grep for it; if they stop finding it, `list-users.sh`
  starts hiding peers.
- Don't pass the unstripped wg0.conf to `wg syncconf`. PostUp /
  Address / DNS / FwMark / Table will all error out.
- Don't reuse a /32 that's still in another peer's AllowedIPs.
  AllocateClientIP's "taken" set is the safety net here; tests
  cover every-host-in-/30 and reuse-after-revoke.
- Don't expose the unexported `rawLines` field in Peer through API
  changes. Embedders using List get clean data; round-trip
  formatting is implementation-private.

## Tests

`go test -race ./pkg/wgadmin/...` covers:

- Keypair shape (44-char base64), clamping bits, public-key
  derivation round-trip, PSK uniqueness across calls.
- Subnet allocator: first-free, skip taken, skip broadcast (/30
  edge case), tiny subnet exhaustion (/31), filter to /32 entries
  in `existingPeerAddrs`.
- Provision happy path: writes [Peer] block with tag, runs sync
  with stripped form (asserts `Address` is NOT in stripped output),
  Profile fields populated.
- Sequential Provisions get .2 / .3 / .4 ordered.
- Duplicate name → `ErrPeerExists`, no sync call.
- Revoke removes the tag, frees the /32, surrounding peers
  preserved.
- Revoke missing name → `ErrPeerNotFound`, no sync.
- Atomic-write: failed read leaves conf unchanged + no sync.

The tests inject a `recordingSync` so they exercise the full code
path without forking the actual `wg` binary.
