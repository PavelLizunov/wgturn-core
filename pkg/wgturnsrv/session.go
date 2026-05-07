// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturnsrv

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
)

// readBufSize is the receive buffer size for both the backend and the
// per-stream reader goroutines. 1600 fits any IPv4-MTU payload plus
// the slack the legacy server allows; larger frames are silently
// truncated and logged.
const readBufSize = 1600

// session is one client's view of the server: a single backend conn
// plus the set of DTLS streams currently demultiplexed onto it. The
// (sessionID, streamID) pair from the 17-byte preamble determines
// which slot a stream lands in; duplicates evict the prior entry.
type session struct {
	id      string
	backend net.Conn
	logger  wgturn.Logger
	cfg     Config

	mu      sync.RWMutex
	streams []streamEntry

	// rrCounter is incremented atomically before each backend → stream
	// dispatch; modulo len(streams) it produces the round-robin index.
	rrCounter atomic.Uint64

	ctx        context.Context
	cancel     context.CancelFunc
	terminated atomic.Bool

	// onTerminate is invoked exactly once when the session is torn
	// down. Server uses it to drop the session from its lookup map.
	onTerminate func()
}

// streamEntry pairs a stream identifier with the DTLS connection
// carrying it. Held in a small slice rather than a map: N <= 32 means
// linear scan is faster than hashing, and the slice form makes the
// round-robin index trivial.
type streamEntry struct {
	id   byte
	conn net.Conn
}

// newSession constructs a session ready to accept streams. The caller
// must launch runBackend separately; that split keeps the lifecycle
// observable from the Server struct without doubling up on goroutines.
func newSession(parentCtx context.Context, id string, backend net.Conn, logger wgturn.Logger, cfg Config, onTerminate func()) *session {
	ctx, cancel := context.WithCancel(parentCtx)
	return &session{
		id:          id,
		backend:     backend,
		logger:      logger,
		cfg:         cfg,
		ctx:         ctx,
		cancel:      cancel,
		onTerminate: onTerminate,
	}
}

// addStream registers conn under streamID, displacing any previous
// occupant of that slot. The displaced conn is returned so the caller
// (typically handleConn) can close it without holding the session
// lock; callers that don't care about the prior conn may discard it.
//
// Returns false if the session is already terminated, in which case
// the caller should close conn and return — the session is past
// accepting new traffic.
func (s *session) addStream(streamID byte, conn net.Conn) (displaced net.Conn, ok bool) {
	if s.terminated.Load() {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.streams {
		if e.id == streamID {
			displaced = e.conn
			s.streams[i].conn = conn
			return displaced, true
		}
	}
	s.streams = append(s.streams, streamEntry{id: streamID, conn: conn})
	return nil, true
}

// removeStream evicts (streamID, conn) from the slice if and only if
// the slot still holds that exact conn. The "still us" check makes
// removal safe to call from the streamReader goroutine after an
// eviction-by-displacement happened — we don't want the loser
// goroutine ripping out the winner's slot. Returns true when the
// slice transitioned from non-empty to empty.
func (s *session) removeStream(streamID byte, conn net.Conn) (drained bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.streams {
		if e.id != streamID || e.conn != conn {
			continue
		}
		// Order-preserving delete keeps the round-robin distribution
		// stable from the perspective of an in-flight backendReader.
		s.streams = append(s.streams[:i], s.streams[i+1:]...)
		break
	}
	return len(s.streams) == 0
}

// snapshotStreams copies the current slice under the read lock so the
// backendReader can iterate without holding it while doing I/O. The
// slice is small (<=32) and changes infrequently; copying is cheaper
// than blocking adds/removes for the duration of a Write.
func (s *session) snapshotStreams() []streamEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.streams) == 0 {
		return nil
	}
	out := make([]streamEntry, len(s.streams))
	copy(out, s.streams)
	return out
}

// streamCount is a cheap read-locked length.
func (s *session) streamCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.streams)
}

// terminate is idempotent: the first call closes the backend conn,
// cancels the session ctx (unblocking every per-stream Read), and
// invokes onTerminate. Subsequent calls return without side effects.
func (s *session) terminate() {
	if !s.terminated.CompareAndSwap(false, true) {
		return
	}
	s.cancel()
	_ = s.backend.Close()
	if s.onTerminate != nil {
		s.onTerminate()
	}
	// Also close any streams we still know about so the per-stream
	// reader goroutines unblock and exit on the next iteration.
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.streams {
		_ = e.conn.Close()
	}
	s.streams = nil
}

// runBackend reads packets from the backend and round-robins them to
// the active streams. The Read deadline is refreshed before each
// Read — exceeding it means the TURN allocations behind every stream
// have lapsed and the session should be torn down.
func (s *session) runBackend() {
	defer s.terminate()
	buf := make([]byte, readBufSize)
	for {
		_ = s.backend.SetReadDeadline(time.Now().Add(s.cfg.StreamReadTimeout))
		n, err := s.backend.Read(buf)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) && s.ctx.Err() == nil {
				s.logger.Debugf("wgturnsrv: session %s backend read: %v", s.id, err)
			}
			return
		}
		streams := s.snapshotStreams()
		if len(streams) == 0 {
			// Backend pushed data but no stream is currently mapped:
			// drop. This matches the legacy server, where a packet
			// that arrives on the WG side before any client-side
			// stream is registered has nowhere to go.
			continue
		}
		idx := int(s.rrCounter.Add(1)-1) % len(streams)
		target := streams[idx].conn
		_ = target.SetWriteDeadline(time.Now().Add(s.cfg.BackendWriteTimeout))
		if _, werr := target.Write(buf[:n]); werr != nil {
			s.logger.Debugf("wgturnsrv: session %s stream %d write: %v", s.id, streams[idx].id, werr)
			// Don't tear the session down on a per-stream write
			// error: the streamReader for that conn will notice and
			// evict itself. We just drop this packet.
		}
	}
}

// runStream is the per-DTLS-conn reader: it copies bytes from the
// stream conn into the backend conn until either side errors. It
// returns when the stream is dead; callers (handleConn) are expected
// to close the conn and unregister the stream after this returns.
func (s *session) runStream(streamID byte, conn net.Conn) {
	buf := make([]byte, readBufSize)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(s.cfg.StreamReadTimeout))
		n, err := conn.Read(buf)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) && s.ctx.Err() == nil {
				s.logger.Debugf("wgturnsrv: session %s stream %d read: %v", s.id, streamID, err)
			}
			return
		}
		_ = s.backend.SetWriteDeadline(time.Now().Add(s.cfg.BackendWriteTimeout))
		if _, werr := s.backend.Write(buf[:n]); werr != nil {
			if !errors.Is(werr, net.ErrClosed) && s.ctx.Err() == nil {
				s.logger.Debugf("wgturnsrv: session %s backend write: %v", s.id, werr)
			}
			// Backend write error means the WG-side socket is gone —
			// tear the whole session down rather than evicting just
			// this stream and leaving the others writing to a dead
			// fd.
			s.terminate()
			return
		}
	}
}
