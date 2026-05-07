// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/PavelLizunov/wgturn-core/internal/creds"
)

// Hub is the central wgturn proxy. It listens for UDP packets on
// cfg.ListenAddr, fans them out across cfg.Streams parallel streams via
// round-robin, and merges the return path back to whoever last wrote to
// the listener. Each stream maintains its own TURN allocation; see
// stream.run.
type Hub struct {
	cfg HubConfig

	creds *creds.Cache

	mu        sync.Mutex
	started   bool
	stopped   bool
	cancel    context.CancelFunc
	streams   []*stream
	localConn net.PacketConn
	wg        sync.WaitGroup
	ready     chan struct{}
	readyOnce sync.Once
}

// NewHub validates cfg and constructs a Hub. Call Start to bring it up.
func NewHub(cfg HubConfig) (*Hub, error) {
	if cfg.Provider == nil {
		return nil, fmt.Errorf("proxy: HubConfig.Provider is required")
	}
	if cfg.Logger == nil {
		return nil, fmt.Errorf("proxy: HubConfig.Logger is required")
	}
	if cfg.Streams <= 0 {
		cfg.Streams = 1
	}
	if cfg.StreamsPerCred <= 0 {
		cfg.StreamsPerCred = 4
	}
	return &Hub{
		cfg:   cfg,
		creds: creds.NewCache(cfg.StreamsPerCred, cfg.Logger),
		ready: make(chan struct{}),
	}, nil
}

// hintFor picks the provider hint for a given stream id. Cred-groups
// are sized at cfg.StreamsPerCred, and groups round-robin through the
// configured Hints pool. With a single-element pool every group sees
// the same hint (legacy behaviour). Returns "" when Hints is empty —
// providers that ignore the hint (e.g. stub) are unaffected.
func (h *Hub) hintFor(streamID int) string {
	if len(h.cfg.Hints) == 0 {
		return ""
	}
	groupID := streamID / h.cfg.StreamsPerCred
	return h.cfg.Hints[groupID%len(h.cfg.Hints)]
}

// Start brings the Hub up. It returns once the local UDP listener is
// bound and the streams are spawned; readiness of any individual stream
// is signalled via Ready().
func (h *Hub) Start(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.started {
		return fmt.Errorf("proxy: hub already started")
	}
	h.started = true

	hubCtx, cancel := context.WithCancel(ctx)
	h.cancel = cancel

	lc := &net.ListenConfig{Control: h.cfg.Protector}
	conn, err := lc.ListenPacket(hubCtx, "udp", h.cfg.ListenAddr)
	if err != nil {
		cancel()
		return fmt.Errorf("listen %s: %w", h.cfg.ListenAddr, err)
	}
	h.localConn = conn

	context.AfterFunc(hubCtx, func() { _ = conn.Close() })

	sessionID, _ := uuid.New().MarshalBinary()
	h.cfg.Logger.Infof("[hub] session-id=%x streams=%d peer-type=%s", sessionID, h.cfg.Streams, h.cfg.PeerType)

	cert, err := generateCert()
	if err != nil {
		cancel()
		return fmt.Errorf("generate dtls cert: %w", err)
	}

	h.streams = make([]*stream, h.cfg.Streams)
	for i := 0; i < h.cfg.Streams; i++ {
		s := &stream{
			id:  i,
			hub: h,
			in:  make(chan []byte, 512),
		}
		h.streams[i] = s

		h.wg.Add(1)
		go func(s *stream, c tls.Certificate) {
			defer h.wg.Done()
			s.run(hubCtx, sessionID, &c)
		}(s, cert)

		// Tiny stagger so we don't hammer credential providers in lockstep.
		time.Sleep(50 * time.Millisecond)
	}

	// Listener -> RR scheduler.
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.scheduleLoop(hubCtx)
	}()

	return nil
}

// scheduleLoop reads from the local listener and round-robins the
// packets across ready streams. Drops packets when no stream is ready
// (so we don't blow up memory while every stream is still authenticating).
func (h *Hub) scheduleLoop(ctx context.Context) {
	var lastUsed int
	for {
		if ctx.Err() != nil {
			return
		}
		buf := getBuf()
		n, addr, err := h.localConn.ReadFrom(buf)
		if err != nil {
			putBuf(buf)
			return
		}

		// Find next ready stream starting at lastUsed+1 (round-robin).
		nStreams := len(h.streams)
		if nStreams == 0 {
			putBuf(buf)
			continue
		}
		lastUsed = (lastUsed + 1) % nStreams
		var s *stream
		for i := 0; i < nStreams; i++ {
			cand := h.streams[(lastUsed+i)%nStreams]
			if cand.ready.Load() {
				s = cand
				break
			}
		}
		if s == nil {
			putBuf(buf)
			continue
		}

		// Remember return address.
		retAddr := addr
		s.peer.Store(&retAddr)

		// Hand off; non-blocking so a wedged stream can't backpressure
		// healthy ones.
		select {
		case s.in <- buf[:n]:
			// queued
		default:
			putBuf(buf)
			s.dropsTx.Add(1)
		}
	}
}

// signalReady reports readiness exactly once, no matter how many streams
// transition to ready concurrently.
func (h *Hub) signalReady() {
	h.readyOnce.Do(func() { close(h.ready) })
}

// Ready returns a channel that is closed when at least one stream has
// reached its post-handshake ready state. Sufficient for callers to
// know that traffic injected on the local listener will reach the peer.
func (h *Hub) Ready() <-chan struct{} { return h.ready }

// Stop tears the Hub down and waits for every goroutine to exit.
func (h *Hub) Stop() error {
	h.mu.Lock()
	if h.stopped || !h.started {
		h.mu.Unlock()
		return nil
	}
	h.stopped = true
	cancel := h.cancel
	h.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	h.wg.Wait()
	return nil
}

// LocalAddr returns the actual address the Hub's local UDP listener is
// bound to. Useful when the configured ListenAddr used port 0 (let the
// kernel pick), which is the typical pattern in tests. Returns nil if
// called before Start.
func (h *Hub) LocalAddr() net.Addr {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.localConn == nil {
		return nil
	}
	return h.localConn.LocalAddr()
}

// Stats returns an aggregated snapshot of stream counters. Cheap enough
// to call from a polling UI.
func (h *Hub) Stats() HubStats {
	out := HubStats{StreamsTotal: len(h.streams)}
	for _, s := range h.streams {
		if s == nil {
			continue
		}
		if s.ready.Load() {
			out.StreamsRunning++
		}
		out.BytesTx += s.bytesTx.Load()
		out.BytesRx += s.bytesRx.Load()
		out.PacketsTx += s.packetsTx.Load()
		out.PacketsRx += s.packetsRx.Load()
		out.DropsTx += s.dropsTx.Load()
		out.ErrorsTx += s.errorsTx.Load()
		out.ErrorsRx += s.errorsRx.Load()
	}
	return out
}
