// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturnsrv

import (
	"context"
	"errors"
	"fmt"
	"net"
)

// Backend opens a per-session duplex connection to the data plane
// behind the proxy — typically a UDP socket bound to a local
// WireGuard listener, but the abstraction also covers in-process
// pipes used by the integration tests in pair_test.go.
//
// The Server calls Open exactly once per session, on first stream.
// The returned net.Conn is owned by the session; it is closed when
// the session terminates (last stream gone or backend read deadline
// exceeded).
type Backend interface {
	Open(ctx context.Context, sessionID string) (net.Conn, error)
}

// UDPBackend dials a fresh UDP socket for each session, connected to
// Addr. Matches the legacy server's per-session source-address
// behaviour: every client that opens a session ends up with a
// stable, distinct UDP source towards wg0, which keeps WireGuard's
// session table happy.
type UDPBackend struct {
	// Addr is the destination address in "host:port" form, e.g.
	// "127.0.0.1:51820". Required.
	Addr string
}

// Open dials a new connected UDP socket to Addr. The returned conn
// is a *net.UDPConn so callers can use Read/Write in addition to the
// generic net.Conn methods.
func (b UDPBackend) Open(ctx context.Context, _ string) (net.Conn, error) {
	if b.Addr == "" {
		return nil, errors.New("wgturnsrv: UDPBackend.Addr is empty")
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", b.Addr)
	if err != nil {
		return nil, fmt.Errorf("wgturnsrv: backend dial %s: %w", b.Addr, err)
	}
	return conn, nil
}
