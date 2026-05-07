# wgturn-core FINDINGS

Things we learned the hard way, with empirical evidence. Read this
**before** trying any of the things listed under "anti-patterns" — we
have already done the experiments and they don't work.

## Bandwidth ceiling

**Claim**: Single source IP can pump at most ~200 KB/s through VK Calls
TURN, regardless of streams / multi-link / wgturn-server count.

### Evidence

| Config | Speed | Notes |
|---|---|---|
| `-streams 4`, 1 link | ~30 KB/s | baseline |
| `-streams 16`, 1 link | ~98 KB/s | 4× from streams alone |
| `-streams 16`, 4 links round-robin | ~135 KB/s | only +35% from multi-link |
| **`-streams 24`, 4 links (sweet spot)** | **~197 KB/s** | reproducible |
| `-streams 32`, 4 links | 262 KB/s peak then flaky | hits VK rate-limit on tokens |
| `-streams 48+` | timeout at startup | too many parallel cred-fetches |

### Why

VK shapes per **source IP**, not per-call or per-allocation. We confirmed
this two ways:

1. Multi-link (4 distinct call sessions) gave only +35%, not 4× — meaning
   VK aggregates shaping across all sessions from the same IP.
2. After ~5 cred-fetches in 10 minutes, VK starts denying anonymous
   tokens for ~30 min. Per-IP rate-limit on `login.vk.ru/?act=get_anonym_token`.

### CPU / network are NOT the bottleneck

Verified during 90-second BW test on 2026-05-06:

- `wgturn-server` on is-01: **0.0% CPU** (load avg 0.02-0.04)
- `wgturn-cli` on .142: 6-13% CPU pulse
- Direct `.142 → cachefly` (no tunnel): **64 MB/s** baseline
- Direct `.142 → is-01:51820/udp` (raw WG): **123 B/s** (RKN throttles
  WG handshake fingerprint specifically — that's why the wgturn DTLS
  wrapper exists)

### Implication

Don't try to scale bandwidth from one device. Period. The "emergency
channel" framing in README reflects this. For consumer VPN use, point
users at xray/REALITY through their own RU VPS — different tool.

## Yandex Telemost is a walled garden

**Claim**: Yandex Telemost's TURN servers (`turn.tel.yandex.net:443`)
refuse to relay packets to any peer IP outside Yandex's own SFU AS.

### Evidence

Test (2026-05-06):
- Built full `pkg/wgturn/provider/yandex` GOLOOM client (HTTP step 1
  + WebSocket hello/ack/serverHello flow).
- Cred-fetch succeeds: `turn.tel.yandex.net:443` + ephemeral
  `1778182412:telemost:<rand>:<conf-uuid>` username.
- Send TURN-relayed UDP to `93.95.226.167:56000` (is-01).
- `tcpdump -i any udp port 56000` on is-01 during 25 sec test:
  **0 packets received**.

DTLS handshake fails with `context canceled` because nothing comes back
through the relay.

### Why

Their TURN service is purpose-built for Yandex's own SFU media plane. It
likely enforces a peer-IP allowlist of Yandex SFU addresses
(`5.255.x.x`, `37.9.x.x`, `77.88.x.x`). Same pattern we saw with
Wildberries (`jaykaiperson/lionheart`, abandoned for the same reason).

### Implication

`pkg/wgturn/provider/yandex` is dead code as a VPN backend. It's kept
because:
1. The credential extraction is correct — useful for non-relay research.
2. The CLI router dispatches Telemost-shaped hints somewhere (better an
   explicit "peer-IP denied" failure than a misroute).
3. If Yandex relaxes the filter, our provider works without code change.

## Other RTC services are dead ends too

Researched 2026-05-06 (see chat history). Findings:

- **OK.ru / Mail.ru / MAX**: share VK's TURN backend (same AS / same
  shaping). No capacity gain. MAX additionally requires SMS verification
  (`auth_token` from OneMe WebSocket gated behind a Russian phone), so
  not anonymous.
- **Sferum** (school messenger): VK-owned, almost certainly shares VK
  Calls backend. Not worth implementing.
- **Whereby**: foreign service, blocked. "WB" in some references
  actually means Wildberries Stream, not Whereby.
- **Wildberries** (`jaykaiperson/lionheart`): TURN servers
  enforce strict `denied-peer-ip` allowlist. 403 Forbidden. Author
  abandoned. Dead end.
- **Telegram**: uses MTProto Reflectors, not standard TURN. No
  anonymous TURN credentials. Not applicable.
- **Astra / Compass / corp messengers**: account-gated, no anonymous flow.

**Conclusion**: VK Calls is the only viable RTC-backend. Don't waste
time researching others until VK changes its API.

## Captcha submit field-name footgun

**Symptom**: VK accepts the success_token from the not-a-robot widget,
but then immediately demands ANOTHER captcha for the same step3 retry.

**Cause**: Wrong field names. VK's API has TWO captcha submit conventions:

| Mode | Field names |
|---|---|
| Legacy text image | `captcha_sid` + `captcha_key` |
| Not-a-robot redirect | `captcha_sid` + `captcha_ts` + `captcha_attempt` + `success_token` + empty `captcha_key` + `is_sound_captcha=0` |

If you send `captcha_token` (intuitive but wrong) or omit `captcha_ts` /
`captcha_attempt`, VK silently re-challenges. The reference is
`cacggghp/vk-turn-proxy/client/main.go getTokenChain`.

This is encoded in `pkg/wgturn/provider/vk/captcha.go applySolution` —
do not regress it.

## CDP solver works because real Chrome passes the fingerprint

VK's not-a-robot challenge runs a JS proof-of-work, fingerprints the
browser (`navigator.webdriver`, `hardwareConcurrency`, languages,
notification permission, …), then requires an AES-encrypted answer
payload using keys baked into a 800 KB JS bundle.

We don't replicate any of that. We just point a real Chrome at the
challenge URL, click the checkbox, harvest the `success_token`. Chrome
does all the bot-defeat work for us. ~1 sec end-to-end.

If Chrome ever stops passing the checkbox-only path (i.e., VK always
escalates to slider regardless of fingerprint), we'd have to either:
- Solve the slider via image processing (port `slider_captcha.go`)
- Use a paid 2captcha-style service

`ChainSolver` already lets us layer fallbacks.

## VK rate-limit on anonymous tokens is per source IP

We exhausted .142's quota during a single afternoon of testing
(~30 fetches in 10 minutes). VK started returning "operation was
canceled" on `login.vk.ru/?act=get_anonym_token` — it was rate-limit
masking as a network-cancel error.

**Reset window**: ~30 minutes of no fetches.

A real user fetching every ~9 minutes (TURN allocation expiry) won't
hit this. Only happens during dev/testing.

## RKN blocks WireGuard fingerprint regardless of destination

Test (2026-05-06) from .142 (RU residential) to is-01 (foreign):

```
Direct UDP/51820 WireGuard: 123 B/s (essentially zero)
Through wgturn (DTLS-wrapped): 100-200 KB/s
```

The `123 B/s` direct test had a successful WG handshake (WG protocol
handshake completed) but data plane got crushed to nothing. RKN's DPI
fingerprints WG's Noise framework on UDP/51820 specifically.

This is the entire reason `wgturn-core` exists — wrap WG packets in
DTLS-over-STUN-ChannelData so they look like RTC media.

## Multi-source from foreign VPS doesn't survive white-list mode

We tested with `nk-01` (Amsterdam) as a 2nd source IP. It works in
**normal RKN mode** because foreign UDP isn't blocked aggressively.

In **white-list mode** (during real shutdowns), `.142 → nk-01:9101`
direct UDP would be blocked because Amsterdam isn't whitelisted.

For white-list-mode multi-source, source VPSes must be **Russian** (so
the `.142 → VPS_N` hop is RU-to-RU, allowed). Then each VPS makes its
own VK call from its own RU IP, and VK's per-IP cap applies independently.

But: see "Bandwidth ceiling" — multi-source only helps if you have
multiple physical devices/VPSes. For a single user it doesn't.

## sing-box / xray architecture pattern

`wgturn-core` already follows it:

- `pkg/wgturn` = stable core API (Tunnel, Config, Stats)
- `pkg/wgturn/provider/*` = optional providers; embedders import only
  what they need (Go's import system means unused packages don't bloat
  the binary)
- `pkg/wgturn/provider/vk/captchasolve` = optional captcha solvers
  (CDP exists; native + embedded + 2captcha planned)
- `pkg/wgkernel` = optional embedded WG userspace
- `cmd/wgturn-cli` = thin CLI wrapper, not the API

Embedders (e.g., a hypothetical custom VPN client) can:
```go
import "github.com/PavelLizunov/wgturn-core/pkg/wgturn"
import "github.com/PavelLizunov/wgturn-core/pkg/wgturn/provider/vk"
// pick captcha solver of choice
tn, _ := wgturn.New(wgturn.Config{Provider: vk.New(...)})
tn.Start(ctx)
```

This is intentional — keeps the SDK minimal and lets embedders avoid
heavy dependencies they don't need.
