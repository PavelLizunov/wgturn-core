# vpn-client — minimal embedder example

This example shows how to embed `wgturn-core` into a Go application
that drives a wgturn VPN end-to-end from a single share URL plus a
VK Calls link. ~120 lines of code, no third-party deps beyond what
`wgturn-core` already pulls in.

## Build

```bash
go build -o vpn-client ./examples/vpn-client
```

## Run

The share URL is what an admin emits via `wgturn-cli provision-url`.
The VK link is whatever public VK Calls invite the user has at the
moment — same value you'd pass to `wgturn-cli connect --vk-link`.

```bash
sudo ./vpn-client \
    -url 'wgturn://eyJ2IjoxLCJzcCI6...#alice' \
    -vk-link 'https://vk.com/call/join/<your-call-id>' \
    -chrome-url 'http://127.0.0.1:9222'
```

You need a Chrome instance with `--remote-debugging-port=9222`
running for the captcha solver. Either spawn it yourself or copy
`cmd/wgturn-cli/chrome.go`'s auto-launch code.

## What the example does NOT do

- **Host-side networking**: no `ip link`/`ip addr`/`ip route` calls.
  Embedders own their UI and platform conventions; on Linux the
  copy-paste-able Linux equivalent is in `cmd/wgturn-cli/hostsetup.go`.
- **Auto-Chrome**: no browser is spawned for you. The example takes
  an explicit `--chrome-url`. `cmd/wgturn-cli/chrome.go` shows the
  full auto-launch flow if you need it.
- **Recovery / reconnect**: a single SIGINT tears everything down.
  Embedders typically wrap the lifecycle in a state machine that
  reconnects on stream failure.

## Architecture overview

```
+------------------+
|  share URL       |   wgshare.Parse → wgshare.Profile
|  (wgturn://...)  |
+--------+---------+
         |
         v
+------------------+      +-------------------+
|  Profile.Tunnel  |      |  Profile.Kernel   |
|  Config(vkLink)  |      |  Config()         |
+--------+---------+      +---------+---------+
         |                          |
         v                          v
+------------------+      +-------------------+
|  wgturn.Tunnel   | <---- WithTurnTunnel ---  |  wgkernel.Kernel  |
|  (proxy hub)     |    rewrites peer endpoint |  (embedded WG)    |
+--------+---------+                           +---------+---------+
         |                                               ^
         | DTLS over TURN                                | TUN packets
         v                                               |
+------------------+                            +--------+----------+
|  Server (your    |                            |  System TUN device|
|  is-01 wgturn-   |                            +-------------------+
|  cli serve)      |
+------------------+
```

The `wgshare.Profile` is the linchpin: it carries every key, IP, and
option the client needs except the VK link, which is a runtime
parameter. That single split (URL vs. VK link) is what lets the same
URL work across users / devices while the VK link rotates per
session.
