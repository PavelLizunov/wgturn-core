# pkg/wgconf — WireGuard config parser with `#@wgt:` metadata

Parses standard `wg-quick` config files extended with our metadata
convention. Vanilla WG tools see comments; we read the metadata.

## Format

```ini
[Interface]
PrivateKey = ...
Address    = 10.7.0.2/32
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
Endpoint   = 127.0.0.1:9000
AllowedIPs = 0.0.0.0/0
```

## What's here

- `parser.go` — line-based parser tolerant of section headers, comments,
  whitespace.
- `settings.go` — `Settings` struct with all `#@wgt:` fields.
- `tunnel_config.go` — `Settings.ToTunnelConfig() (wgturn.Config, error)`
  conversion.
- `parser_test.go` — table-driven coverage.

## Used by

`cmd/wgturn-cli -config <path>`. End users hand-author or download a
`.conf`; the CLI parses + uses it.

## Don't regress

- Don't break vanilla WG-tools compatibility. `wg-quick up` against the
  same file MUST still work (the `#@wgt:` lines are valid WG comments).
- Don't change the prefix `#@wgt:` — it's the agreed convention with
  `kiper292/wireguard-turn-android` and our handoff bundle's example
  configs.
- Field names in `Settings` are part of the wire format. Don't rename.
