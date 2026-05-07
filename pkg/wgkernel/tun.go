// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgkernel

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"

	"golang.zx2c4.com/wireguard/tun"
)

// NewSystemTUN creates a real OS TUN device. Wraps wireguard-go's
// tun.CreateTUN, which is platform-specific:
//
//   - Linux:   open /dev/net/tun, ioctl TUNSETIFF (needs CAP_NET_ADMIN)
//   - Windows: wintun.dll (needs admin / driver installed)
//   - macOS:   utun (needs sudo or a SystemExtension)
//   - Mobile:  use NewTUNFromFD instead — VpnService / NEPacketTunnelProvider
//     hand you an open FD.
//
// name is the desired interface name (Linux/macOS), ignored on Windows
// where wintun assigns one. mtu defaults to DefaultMTU when zero.
func NewSystemTUN(name string, mtu int) (tun.Device, error) {
	if mtu == 0 {
		mtu = DefaultMTU
	}
	return tun.CreateTUN(name, mtu)
}

// NewTUNFromFD is implemented per-platform: tun_fromfd_linux.go provides
// the real Linux/Android version on top of wireguard-go's
// tun.CreateUnmonitoredTUNFromFD, while tun_fromfd_other.go returns a
// "not supported" error everywhere else (macOS desktop, Windows). iOS
// uses NEPacketTunnelProvider which doesn't go through this API.
//
// Splitting it into build-tagged files keeps `cmd/wgturn-cli` cross-
// compilable for darwin/windows; before this split,
// tun.CreateUnmonitoredTUNFromFD's linux-only symbol broke the desktop
// build the moment connect.go imported wgkernel.

// MemoryTUN is a tun.Device whose Read/Write paths terminate in
// in-process channels. It exists for tests and for headless
// applications that want to consume IP packets directly without
// going through the OS TUN layer.
//
// Two MemoryTUNs are typically created via NewMemoryTUNPair; what
// one writes the other reads.
type MemoryTUN struct {
	name    string
	mtu     int
	in      chan []byte   // packets to be read by the device's caller
	out     chan []byte   // packets written by the device's caller
	done    chan struct{} // closed on Close — unblocks Read immediately
	events  chan tun.Event
	closed  atomic.Bool
	closeMu sync.Mutex
}

// NewMemoryTUNPair returns two paired MemoryTUNs whose IP-layer sides
// are crossed: a.Write(p) is observable as a packet on b.Read, and
// vice versa. Useful for end-to-end WG handshake tests in a single
// process without root or netstack.
//
// Both devices report the same MTU. Buffer size sets the per-direction
// packet queue depth (16 is sufficient for handshake + a few exchange
// packets; raise it if the test sends bursts).
func NewMemoryTUNPair(name string, mtu, buffer int) (a, b *MemoryTUN) {
	if mtu <= 0 {
		mtu = DefaultMTU
	}
	if buffer <= 0 {
		buffer = 16
	}
	ab := make(chan []byte, buffer) // a.out -> b.in
	ba := make(chan []byte, buffer) // b.out -> a.in
	a = &MemoryTUN{
		name:   name + "-a",
		mtu:    mtu,
		in:     ba,
		out:    ab,
		done:   make(chan struct{}),
		events: make(chan tun.Event, 1),
	}
	b = &MemoryTUN{
		name:   name + "-b",
		mtu:    mtu,
		in:     ab,
		out:    ba,
		done:   make(chan struct{}),
		events: make(chan tun.Event, 1),
	}
	a.events <- tun.EventUp
	b.events <- tun.EventUp
	return a, b
}

// File satisfies tun.Device. Returns nil — there is no underlying FD.
func (m *MemoryTUN) File() *os.File { return nil }

// Read pulls one packet from the in-queue and copies it into bufs[0].
// MemoryTUN reads at most one packet per call regardless of the
// caller's BatchSize; this is sufficient for our test shapes and
// keeps the implementation small.
//
// Read blocks until either a packet arrives, the partner's queue is
// closed (returns os.ErrClosed), or our own Close is called (also
// returns os.ErrClosed). The local-close signal is critical:
// wireguard-go's RoutineReadFromTUN holds Read across the whole
// device lifetime, and device.Close waits for it to unblock — without
// our own done-channel, closing only the partner's queue isn't
// enough to free the goroutine.
func (m *MemoryTUN) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	if m.closed.Load() {
		return 0, os.ErrClosed
	}
	var pkt []byte
	var ok bool
	select {
	case <-m.done:
		return 0, os.ErrClosed
	case pkt, ok = <-m.in:
		if !ok {
			return 0, os.ErrClosed
		}
	}
	if len(bufs) == 0 || len(sizes) == 0 {
		return 0, errors.New("memtun: empty bufs/sizes")
	}
	dst := bufs[0]
	if offset+len(pkt) > len(dst) {
		return 0, errors.New("memtun: packet exceeds buffer")
	}
	n := copy(dst[offset:], pkt)
	sizes[0] = n
	return 1, nil
}

// Write enqueues each non-empty packet in bufs to the out-queue.
// Returns the count of packets written.
func (m *MemoryTUN) Write(bufs [][]byte, offset int) (int, error) {
	if m.closed.Load() {
		return 0, os.ErrClosed
	}
	written := 0
	for _, b := range bufs {
		if len(b) <= offset {
			continue
		}
		// Copy because the caller may reuse the buffer immediately.
		pkt := make([]byte, len(b)-offset)
		copy(pkt, b[offset:])
		select {
		case m.out <- pkt:
			written++
		default:
			// Drop on overflow rather than block forever — matches
			// real-TUN backpressure semantics.
			return written, errors.New("memtun: out queue full")
		}
	}
	return written, nil
}

// MTU satisfies tun.Device.
func (m *MemoryTUN) MTU() (int, error) { return m.mtu, nil }

// Name satisfies tun.Device.
func (m *MemoryTUN) Name() (string, error) { return m.name, nil }

// Events satisfies tun.Device. We send EventUp at construction so the
// WG device transitions to running immediately.
func (m *MemoryTUN) Events() <-chan tun.Event { return m.events }

// Close satisfies tun.Device. Idempotent.
func (m *MemoryTUN) Close() error {
	m.closeMu.Lock()
	defer m.closeMu.Unlock()
	if m.closed.Swap(true) {
		return nil
	}
	close(m.done) // unblock our own pending Read
	close(m.events)
	close(m.out) // unblock the partner's pending Read
	// We do NOT close m.in — the paired MemoryTUN owns its writes.
	return nil
}

// BatchSize satisfies tun.Device. wireguard-go uses this as a hint for
// vectored I/O; our MemoryTUN handles one packet at a time.
func (m *MemoryTUN) BatchSize() int { return 1 }
