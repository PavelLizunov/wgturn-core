# pkg/wgconf — WireGuard config parser with `#@wgt:` metadata

Parses standard `wg-quick` config files extended with our metadata
convention AND extracts the standard `[Interface]` / `[Peer]` sections,
so a single .conf file is enough to drive both the wgturn hub and the
embedded `pkg/wgkernel`. Vanilla WG tools see comments; wgturn-aware
code reads the metadata and the WG sections.

## Format

```ini
[Interface]
PrivateKey = ...
Address    = 10.7.0.2/24
DNS        = 1.1.1.1, 8.8.8.8
MTU        = 1280
#@wgt:EnableTURN     = true
#@wgt:Mode           = vk_link
#@wgt:VkLink         = https://vk.com/call/join/abcdef
#@wgt:PeerType       = proxy_v2
#@wgt:Streams        = 24
#@wgt:WatchdogTimeout= 30
#@wgt:Peer           = vps.example.com:56000
#@wgt:LocalListen    = 127.0.0.1:9000

[Peer]
PublicKey  = ...
PresharedKey = ...
Endpoint   = 127.0.0.1:9000
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 25
```

## What's here

- `parse.go` — single-file parser handling sections + `#@wgt:` metadata.
- `doc.go` — package overview.
- `parse_test.go` — table-driven coverage.

`Settings` carries:
- top-level wgturn fields (`EnableTURN`, `VkLink`, `Streams`, `Peer`, …)
- `Iface IfaceSection` (parsed `[Interface]`: PrivateKey, Address, DNS,
  MTU, ListenPort) — typed with stdlib `netip` types.
- `WGPeers []PeerSection` (parsed `[Peer]` sections in source order).

Two conversion methods:
- `Settings.ToTunnelConfig() (wgturn.Config, error)` — lifts wgturn
  metadata into a hub config skeleton (caller still sets Provider,
  Protector, Logger).
- The CLI builds a `wgkernel.Config` from `Iface` + `WGPeers` directly.
  We do NOT provide `ToKernelConfig()` here on purpose: it would force
  every wgconf importer to drag in `golang.zx2c4.com/wireguard`. Field
  shapes match exactly so the field-by-field copy is mechanical.

## Used by

- `cmd/wgturn-cli connect` — primary consumer; needs both halves of
  Settings.
- `cmd/wgturn-cli -config <path>` (legacy hub-only mode) — only reads
  `Settings.ToTunnelConfig()`, ignores `Iface` / `WGPeers`.

## Don't regress

- Don't break vanilla WG-tools compatibility. `wg-quick up` against the
  same file MUST still work (the `#@wgt:` lines are valid WG comments).
- Don't change the prefix `#@wgt:` — it's the agreed convention with
  `kiper292/wireguard-turn-android` and our handoff bundle's example
  configs.
- Field names in `Settings`, `IfaceSection`, `PeerSection` are part of
  the wire format. Don't rename.
- Don't add a `wgkernel` import here — keep the dep tree small for
  embedders that just want config parsing.
- Host-side wg-quick keys (`PostUp`, `PreDown`, `Table`, `FwMark`,
  `SaveConfig`, …) are silently ignored. That's intentional — they're
  outside wgturn-core's concern. Don't start "validating" them or
  recording them in `Unknown`; that map is for `#@wgt:` keys only.
