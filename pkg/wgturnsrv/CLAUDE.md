# pkg/wgturnsrv — server-side proxy (Apache-2.0)

Server counterpart to `pkg/wgturn`: terminates wgturn proxy_v2
sessions on a UDP/DTLS listener and forwards the inner UDP payload
to a configurable Backend (typically a local WireGuard daemon).

## What's here

- `doc.go` — package doc, repeats the Apache-cleanliness rule.
- `server.go` — `Server` lifecycle (`New`/`Start`/`Stop`/`Stats`/
  `LocalAddr`), accept loop, `handleConn` demux entry, session-map
  helpers (`getOrCreateSession`, `removeSession`).
- `config.go` — `Config` + `Default*` timeout constants +
  `ErrInvalidConfig` validation surface.
- `backend.go` — `Backend` interface + `UDPBackend` (production:
  per-session UDP socket to a local WG daemon).
- `backend_kernel.go` — `WGKernelBackend` (in-process bridge to
  `pkg/wgkernel.Kernel` via a custom `conn.Bind`). Single-session.
- `session.go` — per-session state (`session` struct), the two
  goroutine pattern (`runBackend` + `runStream`), eviction-on-
  duplicate-streamID, atomic-counter round-robin, idempotent
  `terminate()`.
- `server_test.go` — lifecycle (Start/Stop/Stats/LocalAddr,
  ErrAlreadyStarted, ErrNotStarted, context-cancel-stops, Config
  validation).
- `session_test.go` — addStream eviction safety, runBackend
  round-robin, concurrent terminate idempotency, runStream
  backend-write-failure-terminates, drop-on-no-streams.
- `demux_test.go` — real DTLS clients against the listener, end-
  to-end stream round-trip via stub backend, two-stream round-
  robin, eviction-on-duplicate-streamID, HandshakeTimeout.
- `backend_kernel_test.go` — bind round-trip, single-session
  enforcement, Close-unblocks-ReceiveFunc, read-deadline timeout,
  concurrent stress.
- `pair_test.go` — keystone integration: WG handshake traverses
  the full stack (wgkernel#1 → Hub → in-process pion/turn → Server
  → demux → bind → wgkernel#2). If this is green, the server is
  real.

## Critical invariants

### Apache-2.0 cleanliness — non-negotiable

> The wire protocol matches `kiper292/vk-turn-proxy` (GPL-3.0); this
> package does not vendor or copy GPL-3.0 sources, only re-implements
> the same protocol from public documentation and observable wire
> format.

If you extend this package, you read upstream once for protocol
understanding and write everything from a blank buffer. Any literal
match longer than ~3 tokens with upstream lines is a copyright risk.
See `docs/N8-SERVER-PLAN.md` "Mission constraints".

### proxy_v2 wire format

Every payload-bearing UDP packet begins with:

```
[16 bytes: session UUID][1 byte: stream-id]
```

Encoded / decoded by `internal/framing`. Don't change anything in
this package without a coordinated client-side and field-deployed-
server-side update — old `wgturn-cli connect` binaries in the wild
must keep working.

### Backend invariants

- `Backend.Open` is called **once per session**, on first stream
  arrival. Subsequent streams in the same session reuse the same
  conn.
- The returned `net.Conn` is owned by the session — closed when the
  session terminates.
- For multi-client production use, build per-session source state
  the way `UDPBackend` does (fresh dial per Open). The legacy
  upstream relied on this, and WireGuard's session table on the
  exit side is happier with stable per-client source IPs.

### Session lifecycle

- Lazy backend Open under the session-map lock — concurrent
  handshakes for the same Session-ID must not double-dial.
- `runBackend` (1 goroutine/session) drives backend → stream
  round-robin via `atomic.Uint64` counter. StreamReadTimeout per
  Read; expired = WG side silent = `terminate()`.
- `runStream` (1 goroutine/stream) drives stream → backend. On
  backend write failure, `terminate()` the whole session — peer
  streams shouldn't keep shovelling into a dead fd.
- Eviction-on-duplicate-streamID closes the prior conn but
  `removeStream` only evicts a slot whose conn identity still
  matches the caller. The losing goroutine of an
  eviction-by-displacement therefore can't rip out the winner's
  slot when its Read eventually returns net.ErrClosed.
- `terminate()` is idempotent (CAS-guarded). First call: cancel
  ctx, close backend, close any remaining stream conns, fire
  `onTerminate`. Subsequent calls: no-op.

### `WGKernelBackend` is single-session today

`Open` returns an error on the second call. That's enough for
`pair_test.go` and any single-client soak. Multi-peer "all-in-one"
deployments need a fan-out variant where each Open returns a
wrapper conn the bind dispatches to by source endpoint — left for
later work.

The `kernelBind` survives `Close → Open` cycles because
wireguard-go drives those internally during normal IPC config
application (any `listen_port=N` line triggers a `BindUpdate`).
Each Open creates a fresh "open generation" channel that the
ReceiveFunc returned from that Open watches; Close closes the
generation but leaves the bind ready for re-Open. Permanent
shutdown lives on `kernelBackendConn.closeC`, not on the bind.

## Don't regress

- Don't add per-stream backend connections. Keep per-session.
- Don't add backpressure / channel queueing between DTLS and
  backend. The upstream model is "block on Write or drop on
  deadline timeout"; reproducing that pattern is what keeps the
  failure modes legible.
- Don't add TLS certificate verification. DTLS here is opaque
  framing, not authentication. The Session-ID handshake is the
  auth signal (knowing it = belonging to that session).
- Don't change the eviction-on-conflict semantics. `wgturn-cli
  connect` from a hard-killed previous run reconnects with the
  same Session-ID and Stream-ID; the server must take the new
  conn over and close the old one for the client to recover.
- Don't open multiple `Server` instances on the same UDP port. One
  process, one listener; horizontal scaling is via
  one-server-per-VPS, not one-port-multi-process.
- Don't deploy on is-01 without finishing the parallel-port soak
  in `docs/HANDBOOK.md` "Switching is-01". Pavel's only emergency
  tunnel goes through that box; breaking it has real cost.

## Tests

`go test -race ./pkg/wgturnsrv/...` runs the whole suite in ~3-4 s
under the race detector. Coverage includes:

- Lifecycle: New/Start/Stop, ErrAlreadyStarted, ErrNotStarted,
  context-cancel-stops, Config validation.
- Session demux: eviction-on-duplicate, round-robin distribution,
  drop-on-no-streams, terminate idempotency under concurrent
  callers, runStream backend-write-failure-terminates.
- DTLS round-trip via stub backend: end-to-end stream traffic,
  HandshakeTimeout closes silent clients, two-stream round-robin
  splits 6 packets exactly 3+3, eviction-on-duplicate over the
  wire closes the loser conn.
- `WGKernelBackend`: bind round-trip, single-session enforcement,
  Close-unblocks-ReceiveFunc, read-deadline timeout, concurrent
  64-packet stress.
- **Keystone**: `pair_test.go` brings up the entire stack
  (wgkernel#1 + Hub + in-process pion/turn + Server +
  WGKernelBackend + wgkernel#2) and asserts both kernels see a
  non-zero `last_handshake_time_sec` within 20 s. ~150 ms typical
  runtime.
