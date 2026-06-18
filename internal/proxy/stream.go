// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cbeuw/connutil"
	"github.com/pion/dtls/v3"
	"github.com/pion/logging"
	"github.com/pion/turn/v5"

	"github.com/PavelLizunov/wgturn-core/internal/creds"
	"github.com/PavelLizunov/wgturn-core/internal/framing"
)

// credsAdapter bridges proxy.Provider (which returns proxy.Credentials)
// into creds.Provider (which returns creds.Credentials). Both types are
// identical structurally; this wrapper avoids a circular import.
type credsAdapter struct{ p Provider }

// Fetch satisfies creds.Provider.
func (a credsAdapter) Fetch(ctx context.Context, hint string, streamID int) (creds.Credentials, error) {
	c, err := a.p.Fetch(ctx, hint, streamID)
	if err != nil {
		return creds.Credentials{}, err
	}
	return creds.Credentials{
		Username:   c.Username,
		Password:   c.Password,
		ServerAddr: c.ServerAddr,
		ExpiresIn:  c.ExpiresIn,
	}, nil
}

// stream is one of N parallel TURN streams owned by a Hub. Each stream
// runs an independent TURN allocation; if PeerType is proxy_v* it wraps
// the relay in DTLS and (for proxy_v2) sends a 17-byte
// session-id+stream-id handshake on top.
type stream struct {
	id  int
	hub *Hub

	// in carries packets coming from the local listener that this stream
	// should send out via TURN. The Hub round-robins among ready streams.
	in chan []byte

	// peer is the latest source address seen on the local listener. RX
	// from TURN is sent back to this address.
	peer atomic.Pointer[net.Addr]

	// ready flips to true when the stream is fully established
	// (allocation + DTLS handshake done, if applicable).
	ready atomic.Bool

	// stats counters maintained by the run loops; the Hub aggregates.
	bytesTx, bytesRx     atomic.Uint64
	packetsTx, packetsRx atomic.Uint64
	dropsTx              atomic.Uint64
	errorsTx, errorsRx   atomic.Uint64
}

// run is the main per-stream loop: connect, run, reconnect on failure.
// It returns only when ctx is cancelled.
func (s *stream) run(ctx context.Context, sessionID []byte, cert *tls.Certificate) {
	for {
		if ctx.Err() != nil {
			return
		}

		if err := s.runOnce(ctx, sessionID, cert); err != nil && ctx.Err() == nil {
			// Default 1s reconnect, but back off hard when VK is rate-limiting
			// anonymous-token requests (error_code 29) — a flat 1s retry just
			// hammers the limit and prolongs it.
			backoff := time.Second
			if creds.IsRateLimitError(err) {
				backoff = 15 * time.Second
			}
			s.hub.cfg.Logger.Warnf("[stream %d] run error: %v; reconnecting in %v", s.id, err, backoff)
			s.ready.Store(false)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
		}
	}
}

// runOnce establishes one TURN allocation, optionally wraps it in DTLS,
// and pumps packets until something fails or ctx is cancelled.
func (s *stream) runOnce(ctx context.Context, sessionID []byte, cert *tls.Certificate) (err error) {
	cfg := s.hub.cfg
	logger := cfg.Logger
	logger.Debugf("[stream %d] fetching credentials", s.id)

	hint := s.hub.hintFor(s.id)
	cr, err := s.hub.creds.Get(ctx, hint, s.id, credsAdapter{cfg.Provider})
	if err != nil {
		return fmt.Errorf("creds: %w", err)
	}

	addr := cr.ServerAddr
	if cfg.TURNHostOverride != "" || cfg.TURNPortOverride != 0 {
		host, port, splitErr := net.SplitHostPort(addr)
		if splitErr == nil {
			if cfg.TURNHostOverride != "" {
				host = cfg.TURNHostOverride
			}
			if cfg.TURNPortOverride != 0 {
				port = fmt.Sprintf("%d", cfg.TURNPortOverride)
			}
			addr = net.JoinHostPort(host, port)
		}
	}

	logger.Infof("[stream %d] dialing TURN %s (udp=%v)", s.id, addr, cfg.UDP)

	dialer := &net.Dialer{Timeout: 30 * time.Second, Control: cfg.Protector}

	var carrier net.PacketConn
	if cfg.UDP {
		c, derr := dialer.DialContext(ctx, "udp", addr)
		if derr != nil {
			return fmt.Errorf("dial udp: %w", derr)
		}
		defer c.Close()
		carrier = &connectedUDPConn{c.(*net.UDPConn)}
	} else {
		c, derr := dialer.DialContext(ctx, "tcp", addr)
		if derr != nil {
			return fmt.Errorf("dial tcp: %w", derr)
		}
		defer c.Close()
		carrier = turn.NewSTUNConn(c)
	}

	turnAddr, rerr := net.ResolveUDPAddr("udp", addr)
	if rerr != nil {
		return fmt.Errorf("resolve turn addr: %w", rerr)
	}

	addrFamily := turn.RequestedAddressFamilyIPv4
	peerUDP, perr := net.ResolveUDPAddr("udp", cfg.PeerAddr)
	if perr != nil {
		return fmt.Errorf("resolve peer addr: %w", perr)
	}
	if peerUDP.IP.To4() == nil {
		addrFamily = turn.RequestedAddressFamilyIPv6
	}

	client, cerr := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr:         turnAddr.String(),
		TURNServerAddr:         turnAddr.String(),
		Conn:                   carrier,
		Username:               cr.Username,
		Password:               cr.Password,
		RequestedAddressFamily: addrFamily,
		LoggerFactory:          logging.NewDefaultLoggerFactory(),
	})
	if cerr != nil {
		return fmt.Errorf("turn client: %w", cerr)
	}
	defer client.Close()

	if lerr := client.Listen(); lerr != nil {
		err := fmt.Errorf("turn listen: %w", lerr)
		s.invalidateOnAuthError(err)
		return err
	}

	relay, aerr := client.Allocate()
	if aerr != nil {
		err := fmt.Errorf("turn allocate: %w", aerr)
		s.invalidateOnAuthError(err)
		return err
	}
	defer relay.Close()

	logger.Infof("[stream %d] allocation ok, relay-addr=%s", s.id, relay.LocalAddr())

	switch cfg.PeerType {
	case "wireguard":
		return s.pumpRaw(ctx, relay, peerUDP)
	case "proxy_v1":
		return s.pumpDTLS(ctx, relay, peerUDP, cert, sessionID, false)
	case "proxy_v2", "":
		return s.pumpDTLS(ctx, relay, peerUDP, cert, sessionID, true)
	default:
		return fmt.Errorf("unknown PeerType %q", cfg.PeerType)
	}
}

// pumpRaw forwards bytes between local listener and the TURN relay
// without any DTLS layer.
func (s *stream) pumpRaw(ctx context.Context, relay net.PacketConn, peerUDP *net.UDPAddr) error {
	pumpCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Clear readiness synchronously the moment the pump returns, so the
	// Hub scheduler stops selecting this stream for TX before run() loops
	// (closes the stale-ready window).
	defer s.ready.Store(false)

	context.AfterFunc(pumpCtx, func() {
		_ = relay.SetDeadline(time.Now())
	})

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		defer cancel()
		for {
			select {
			case <-pumpCtx.Done():
				return
			case b := <-s.in:
				if b == nil {
					return
				}
				n, err := relay.WriteTo(b, peerUDP)
				putBuf(b)
				if err != nil {
					s.errorsTx.Add(1)
					return
				}
				s.bytesTx.Add(uint64(n))
				s.packetsTx.Add(1)
			}
		}
	}()

	go func() {
		defer wg.Done()
		defer cancel()
		buf := make([]byte, PacketBufSize)
		for {
			n, from, err := relay.ReadFrom(buf)
			if err != nil {
				s.errorsRx.Add(1)
				return
			}
			if from.String() != peerUDP.String() {
				continue
			}
			addr := s.peer.Load()
			if addr == nil {
				continue
			}
			if _, werr := s.hub.localConn.WriteTo(buf[:n], *addr); werr != nil {
				s.errorsRx.Add(1)
				return
			}
			s.bytesRx.Add(uint64(n))
			s.packetsRx.Add(1)
		}
	}()

	s.ready.Store(true)
	s.hub.signalReady()

	wg.Wait()
	return pumpExitErr(ctx)
}

// pumpDTLS forwards bytes between local listener and TURN relay through
// a DTLS 1.2 tunnel. If sendHandshake is true (proxy_v2) the first 17
// post-handshake bytes are session-id (16) + stream-id (1) so the
// server can aggregate streams per client into a single backend UDP
// connection.
func (s *stream) pumpDTLS(
	ctx context.Context,
	relay net.PacketConn,
	peerUDP *net.UDPAddr,
	cert *tls.Certificate,
	sessionID []byte,
	sendHandshake bool,
) error {
	pumpCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Clear readiness synchronously the moment the pump returns, so the
	// Hub scheduler stops selecting this stream for TX before run() loops
	// (closes the stale-ready window).
	defer s.ready.Store(false)

	c1, c2 := connutil.AsyncPacketPipe()
	defer c1.Close()
	defer c2.Close()

	dtlsConn, err := dtls.Client(c1, peerUDP, framing.NewDTLSConfig(framing.RoleClient, *cert))
	if err != nil {
		return fmt.Errorf("dtls client: %w", err)
	}
	defer dtlsConn.Close()

	context.AfterFunc(pumpCtx, func() {
		_ = relay.SetDeadline(time.Now())
		_ = dtlsConn.SetDeadline(time.Now())
		_ = c2.SetDeadline(time.Now())
	})

	var wg sync.WaitGroup
	wg.Add(2)

	// Pipe c2 <-> relay (carrier for DTLS).
	go func() {
		defer wg.Done()
		defer cancel()
		buf := make([]byte, PacketBufSize)
		for {
			n, _, err := c2.ReadFrom(buf)
			if err != nil {
				return
			}
			if _, werr := relay.WriteTo(buf[:n], peerUDP); werr != nil {
				s.errorsTx.Add(1)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		defer cancel()
		buf := make([]byte, PacketBufSize)
		for {
			n, from, err := relay.ReadFrom(buf)
			if err != nil {
				s.errorsRx.Add(1)
				return
			}
			if from.String() != peerUDP.String() {
				continue
			}
			if _, werr := c2.WriteTo(buf[:n], peerUDP); werr != nil {
				s.errorsRx.Add(1)
				return
			}
		}
	}()

	_ = dtlsConn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := dtlsConn.HandshakeContext(pumpCtx); err != nil {
		return fmt.Errorf("dtls handshake: %w", err)
	}
	_ = dtlsConn.SetDeadline(time.Time{})

	if sendHandshake {
		_ = dtlsConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := framing.WriteHandshake(dtlsConn, sessionID, byte(s.id)); err != nil {
			return fmt.Errorf("session-id handshake: %w", err)
		}
		_ = dtlsConn.SetWriteDeadline(time.Time{})
	}

	s.ready.Store(true)
	s.hub.signalReady()

	wg.Add(2)

	var lastRx atomic.Int64
	lastRx.Store(time.Now().Unix())

	// Local-listener -> DTLS (TX).
	go func() {
		defer wg.Done()
		defer cancel()
		for {
			select {
			case <-pumpCtx.Done():
				return
			case b := <-s.in:
				if b == nil {
					return
				}
				if wd := s.hub.cfg.WatchdogTimeout; wd > 0 {
					if time.Since(time.Unix(lastRx.Load(), 0)) > wd {
						putBuf(b)
						s.dropsTx.Add(1)
						return
					}
				}
				n, err := dtlsConn.Write(b)
				putBuf(b)
				if err != nil {
					s.errorsTx.Add(1)
					return
				}
				s.bytesTx.Add(uint64(n))
				s.packetsTx.Add(1)
			}
		}
	}()

	// DTLS -> Local-listener (RX).
	go func() {
		defer wg.Done()
		defer cancel()
		buf := make([]byte, PacketBufSize)
		for {
			n, err := dtlsConn.Read(buf)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					s.errorsRx.Add(1)
				}
				return
			}
			lastRx.Store(time.Now().Unix())
			addr := s.peer.Load()
			if addr == nil {
				continue
			}
			if _, werr := s.hub.localConn.WriteTo(buf[:n], *addr); werr != nil {
				s.errorsRx.Add(1)
				return
			}
			s.bytesRx.Add(uint64(n))
			s.packetsRx.Add(1)
		}
	}()

	wg.Wait()
	return pumpExitErr(ctx)
}

// pumpExitErr classifies why a pump's goroutines all returned. A pump
// goroutine that hits a relay/DTLS I/O error cancels the shared pumpCtx via
// its deferred cancel(), tearing the others down — so if we reach here with
// the PARENT ctx still live, a failure (not a clean shutdown) ended the
// session. Returning a non-nil error makes run() reset readiness and
// reconnect with backoff instead of busy-looping on the dead session.
func pumpExitErr(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("stream pump terminated (relay/DTLS failure)")
}

// invalidateOnAuthError asks the credential cache to drop this stream's
// group credentials when a TURN failure looks like an auth rejection, so the
// next runOnce refetches instead of retrying with the same rejected creds for
// the whole cache TTL.
func (s *stream) invalidateOnAuthError(err error) {
	if creds.IsAuthError(err) {
		s.hub.creds.HandleAuthError(s.id)
	}
}

// connectedUDPConn lets a *net.UDPConn satisfy net.PacketConn semantics
// expected by pion/turn when the socket is already "connected" (i.e. the
// remote address is fixed).
type connectedUDPConn struct{ *net.UDPConn }

// WriteTo ignores the destination address (the connected UDP socket has
// a fixed remote) and just writes.
func (c *connectedUDPConn) WriteTo(p []byte, _ net.Addr) (int, error) { return c.Write(p) }
