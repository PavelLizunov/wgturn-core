// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturn

import "syscall"

// SocketProtector is a platform abstraction over "exclude this socket
// from the host VPN routing table". The Tunnel calls Protect for every
// outgoing socket it opens (both for HTTP credentials fetches and for
// the carrier itself), so that, for example, an Android VpnService can
// hand the file descriptor to VpnService.protect(fd) and the carrier
// traffic does not get routed back into the very tunnel we are trying
// to bring up.
//
// On platforms where this is unnecessary (typical desktop, where the
// TUN device drives the routing table directly and the TURN client
// runs in userspace), use NoopProtector.
type SocketProtector interface {
	// Protect is invoked synchronously inside (*net.Dialer).Control just
	// before the connect/listen call. It MUST return quickly. If it
	// returns a non-nil error, the dial fails and is propagated to the
	// caller.
	//
	// fd is a raw file descriptor (int) on Unix-like platforms and a
	// SOCKET (uintptr) on Windows. Implementations should not retain it.
	Protect(fd uintptr) error
}

// NoopProtector implements SocketProtector by doing nothing. It is the
// correct choice on Linux/macOS/Windows desktop where the tunnel and the
// host process share routing fate normally.
type NoopProtector struct{}

// Protect satisfies the SocketProtector interface.
func (NoopProtector) Protect(uintptr) error { return nil }

// FuncProtector adapts a plain function to the SocketProtector interface.
// Convenient for one-off platform glue (CGo callbacks, JNI bridges, etc.).
type FuncProtector func(fd uintptr) error

// Protect satisfies the SocketProtector interface.
func (f FuncProtector) Protect(fd uintptr) error { return f(fd) }

// ControlFunc returns a function suitable for net.Dialer.Control / .ListenConfig.Control,
// invoking the SocketProtector for each opened socket. This is the canonical way
// to plug a SocketProtector into a Go *net.Dialer.
func ControlFunc(p SocketProtector) func(network, address string, c syscall.RawConn) error {
	if p == nil {
		p = NoopProtector{}
	}
	return func(_, _ string, c syscall.RawConn) error {
		var protectErr error
		if err := c.Control(func(fd uintptr) {
			protectErr = p.Protect(fd)
		}); err != nil {
			return err
		}
		return protectErr
	}
}
