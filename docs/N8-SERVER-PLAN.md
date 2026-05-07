# N8 — Server-side re-implementation plan

> **Pre-requisite reading.** Before doing anything in this doc, read in
> order: top-level `CLAUDE.md`, `docs/HANDBOOK.md`, this file,
> `docs/ARCHITECTURE.md`, `docs/FINDINGS.md`, then
> `internal/proxy/stream.go` + `internal/proxy/hub.go` to understand
> the client side this server pairs with.

## Why this exists

`wgturn-core` (Apache-2.0) currently only contains the client side of
the wgturn proxy stack. The matching server lives in the sibling repo
`slovn/wgturn-server`, which is a fork of `kiper292/vk-turn-proxy`
under **GPL-3.0**. That split has two pain points:

1. **Two binaries to build, ship, configure, monitor**, when the work
   really is "two ends of the same wire format".
2. **Licence cliff**: we can never embed the server inside
   `wgturn-core` while it lives in a GPL repo (GPL is viral; embedding
   would force `wgturn-core` to GPL and break Apache-2.0 promises to
   downstream embedders, including `vpn-crypto` and any future SDK
   consumers).

The fix is to **re-implement** the server inside `wgturn-core` as
`pkg/wgturnsrv/` under Apache-2.0, **clean-room**, and expose it
through a `wgturn-cli serve` subcommand. End state is sing-box-style
unification: one binary, two roles selected by subcommand
(`connect` / `serve`), shared config parser, shared logging, shared
build pipeline, shared CI.

## Mission constraints (read these first)

### Apache-2.0 cleanliness — non-negotiable

`docs/ARCHITECTURE.md` already documents the rule we set for the
client side, and we must apply the same dispatch to the server side:

> The wire protocol matches `kiper292/vk-turn-proxy` (GPL-3.0); this
> repository does not vendor or copy GPL-3.0 sources, only
> re-implements the same protocol from public documentation and
> observable wire format.

Concretely this means:

- **Do not** open `slovn/wgturn-server/server/main.go` in the same
  editor window as the new file you are writing. Read it once for
  protocol understanding (what bytes go on the wire, in what order,
  with what timeouts) and close it. Write everything from a blank
  buffer.
- **Do not** copy comments, variable names, type names, struct field
  ordering, helper function shapes, or log messages from the upstream.
  Any literal lines longer than ~3 tokens that match upstream are a
  copyright risk.
- **Do not** import `github.com/cacggghp/vk-turn-proxy/...` from
  anywhere in the new code. The dependency graph stays Apache-2.0 +
  permissive deps only.
- The 271 lines of upstream `server/main.go` are a *specification*,
  not a *codebase*. If the protocol description here is unclear, dig
  into the wire — not the source.

### CGO_ENABLED=0 stays

CI tests are run with `CGO_ENABLED=0`; cross-compile is the same.
`pkg/wgturnsrv` must keep this invariant. `pion/dtls/v3` is pure-Go
already so this is automatic — just don't pull in any cgo dep.

### Backwards compatibility on the wire

A `wgturn-cli connect` of any vintage shipped to a user MUST keep
working against:
- the legacy `slovn/wgturn-server` binary (in case the operator
  hasn't switched yet);
- the new `wgturn-cli serve` binary;
- both, simultaneously, on different ports if needed.

This pins the wire format to *exactly* proxy_v2 as it stands today.
No "while we're at it" tweaks.

## Wire format reminder (for the new server)

This section is the contract the server must implement. Numbers are
from observation, not from upstream code.

### UDP carrier

- Listener: UDP/<port>. Default `:56000`.
- Each accepted DTLS session is one stream from one client.
- Multiple streams from the same logical client share a 16-byte
  Session-ID; the server demultiplexes on it.

### DTLS

- Library: `github.com/pion/dtls/v3`.
- Cipher suite: `TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256` (only one
  supported by upstream — be lenient, accept whatever pion negotiates).
- Self-signed certificate generated at server startup
  (`pion/dtls/v3/pkg/crypto/selfsign.GenerateSelfSigned`). Identity
  isn't authenticated — we use DTLS purely as encrypted framing that
  RKN DPI can't fingerprint as WireGuard.
- Extended Master Secret required.
- Connection-ID extension on, 8-byte random CID generator.
- Handshake timeout: 30 s.

### Per-stream handshake (proxy_v2)

Immediately after the DTLS handshake completes, the client writes
**exactly 17 bytes**:

```
+--------------------------------+----+
| Session-ID (16 bytes, UUID v4) | SI |
+--------------------------------+----+
                                  ^
                       1-byte Stream-ID, 0..255
```

The server reads 17 bytes (`io.ReadFull`) with a 5 s deadline, decodes
the Session-ID as the lower-cased hex string of those 16 bytes
(matches upstream's `fmt.Sprintf("%x", idBuf[:16])`), and looks up or
creates a `userSession` keyed by that hex string.

If a stream with the same `(Session-ID, Stream-ID)` already exists,
the server **evicts** the old one (closes the old conn, replaces it
with the new one in the same slot in `Streams[]`).

### Per-stream payload phase

After the 17-byte handshake, both directions carry raw datagrams up
to ~1500 B (MTU-clamped on the WG side; the server uses a 1600-byte
read buffer for safety). Each direction independently reads from one
endpoint and writes to the other, no further framing.

Per stream timeouts:
- Read deadline: refresh to `now + 5 min` before each Read. Reading
  longer means the stream is dead (TURN allocation expired).
- Write deadline: 10 s.

### Backend (the WG side)

The server has a single per-session `*net.UDPConn` opened to a
configurable address (the local WireGuard listen port, e.g.
`127.0.0.1:51820`). Both directions share that conn:

- DTLS-stream → `BackendConn.Write(buf)` (write deadline 5 s).
- `BackendConn.Read(buf)` → write to ONE of the active DTLS streams
  for that session, picked round-robin on a per-session counter.

Read deadline on backend is 5 min (matches stream side); on timeout
the session is torn down.

When the last stream closes or the 5-min backend read times out, the
session terminates and the entry in the manager map is removed. Any
remaining open DTLS conns are closed.

## Architecture

### Package layout

```
pkg/wgturnsrv/
    doc.go                       package docs
    server.go                    type Server, New, Start, Stop, Stats
    session.go                   type session (lowercase), demux logic
    backend.go                   type Backend interface + UDPBackend
    backend_kernel.go            type WGKernelBackend (pairs with pkg/wgkernel)
    config.go                    type Config — what runConnect/runServe consume
    server_test.go               unit tests for handshake parser,
                                 round-robin scheduler, eviction logic
    pair_test.go                 in-process Hub + Server + 2 wgkernels,
                                 expect WG handshake to complete
    CLAUDE.md                    package gotchas / don't-regress notes

cmd/wgturn-cli/
    serve.go                     runServe(args) — analogous to runConnect
    keygen.go                    runKeygen(args) — bonus, helper for ops
    main.go                      dispatch: connect / serve / keygen
```

### `Server` type — public API shape

```go
type Server struct {
    cfg     Config
    backend Backend
    logger  wgturn.Logger
    listener *dtls.Listener   // populated on Start
    sessions sync.Map         // key: hex-string sessionID, val: *session

    mu     sync.Mutex
    state  state
    cancel context.CancelFunc
    wg     sync.WaitGroup
}

type Config struct {
    ListenAddr string  // e.g. ":56000"
    Backend    Backend // required; constructor for UDPBackend / WGKernelBackend in same package
    Logger     wgturn.Logger
    HandshakeTimeout    time.Duration   // default 30s
    StreamReadTimeout   time.Duration   // default 5min
    BackendWriteTimeout time.Duration   // default 5s
}

func New(cfg Config) (*Server, error)
func (s *Server) Start(ctx context.Context) error  // returns once listener is bound
func (s *Server) Stop() error                       // graceful, drains sessions
func (s *Server) Stats() (Stats, error)
```

`Start` does NOT block — it returns once the listener accepts. Goroutines own the loops.

### `Backend` interface

```go
type Backend interface {
    // Open returns a connection to the WG side for one session. The
    // returned net.Conn is duplex; the server reads peer→client packets
    // from it and writes client→peer packets to it.
    Open(ctx context.Context, sessionID string) (net.Conn, error)
}

// UDPBackend dials a fresh UDP socket per session to the configured WG
// listener (matches upstream's net.Dial("udp", connectAddr) per
// session — keeps state isolated, wg-server happy with one source per
// session).
type UDPBackend struct{ Addr string }
func (b UDPBackend) Open(ctx context.Context, _ string) (net.Conn, error)

// WGKernelBackend pairs the server with an in-process wgkernel.Kernel
// — useful for in-process pair tests AND for "all-in-one" deployments
// that want both server proxy and embedded WG in the same binary.
type WGKernelBackend struct{ K *wgkernel.Kernel }
func (b WGKernelBackend) Open(ctx context.Context, _ string) (net.Conn, error)
```

The interface is small on purpose: a single `Open` call per session,
returns a duplex `net.Conn`. The server doesn't care what's behind it
— could be UDP to wg0, in-memory pipe to a wgkernel, or a mock for
tests.

### Demultiplexer — `session`

```go
type session struct {
    id      string  // hex of the 16-byte UUID
    backend net.Conn

    mu      sync.RWMutex
    streams []streamEntry  // small slice, lookup by streamID is O(N), N <= 32

    rrCounter uint32  // atomic; backend → DTLS round-robin index

    ctx    context.Context
    cancel context.CancelFunc
}

type streamEntry struct {
    id   byte
    conn net.Conn
}
```

Two goroutines per session:
1. `backendReader` (1 per session): `backend.Read` → pick stream
   round-robin → `stream.Write`. Refresh read deadline to 5 min on
   each iteration. On any backend error, cancel session.
2. `streamReader` (N per session, 1 per active stream): `stream.Read`
   → `backend.Write`. On any stream error, evict that stream from
   `streams[]` and exit.

Locking: `streams` slice is small (<32) and changes infrequently
(stream open / evict / close). RWMutex with reads dominant. The
backend connection is single-writer at a time guaranteed by
goroutine ownership — no lock needed on `backend`.

Eviction-on-conflict matches upstream: if a new connection arrives
for `(sessionID, streamID)` that's already in `streams[]`, close the
old one and put the new one in its place. This handles client
restart-without-clean-shutdown.

## Existing code to reuse

The client side already implements the matching half of this protocol
in `internal/proxy/stream.go`. Symmetric pieces we should factor out
into `internal/framing/` (NEW package, both client and server import
it):

| Symbol | What it does | Lives in today | Move to |
|---|---|---|---|
| 17-byte handshake encode | client writes Session-ID + Stream-ID | `internal/proxy/stream.go::pumpDTLS` lines around `hs[:16]` copy | `internal/framing.WriteHandshake(w io.Writer, sessionID []byte, streamID byte)` |
| 17-byte handshake decode | server reads same 17 bytes | (new) | `internal/framing.ReadHandshake(r io.Reader) (sessionID []byte, streamID byte, err error)` |
| DTLS config builder | self-signed cert + cipher suite list + connection-id-generator + extended-master-secret | duplicated between client + server | `internal/framing.NewDTLSConfig(role string)` (`role="server"` makes Listener) |

The factoring lifts ~30 lines from `stream.go` into a new package and
adds maybe 30 more lines on the server side. Worth doing — not for
LOC reduction but to make the protocol an explicit, single-source
artefact.

Note: `internal/` is module-internal; both `pkg/wgturn` and
`pkg/wgturnsrv` (and `cmd/wgturn-cli`) can import it. Outside callers
cannot, which is correct.

## Subtasks, in execution order

Suggested order — earlier tasks unblock later ones. Each step ends
with `make test && make lint` green before moving on.

### S1. Factor out `internal/framing` (≈45 min)

- Create `internal/framing/` package.
- Move handshake encode/decode + DTLS config builder there.
- Update `internal/proxy/stream.go` to call into `framing` instead.
- All existing tests stay green.

This is a refactor commit, ships independently. Lint + test green.

### S2. Skeleton `pkg/wgturnsrv` (≈45 min)

- `doc.go`, `config.go`, `server.go` with empty methods.
- `Backend` interface + `UDPBackend` only.
- `New` returns a `*Server`; `Start` opens the DTLS listener, runs an
  empty accept loop in a goroutine; `Stop` cancels and waits.
- No demux yet — accepted conns just close.
- Goal: compile, vet, lint clean, with one trivial test that confirms
  Start/Stop lifecycle.

### S3. Demultiplexer + per-session loops (≈90 min)

- Implement `session` struct with the two goroutine pattern from the
  Architecture section.
- Wire `accept` → `framing.ReadHandshake` → `getOrCreateSession` →
  `addStream`.
- Backend opened lazily on first stream of a session (per upstream
  behaviour).
- Round-robin counter via `atomic.Uint32`.
- Eviction on duplicate `(sessionID, streamID)`.

After this step the server should already work end-to-end on the
wire, just untested.

### S4. `WGKernelBackend` (≈30 min)

- New file `backend_kernel.go`.
- Wrapper around a `*wgkernel.Kernel` exposing a `net.Conn`-like
  interface to the server.
- This is the bridge that makes the in-process pair test possible AND
  enables a future "all-in-one" deployment (server proxy + embedded
  wg in the same binary on the VPS).

The exact glue: wgkernel exposes its UDP carrier via the bind
configured at construction (`conn.NewDefaultBind()`). For server-side
in-process use, swap the bind for one that reads/writes through a
`net.Pipe()`-style adapter. We already have `pkg/wgkernel.WithBind`
for exactly this kind of thing — see `kernel.go` line ~62.

### S5. In-process pair test (≈90 min)

`pkg/wgturnsrv/pair_test.go` analogue to
`pkg/wgkernel/kernel_test.go::TestKernel_Handshake_BothEnds` but with
the proxy in the middle:

```
[wgkernel#1 client] --UDP--> [Hub (proxy.Hub)] --DTLS over loopback-->
                                                                       ↓
[wgkernel#2 server] <--UDP-- [Server (wgturnsrv.Server)] <-------------┘
```

Asserts that within ~10 s both kernels see a non-zero
`last_handshake_time_sec`, proving the WG handshake traversed the
proxy stack.

Test setup:
- Both `Hub` and `Server` listen on `127.0.0.1:0` (kernel-picked
  ports) so tests don't conflict with each other or with running
  prod.
- `stub.Provider` returns a fixed credential pointing at a
  `pion/turn` test server in the same process — same trick already
  used in `internal/proxy/integration_test.go`.

If this test passes, the server is real. Period.

### S6. `wgturn-cli serve` subcommand (≈45 min)

- `cmd/wgturn-cli/serve.go` — analogous to `connect.go`.
- Reads `#@wgt:Listen`, `#@wgt:Backend` keys from a server-side
  `.conf`. Extend `pkg/wgconf` with these.
- Lifecycle: parse → build Server → Start → wait for SIGINT → Stop.
- `--listen-port` flag override (so smoke tests don't fight prod's
  `:56000`).

After this, `wgturn-cli serve <conf>` runs.

### S7. `wgturn-cli keygen` (≈30 min, optional but cheap)

- `cmd/wgturn-cli/keygen.go` — produces `wg`-compatible PrivateKey,
  PublicKey, PresharedKey using `golang.org/x/crypto/curve25519` +
  `crypto/rand` and base64 (already in stdlib). Output format
  matches `wg genkey | wg pubkey` so `scripts/provision-user.sh` can
  call this instead of requiring `wireguard-tools` on the operator's
  box.
- Tests: deterministic round-trip check + a "wg sees this as a valid
  key" sanity test (call into wgkernel to load a Config with this
  key).

### S8. Cross-compile + handoff bundle (≈30 min)

- `make cli` produces one binary per platform that has both
  `connect` and `serve` (and `keygen`) subcommands.
- Update `~/wgturn-handoff/README.md` to mention the server side
  briefly (most end users never need it; add a short section
  "operating your own server").
- The four embedded variants stay client-only; embedding Chromium
  inside a server binary is meaningless.

### S9. Switch plan for is-01 — DON'T DO BLINDLY (≈30 min planning,
then a maintenance window)

This step is operational, not coding. It's gated on every previous
step being green and on Pavel explicitly asking for it.

1. Drop new binary at `/usr/local/bin/wgturn-cli` on is-01 next to
   the existing `wgturn-server` from `/opt/wgturn-server/`.
2. Run new binary on **port `:56001`** (NOT 56000) for parallel
   smoke. Old server keeps `:56000`. Logs go to a separate file.
3. From .142, modify a *test* client config to use `:56001` (the
   prod handoff config still uses `:56000`). Verify end-to-end:
   `connect` → ping `93.95.226.167` → curl ifconfig.me through
   tunnel → exit IP correct.
4. Soak the new binary on `:56001` for 24 h: connect from .142
   continuously, watch resource usage, watch for goroutine leaks
   (`pprof` if needed). No-op crashes / dropped sessions.
5. Maintenance window:
   - Stop old `wgturn-server` (`docker stop wgturn-server` or
     systemd unit, depending on current deploy).
   - Restart new `wgturn-cli serve` on `:56000`.
   - Verify Pavel's existing handoff config keeps working without
     edits.
6. Keep `/opt/wgturn-server/` source around for one sprint as
   rollback. Revert path is just "stop new, start old".
7. After two weeks of green: archive `slovn/wgturn-server` repo
   (mark as Read-only in Forgejo settings, retain on GitHub mirror
   as historical reference).

### S10. Documentation (≈30 min)

- `docs/ARCHITECTURE.md` — add server-side block.
- `docs/HANDBOOK.md` — add "running a wgturn server" recipe.
- `docs/ROADMAP.md` — flip N8 to ✅, drop entry from "Next".
- `pkg/wgturnsrv/CLAUDE.md` — module docs + don't-regress checklist.
- Top-level `CLAUDE.md` — update status table.
- Mark issue #2 closed on Forgejo.

## Tests

### Unit tests (fast, hermetic)

- `framing_test.go` — handshake encode/decode round-trip, including
  edge cases (short read, zero-stream-id, boundary stream-id 255).
- `server_test.go` — eviction-on-duplicate, round-robin counter
  monotonicity, session cleanup on backend close.

### Integration test (the keystone)

`pair_test.go` — see S5. ONE test that brings up the full stack and
confirms WG handshake completes. If this test breaks, every regression
is caught at PR time.

### Negative tests

- Client opens DTLS but never sends 17-byte handshake → server times
  out and closes (within `HandshakeTimeout`).
- Backend Dial fails (port not listening) → session created, all
  streams from that session error and close cleanly. Server stays up.
- 33rd stream for a session arrives → either we cap at some N or the
  upstream just appends; document the choice in code comments.

## `wgturn-cli serve` UX (target)

Config file (server-side):

```ini
[Interface]
# Optional — used only if -tags allinone (S4 enables WGKernelBackend).
PrivateKey = ...
Address    = 10.7.0.1/24

[Peer]
# Standard wg-quick peers; ignored unless -tags allinone.
PublicKey  = ...
AllowedIPs = 10.7.0.2/32

#@wgt:EnableServer = true
#@wgt:Listen       = :56000
#@wgt:Backend      = udp:127.0.0.1:51820
# OR
#@wgt:Backend      = wgkernel    # uses the [Interface]/[Peer] above
```

Command line:

```sh
sudo wgturn-cli serve /etc/wgturn/server.conf -v
sudo wgturn-cli serve --listen :56001 --backend udp:127.0.0.1:51820 -v
```

Behaviour:
- Parse config → build Backend → Server.New → Server.Start.
- Stats line every `--stats` interval (default 30 s).
- Ctrl-C → Server.Stop → graceful, drains active sessions for up to
  10 s.

## Anti-patterns (do NOT do)

- ❌ Don't try to share the same `Hub` and `Server` UDP socket. They
  serve opposite roles (`Hub` listens for WG, `Server` listens for
  DTLS). Independent listeners.
- ❌ Don't add backpressure / queueing between DTLS and backend. The
  upstream model is "block on Write or drop on deadline timeout". Go
  channels with bounded capacity sound nice but they hide the failure
  mode. Reproduce the deadline pattern verbatim.
- ❌ Don't make the backend connection per-stream. Upstream is per-
  session for a reason: WG-side servers want a stable source IP per
  client across all carrier streams; rotating sources would re-trigger
  WG handshake unnecessarily. Keep per-session.
- ❌ Don't add TLS certificate verification. DTLS is opaque framing
  here, not authentication. The Session-ID handshake is the auth
  layer (knowing it = belonging to that session). Adding cert pinning
  would just create a new key-management problem with no security
  gain.
- ❌ Don't try to drop a "0.0.0.0" listener and a multi-instance
  pattern in v1. One process, one listener, that's it.
- ❌ Don't deploy the new server on top of the old one without
  finishing soak (S9 step 4). The current production tunnel is
  Pavel's only emergency channel; breaking it costs real connectivity.

## Definition of Done

All boxes ticked:

- [ ] `internal/framing/` exists, both client and server import from it.
- [ ] `pkg/wgturnsrv/` compiles, vets, lints clean.
- [ ] `pair_test.go` passes locally and on Forgejo Actions CI.
- [ ] `wgturn-cli serve` works against a real `wgturn-cli connect`
      (loopback test, both on dev machine).
- [ ] Cross-compile under all 5 GOOS×GOARCH combos green.
- [ ] Handoff binaries refreshed.
- [ ] `pkg/wgturnsrv/CLAUDE.md` written.
- [ ] `docs/ROADMAP.md` flips N8 to done.
- [ ] Issue #2 closed.
- [ ] (Operational, separate window) is-01 switched per S9; old
      `wgturn-server` archived after 2-week soak.

## Estimated effort

| Step | Estimate |
|---|---|
| S1 framing factor-out | 45 min |
| S2 server skeleton | 45 min |
| S3 demux + session loops | 90 min |
| S4 WGKernelBackend | 30 min |
| S5 in-process pair test | 90 min |
| S6 `wgturn-cli serve` | 45 min |
| S7 `wgturn-cli keygen` (optional) | 30 min |
| S8 cross-compile + handoff | 30 min |
| S9 switch plan execution (operational) | 90 min + 24 h soak |
| S10 documentation | 30 min |

**Coding total**: ~6 h (incl. keygen + handoff). **Plus a 24 h soak
window** before flipping is-01.

## When to start

- After a clean break — not at the tail of a long session. The
  factor-out (S1) and demux (S3) need fresh attention; subtle bugs
  in framing translate into "WG handshake never completes" failures
  that are easy to misdiagnose as crypto problems.
- Block off ~6 h of focused time on the dev side, then come back
  later for the operational soak.
- Pavel's standing rule: nothing on is-01 changes without his
  explicit go-ahead in a maintenance window.
