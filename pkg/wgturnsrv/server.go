// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturnsrv

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

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
	loopCtx  context.Context
	wg       sync.WaitGroup

	sessMu   sync.Mutex
	sessions map[string]*session
}

// state tracks Server lifecycle phase.
type state int

const (
	stateNew state = iota
	stateStarted
	stateStopped
)

// Stats is a snapshot of runtime counters useful for monitoring and
// tests. Counts are taken under the session-map lock and are
// internally consistent — for example StreamsActive >= SessionsActive
// always holds while traffic is flowing.
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
		cfg:      cfg,
		cert:     cert,
		logger:   cfg.Logger,
		backend:  cfg.Backend,
		sessions: make(map[string]*session),
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
	s.loopCtx = loopCtx
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

// handleConn is the per-connection entrypoint. It reads the 17-byte
// session+stream preamble (with HandshakeTimeout deadline), looks up
// or creates the corresponding session, registers the conn against
// the session's stream slot (evicting any prior occupant), and runs
// the per-stream reader loop until the conn closes.
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	closeConn := true
	defer func() {
		if closeConn {
			_ = conn.Close()
		}
	}()

	_ = conn.SetReadDeadline(time.Now().Add(s.cfg.HandshakeTimeout))
	sid, streamID, err := framing.ReadHandshake(conn)
	if err != nil {
		s.logger.Warnf("wgturnsrv: handshake from %s: %v", conn.RemoteAddr(), err)
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	sidHex := hex.EncodeToString(sid)

	sess, fresh, err := s.getOrCreateSession(ctx, sidHex)
	if err != nil {
		s.logger.Warnf("wgturnsrv: session %s open: %v", sidHex, err)
		return
	}

	displaced, accepted := sess.addStream(streamID, conn)
	if !accepted {
		// Session terminated between getOrCreateSession and addStream;
		// drop the conn.
		s.logger.Debugf("wgturnsrv: session %s already terminated; dropping stream %d", sidHex, streamID)
		return
	}
	if displaced != nil {
		// Eviction-on-conflict: an older stream with the same id is
		// being replaced. Closing it forces the loser goroutine's
		// Read to return so it exits cleanly. removeStream's "still
		// us" check keeps the loser from ripping out our slot.
		s.logger.Debugf("wgturnsrv: session %s evicted prior stream %d", sidHex, streamID)
		_ = displaced.Close()
	}
	if fresh {
		s.logger.Infof("wgturnsrv: session %s opened (stream %d)", sidHex, streamID)
	} else {
		s.logger.Debugf("wgturnsrv: session %s adding stream %d", sidHex, streamID)
	}

	// runStream owns the conn for the rest of its lifetime; defer the
	// close to here rather than the helper above.
	closeConn = false
	defer func() {
		_ = conn.Close()
		if drained := sess.removeStream(streamID, conn); drained {
			sess.terminate()
		}
	}()

	sess.runStream(streamID, conn)
}

// getOrCreateSession returns the session for sidHex, opening a backend
// connection on first use. The fresh return tells the caller whether
// it was the one to bring the session up — used for log levelling.
func (s *Server) getOrCreateSession(ctx context.Context, sidHex string) (sess *session, fresh bool, err error) {
	s.sessMu.Lock()
	if existing, ok := s.sessions[sidHex]; ok {
		s.sessMu.Unlock()
		return existing, false, nil
	}

	// Open the backend before the session goes into the map so a
	// failing dial doesn't leave a phantom session entry. Use the
	// server's loop context so the backend lives as long as the
	// server does, regardless of which client conn won the race to
	// create the session.
	parent := s.loopCtx
	if parent == nil {
		parent = ctx
	}
	backend, err := s.backend.Open(parent, sidHex)
	if err != nil {
		s.sessMu.Unlock()
		return nil, false, fmt.Errorf("backend open: %w", err)
	}

	onTerminate := func() { s.removeSession(sidHex) }
	sess = newSession(parent, sidHex, backend, s.logger, s.cfg, onTerminate)
	s.sessions[sidHex] = sess
	s.sessMu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		sess.runBackend()
	}()
	return sess, true, nil
}

// removeSession drops sidHex from the session map. Idempotent — the
// session's terminate() path may invoke this concurrently with a
// Stop call that's already walking the map.
func (s *Server) removeSession(sidHex string) {
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	delete(s.sessions, sidHex)
}

// Stop tears the Server down: cancels the accept context, closes the
// listener, terminates every active session (which closes its backend
// and all attached DTLS conns so per-stream reader goroutines unblock),
// and waits for every spawned goroutine to exit. Subsequent calls are
// no-ops.
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

	// Snapshot the session set under the map lock so terminate() can
	// take that lock itself (via removeSession) without recursing.
	s.sessMu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.sessMu.Unlock()
	for _, sess := range sessions {
		sess.terminate()
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
// called before Start.
func (s *Server) Stats() (Stats, error) {
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state != stateStarted {
		return Stats{}, ErrNotStarted
	}

	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	st := Stats{SessionsActive: len(s.sessions)}
	for _, sess := range s.sessions {
		st.StreamsActive += sess.streamCount()
	}
	return st, nil
}
