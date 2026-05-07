// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturnsrv

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"golang.zx2c4.com/wireguard/conn"
)

// WGKernelBackend bridges the server's per-session backend conn into
// an in-process wgkernel.Kernel. The bridge replaces the kernel's
// usual UDP socket: packets that the proxy hands the backend are
// fed straight into the kernel via its conn.Bind ReceiveFunc, and
// packets the kernel sends out are surfaced on the backend's net.Conn.
//
// The intended usage is:
//
//	backend := wgturnsrv.NewWGKernelBackend()
//	kernel, _ := wgkernel.New(cfg, tun, wgkernel.WithBind(backend.Bind()))
//	server, _ := wgturnsrv.New(wgturnsrv.Config{Backend: backend, ...})
//
// Today the backend is single-session: a second Open call returns an
// error. That is sufficient for the pair_test and any single-client
// soak; multi-peer "all-in-one" deployments where one kernel terminates
// many sessions need a fan-out variant which is left for a future
// version.
type WGKernelBackend struct {
	bind *kernelBind

	mu     sync.Mutex
	opened bool
}

// NewWGKernelBackend constructs an unbound backend. The Bind returned
// by Bind() must be plugged into a wgkernel.Kernel before Open is
// called; otherwise Send/Receive have nothing to talk to.
func NewWGKernelBackend() *WGKernelBackend {
	return &WGKernelBackend{bind: newKernelBind()}
}

// Bind returns the conn.Bind to pass to wgkernel.New via wgkernel.WithBind.
// The returned Bind is owned by the backend; do not pass it to multiple
// kernels.
func (b *WGKernelBackend) Bind() conn.Bind { return b.bind }

// Open returns a duplex net.Conn that pumps packets in and out of the
// embedded kernel. Subsequent Open calls error: this backend supports
// exactly one active session at a time.
func (b *WGKernelBackend) Open(_ context.Context, _ string) (net.Conn, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.opened {
		return nil, errors.New("wgturnsrv: WGKernelBackend already has an active session")
	}
	b.opened = true
	return &kernelBackendConn{
		bind:   b.bind,
		parent: b,
		closeC: make(chan struct{}),
	}, nil
}

// release re-arms the backend so a subsequent Open succeeds. Called by
// the conn's Close.
func (b *WGKernelBackend) release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.opened = false
}

// kernelBackendConn is the net.Conn the proxy reads/writes through.
// Read returns the next packet the kernel sent out (one packet per
// Read). Write pushes a packet into the kernel's Receive path.
type kernelBackendConn struct {
	bind   *kernelBind
	parent *WGKernelBackend
	closed atomic.Bool
	// closeC closes when the conn is Close()'d so blocked Reads can
	// unblock without polling. Independent of the bind's own
	// Open/Close cycle (which the kernel drives during normal config
	// changes).
	closeC chan struct{}
}

func (c *kernelBackendConn) Read(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	pkt, err := c.bind.recvFromKernel(c.closeC)
	if err != nil {
		return 0, err
	}
	n := copy(p, pkt)
	return n, nil
}

func (c *kernelBackendConn) Write(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	if err := c.bind.sendToKernel(p, c.closeC); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *kernelBackendConn) Close() error {
	if c.closed.CompareAndSwap(false, true) {
		close(c.closeC)
		c.parent.release()
	}
	return nil
}

func (c *kernelBackendConn) LocalAddr() net.Addr               { return kernelAddr{} }
func (c *kernelBackendConn) RemoteAddr() net.Addr              { return kernelAddr{} }
func (c *kernelBackendConn) SetDeadline(t time.Time) error     { return c.bind.setDeadline(t) }
func (c *kernelBackendConn) SetReadDeadline(t time.Time) error { return c.bind.setReadDeadline(t) }
func (c *kernelBackendConn) SetWriteDeadline(t time.Time) error {
	return c.bind.setWriteDeadline(t)
}

// kernelAddrName is the placeholder string we report through both
// kernelAddr methods and the singleton kernelEndpoint. It has no
// meaning beyond making log lines printable.
const kernelAddrName = "wgkernel"

// kernelAddr is a placeholder net.Addr; the in-process bind has no
// meaningful address. Both LocalAddr and RemoteAddr return one so log
// lines have something printable.
type kernelAddr struct{}

func (kernelAddr) Network() string { return kernelAddrName }
func (kernelAddr) String() string  { return kernelAddrName }

// kernelBind implements conn.Bind for in-process traffic between the
// server backend and a wgkernel.Kernel. It is intentionally simple:
// two channels (one per direction) plus a singleton Endpoint that
// the kernel uses to address its peer.
//
// The bind's Open/Close lifecycle follows wireguard-go's expectations:
// during normal startup and IPC config changes, the kernel does
// Close → Open cycles. Each Open creates a fresh "open generation"
// channel; the ReceiveFunc returned from that Open watches its own
// generation, so a Close from the kernel only cancels the matching
// ReceiveFunc, leaving the bind ready for a subsequent Open.
//
// Permanent shutdown (the user side of the bridge going away) lives
// on kernelBackendConn instead; its closeC unblocks the bind's
// sendToKernel / recvFromKernel helpers.
type kernelBind struct {
	// toKernel carries packets from the proxy backend to the kernel's
	// ReceiveFunc. Buffered to avoid blocking the proxy on a slow
	// kernel handshake; UDP semantics — drop on overflow.
	toKernel chan []byte

	// fromKernel carries packets the kernel sent. Backend Reads pull
	// from this channel.
	fromKernel chan []byte

	mu      sync.Mutex
	openGen chan struct{} // closed by Close; nil when the bind has no live ReceiveFunc

	readDdl  atomic.Pointer[time.Time]
	writeDdl atomic.Pointer[time.Time]

	endpoint kernelEndpoint
}

const kernelBindQueueDepth = 256

func newKernelBind() *kernelBind {
	return &kernelBind{
		toKernel:   make(chan []byte, kernelBindQueueDepth),
		fromKernel: make(chan []byte, kernelBindQueueDepth),
		endpoint:   kernelEndpoint{},
	}
}

// Open implements conn.Bind. The port argument is ignored — there is
// no real socket — but conn.Bind requires us to report a non-zero
// "actual" port back to the kernel so its UAPI surface looks sane.
// A subsequent Open without an intervening Close implicitly closes
// the previous generation so blocked ReceiveFuncs unblock.
func (b *kernelBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	if b.openGen != nil {
		// Implicit close: unblock the previous ReceiveFunc before
		// arming the new one. Using a select so re-Open by the same
		// caller (rare but legal) doesn't double-close.
		select {
		case <-b.openGen:
		default:
			close(b.openGen)
		}
	}
	gen := make(chan struct{})
	b.openGen = gen
	b.mu.Unlock()

	if port == 0 {
		port = 1 // any non-zero placeholder
	}

	recv := func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		// Block until a packet arrives or this open generation closes.
		// We produce at most one packet per call; multi-packet batches
		// don't help here because the source is a single channel.
		select {
		case <-gen:
			return 0, net.ErrClosed
		case pkt, ok := <-b.toKernel:
			if !ok {
				return 0, net.ErrClosed
			}
			n := copy(packets[0], pkt)
			sizes[0] = n
			eps[0] = &b.endpoint
			return 1, nil
		}
	}
	return []conn.ReceiveFunc{recv}, port, nil
}

// Close implements conn.Bind. Idempotent: closes the current open
// generation so its ReceiveFunc returns net.ErrClosed, and clears the
// pointer so a subsequent Open starts a fresh generation. Repeated
// Close calls without an intervening Open are safe no-ops.
func (b *kernelBind) Close() error {
	b.mu.Lock()
	gen := b.openGen
	b.openGen = nil
	b.mu.Unlock()
	if gen != nil {
		select {
		case <-gen:
		default:
			close(gen)
		}
	}
	return nil
}

// SetMark is a no-op: the in-memory bind has nothing to set marks on.
func (b *kernelBind) SetMark(uint32) error { return nil }

// Send implements conn.Bind. The kernel calls this to emit a packet
// towards its peer; we hand the bytes to the backend Reader. Drops on
// overflow (UDP semantics — higher layers retransmit if they care).
func (b *kernelBind) Send(bufs [][]byte, _ conn.Endpoint) error {
	for _, p := range bufs {
		if len(p) == 0 {
			continue
		}
		// Copy: the kernel reuses the buffer once Send returns.
		cp := make([]byte, len(p))
		copy(cp, p)
		select {
		case b.fromKernel <- cp:
		default:
			// fromKernel is full — drop the packet. UDP semantics:
			// the kernel re-transmits if the higher layer cares.
		}
	}
	return nil
}

// ParseEndpoint implements conn.Bind. The string argument is what the
// IPC config contains; for the in-memory bind it carries no real
// information, so we always return our singleton endpoint.
func (b *kernelBind) ParseEndpoint(string) (conn.Endpoint, error) {
	return &b.endpoint, nil
}

// BatchSize implements conn.Bind. Single-packet behaviour matches the
// channel-based queueing above.
func (b *kernelBind) BatchSize() int { return 1 }

// sendToKernel buffers a packet for the next ReceiveFunc call. The
// connCloseC channel comes from the user-side conn so a Close on the
// backend conn unblocks any in-flight Send.
func (b *kernelBind) sendToKernel(p []byte, connCloseC <-chan struct{}) error {
	cp := make([]byte, len(p))
	copy(cp, p)
	deadlineCh := b.writeDeadlineChannel()
	select {
	case b.toKernel <- cp:
		return nil
	case <-connCloseC:
		return net.ErrClosed
	case <-deadlineCh:
		return errBindDeadline
	default:
		// Queue full — drop. The kernel will see no packet; the
		// higher layer (WG handshake retransmit, TCP retx) handles
		// loss.
		return nil
	}
}

// recvFromKernel blocks until the next packet from the kernel arrives,
// the read deadline fires, or the user-side conn is closed.
func (b *kernelBind) recvFromKernel(connCloseC <-chan struct{}) ([]byte, error) {
	deadlineCh := b.readDeadlineChannel()
	select {
	case <-connCloseC:
		return nil, net.ErrClosed
	case pkt := <-b.fromKernel:
		return pkt, nil
	case <-deadlineCh:
		return nil, errBindDeadline
	}
}

// readDeadlineChannel returns a channel that fires when the current
// read deadline expires, or nil if none is set.
func (b *kernelBind) readDeadlineChannel() <-chan time.Time {
	d := b.readDdl.Load()
	if d == nil || d.IsZero() {
		return nil
	}
	return time.After(time.Until(*d))
}

func (b *kernelBind) writeDeadlineChannel() <-chan time.Time {
	d := b.writeDdl.Load()
	if d == nil || d.IsZero() {
		return nil
	}
	return time.After(time.Until(*d))
}

func (b *kernelBind) setDeadline(t time.Time) error {
	b.readDdl.Store(&t)
	b.writeDdl.Store(&t)
	return nil
}
func (b *kernelBind) setReadDeadline(t time.Time) error  { b.readDdl.Store(&t); return nil }
func (b *kernelBind) setWriteDeadline(t time.Time) error { b.writeDdl.Store(&t); return nil }

// errBindDeadline is the deadline-fired sentinel returned to the
// proxy. Wrapping a stdlib timeout type keeps net.Error.Timeout()
// behaviour intact for callers that switch on it.
var errBindDeadline = &bindDeadlineErr{}

type bindDeadlineErr struct{}

func (*bindDeadlineErr) Error() string   { return "wgturnsrv: bind deadline exceeded" }
func (*bindDeadlineErr) Timeout() bool   { return true }
func (*bindDeadlineErr) Temporary() bool { return true }

// kernelEndpoint is the singleton Endpoint used for both peer
// directions. The wgkernel uses it only for caching; we don't track
// real source/dest IPs because there's only one peer in scope.
type kernelEndpoint struct{}

func (*kernelEndpoint) ClearSrc()           {}
func (*kernelEndpoint) SrcToString() string { return kernelAddrName }
func (*kernelEndpoint) DstToString() string { return kernelAddrName }
func (*kernelEndpoint) DstToBytes() []byte  { return []byte{0, 0, 0, 0, 0, 0} }
func (*kernelEndpoint) DstIP() netip.Addr   { return netip.IPv4Unspecified() }
func (*kernelEndpoint) SrcIP() netip.Addr   { return netip.IPv4Unspecified() }
