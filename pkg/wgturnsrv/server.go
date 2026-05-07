// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturnsrv

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/pion/dtls/v3"

	"github.com/PavelLizunov/wgturn-core/internal/framing"
	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
)

// Lifecycle errors returned by Server methods. Embedders can switch
// on these via errors.Is for setup or shutdown handling.
var (
	// ErrAlreadyStarted is returned by Start when invoked twice on
	// the same Server. Servers are single-use.
	ErrAlreadyStarted = errors.New("wgturnsrv: server already started")

	// ErrNotStarted is returned by Stats before Start has succeeded.
	ErrNotStarted = errors.New("wgturnsrv: server not started")
)

// Server terminates wgturn proxy_v2 sessions on a UDP listener and
// forwards their inner payload to a Backend. Construct with New, then
// call Start once and Stop once. A Server is single-use; build a new
// one to restart.
type Server struct {
	cfg     Config
	cert    tls.Certificate
	logger  wgturn.Logger
	backend Backend

	mu       sync.Mutex
	state    state
	listener net.Listener
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// state tracks Server lifecycle phase.
type state int

const (
	stateNew state = iota
	stateStarted
	stateStopped
)

// Stats is a snapshot of runtime counters. Empty in S2 — populated
// by S3 once the demuxer lands.
type Stats struct {
	// SessionsActive is the number of sessions currently demultiplexing
	// streams. A session is active from the moment a backend is opened
	// for it until its last stream closes.
	SessionsActive int

	// StreamsActive is the total number of DTLS streams currently
	// being served across all sessions.
	StreamsActive int
}

// New validates cfg and returns an unstarted Server. The DTLS
// self-signed certificate is generated up front so a misconfigured
// crypto subsystem fails fast instead of in the accept loop.
func New(cfg Config) (*Server, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()

	cert, err := framing.GenerateCertificate()
	if err != nil {
		return nil, fmt.Errorf("wgturnsrv: generate cert: %w", err)
	}

	return &Server{
		cfg:     cfg,
		cert:    cert,
		logger:  cfg.Logger,
		backend: cfg.Backend,
	}, nil
}

// Start opens the DTLS listener on cfg.ListenAddr and launches the
// accept loop in a background goroutine, then returns. Bind failures
// surface synchronously. The returned Server is single-use; Start
// twice is an error.
//
// ctx governs the lifetime of the accept loop and any per-session
// goroutines (added by S3): cancelling ctx is equivalent to calling
// Stop, modulo wait semantics.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stateNew {
		return ErrAlreadyStarted
	}

	udpAddr, err := net.ResolveUDPAddr("udp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("wgturnsrv: resolve %q: %w", s.cfg.ListenAddr, err)
	}

	dtlsCfg := framing.NewDTLSConfig(framing.RoleServer, s.cert)
	listener, err := dtls.Listen("udp", udpAddr, dtlsCfg)
	if err != nil {
		return fmt.Errorf("wgturnsrv: dtls listen %s: %w", s.cfg.ListenAddr, err)
	}

	loopCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.listener = listener
	s.state = stateStarted

	// Cancellation closes the listener so a blocked Accept unblocks.
	context.AfterFunc(loopCtx, func() { _ = listener.Close() })

	s.logger.Infof("wgturnsrv: listening on %s", listener.Addr())

	s.wg.Add(1)
	go s.acceptLoop(loopCtx)

	return nil
}

// acceptLoop drains the DTLS listener and hands each accepted conn to
// handleConn. In S2 handleConn just closes the conn; S3 will replace
// it with the demux that reads the 17-byte preamble and routes to the
// per-session goroutines.
func (s *Server) acceptLoop(ctx context.Context) {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			// Listener was closed — exit cleanly. Anything else is
			// surfaced at warn level so an operator can spot a
			// misconfigured network.
			if ctx.Err() != nil {
				return
			}
			s.logger.Warnf("wgturnsrv: accept error: %v", err)
			return
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			s.handleConn(ctx, c)
		}(conn)
	}
}

// handleConn is the per-connection entrypoint. In S2 it closes the
// connection immediately so the lifecycle can be exercised end-to-end
// without the demuxer being implemented yet. S3 replaces the body
// with the framing.ReadHandshake → session.addStream flow.
func (s *Server) handleConn(_ context.Context, conn net.Conn) {
	_ = conn.Close()
}

// Stop tears the Server down: cancels the accept context, closes the
// listener, and waits for every spawned goroutine to exit. Subsequent
// calls are no-ops.
func (s *Server) Stop() error {
	s.mu.Lock()
	if s.state != stateStarted {
		s.mu.Unlock()
		return nil
	}
	s.state = stateStopped
	cancel := s.cancel
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
	return nil
}

// LocalAddr returns the actual address the Server's UDP listener is
// bound to. Useful when ListenAddr was "host:0" (kernel-picked port).
// Returns nil before Start has succeeded.
func (s *Server) LocalAddr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Stats returns a Stats snapshot. ErrNotStarted is returned when
// called before Start. Counters are placeholders in S2 and gain
// meaning when S3 lands.
func (s *Server) Stats() (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stateStarted {
		return Stats{}, ErrNotStarted
	}
	return Stats{}, nil
}
