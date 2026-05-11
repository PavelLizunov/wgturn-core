# Security policy

`wgturn-core` is an emergency-channel circumvention tool with a real
threat model: the people who use it depend on it staying functional
under aggressive network conditions, and a vulnerability disclosed
publicly before a fix is shipped can put users at material risk.

## Reporting a vulnerability

**Don't open a public GitHub / Forgejo issue for security reports.**
Email the maintainer through the channel you used to obtain the
binary, or open a private Forgejo issue against the
`slovn/wgturn-core` repository (web UI → New Issue → check
"Confidential"). If neither path is available, message any of the
project's published contact addresses with the subject prefix
`[wgturn-core security]`.

Please include:

- Affected version (`wgturn-cli -version` if available, or the
  commit SHA you built from).
- A concise reproduction: command line, config snippet, the
  observed vs. expected behaviour.
- Whether the issue is reachable from network or only by a local
  user with the share URL.
- Whether you intend to disclose publicly, and on what timeline.

You can expect an acknowledgement within a few days. Real fixes
ship as soon as we can verify them — there is no commercial SLA.

## What we consider in scope

- Authentication / authorisation bypasses: anything that lets a
  non-provisioned client get a tunnel up, or read another client's
  traffic.
- Memory-safety bugs in the Go code (panics that DoS the server,
  goroutine leaks observable under realistic load, deadlocks in
  the demuxer).
- Cryptographic mistakes: weakened DTLS configuration, key
  material accidentally logged or persisted, predictable
  generators, replay vulnerabilities in the proxy_v2 handshake.
- Server-side privilege issues: the server reading or writing
  outside `ConfPath` / its working directory, escaping its own
  privilege scope via the `wg syncconf` shell-out, leaking the WG
  private key through error messages.
- Wire-format malformations that crash the server or wedge active
  sessions.
- Anything that fingerprints the wgturn DTLS layer distinctly
  from genuine WebRTC media to a passive on-path observer — that
  is the entire point of the obfuscation and is treated as a
  correctness issue, not a feature request.

## What we do NOT treat as a security issue

- VK Calls upstream changes that break the provider (`vk` package
  stops fetching tokens, captcha flow changes). These are
  operational regressions — open a normal issue or PR.
- The ~200 KB/s per-source-IP bandwidth ceiling. This is an
  empirical VK shaping property, not a code bug.
- "I gave my `wgturn://` URL to someone untrustworthy and they
  used my tunnel." That's working as intended; revoke the peer
  on the server (`wgturn-cli revoke-url <name>`) and issue a fresh
  one.
- Russian law enforcement actions against an operator or end user.
  These are legal / operational risks documented in the README
  disclaimer; no code change addresses them.
- Bugs in the GPL `slovn/wgturn-server` legacy fork. Report those
  upstream against `kiper292/vk-turn-proxy`.

## Hardening posture

- DTLS uses pion/dtls v3 with `ExtendedMasterSecret = required`
  and a one-element cipher list
  (`TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256`). Self-signed certs
  are regenerated per server startup; identity is NOT
  authenticated through them — knowledge of the per-session
  16-byte UUID is the auth signal.
- Client / server keys live in `wgshare.Profile` and the share
  URL embeds the WireGuard private key. The same property as a
  wg-quick `.conf` attachment; distribute through trust channels
  only.
- `pkg/wgadmin.Server` writes `wg0.conf` atomically (temp file →
  fsync → rename). Failed provisions never call `wg syncconf`,
  preserving idempotency under retries.
- `internal/framing.ReadHandshake` is bounded to 17 bytes via
  `io.ReadFull` with a deadline — no unbounded-allocation vector
  through the handshake parser.

## Past advisories

None yet. This section will list any fixed CVEs / security-relevant
patches as they happen.
