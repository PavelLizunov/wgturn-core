// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// Package creds is a per-stream-group credentials cache for the wgturn
// TURN proxy. It memoizes the (username, password, server) tuple for a
// configurable lifetime and invalidates entries that the upstream rejects
// with an auth error.
//
// The cache is derived from kiper292/wireguard-turn-android's
// libwg-go/credentials.go, refactored to avoid package-level state so
// multiple Tunnels can coexist in the same process with independent
// credential lifetimes.
package creds

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Defaults match kiper292's libwg-go behaviour.
const (
	DefaultLifetime     = 10 * time.Minute
	DefaultSafetyMargin = 60 * time.Second
	MaxCacheErrors      = 3
	ErrorWindow         = 10 * time.Second
)

// Credentials mirrors the proxy.Credentials shape (avoiding an upward
// dependency).
type Credentials struct {
	Username   string
	Password   string
	ServerAddr string
	ExpiresIn  time.Duration
}

// FetchFunc is what the cache calls on a miss.
type FetchFunc func(ctx context.Context, hint string, streamID int) (Credentials, error)

// Provider is the contract callers can satisfy directly. The cache
// adapts either form via Get.
type Provider interface {
	Fetch(ctx context.Context, hint string, streamID int) (Credentials, error)
}

// Logger is the minimal logging surface used by the cache. The proxy
// package's Logger satisfies it; tests can pass a no-op.
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// noopLogger is a private fallback for tests that don't care about logs.
type noopLogger struct{}

func (noopLogger) Debugf(string, ...any) {}
func (noopLogger) Infof(string, ...any)  {}
func (noopLogger) Warnf(string, ...any)  {}
func (noopLogger) Errorf(string, ...any) {}

// entry is a single cached credentials block plus its auth-error tracking.
type entry struct {
	mu sync.Mutex

	creds   Credentials
	link    string
	expires time.Time

	errorCount    atomic.Int32
	lastErrorTime atomic.Int64
}

// Cache groups streams into "credential groups" of streamsPerCred each
// (default 4). Streams within the same group share an entry; auth
// errors and TTLs are tracked per-group.
type Cache struct {
	streamsPerCred int
	logger         Logger

	// fetchMu serialises concurrent Fetch calls on miss. The kiper292
	// implementation observes that VK API rate-limits aggressively when
	// concurrent fetches happen; one-at-a-time is the simplest mitigation.
	fetchMu sync.Mutex

	mu      sync.RWMutex
	entries map[int]*entry
}

// NewCache returns a Cache. streamsPerCred is the group size; pass <= 0
// for the default of 4. logger may be nil.
func NewCache(streamsPerCred int, logger Logger) *Cache {
	if streamsPerCred <= 0 {
		streamsPerCred = 4
	}
	if logger == nil {
		logger = noopLogger{}
	}
	return &Cache{
		streamsPerCred: streamsPerCred,
		logger:         logger,
		entries:        make(map[int]*entry),
	}
}

// groupID returns the group index a stream belongs to.
func (c *Cache) groupID(streamID int) int { return streamID / c.streamsPerCred }

// getEntry returns or lazily creates the entry for streamID.
func (c *Cache) getEntry(streamID int) *entry {
	gid := c.groupID(streamID)

	c.mu.RLock()
	if e, ok := c.entries[gid]; ok {
		c.mu.RUnlock()
		return e
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.entries[gid]; ok {
		return e
	}
	e := &entry{}
	c.entries[gid] = e
	return e
}

// Get returns cached credentials, or invokes p to fetch and caches the
// result. Concurrent Get calls on a miss are serialised across the
// whole Cache (not just per-group) to avoid stampeding the upstream
// API.
func (c *Cache) Get(ctx context.Context, hint string, streamID int, p Provider) (Credentials, error) {
	if p == nil {
		return Credentials{}, errors.New("creds: provider is nil")
	}

	e := c.getEntry(streamID)
	gid := c.groupID(streamID)

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.link == hint && time.Now().Before(e.expires) {
		c.logger.Debugf("[creds] cache hit (group=%d, expires in %v)", gid, time.Until(e.expires))
		return e.creds, nil
	}

	c.logger.Debugf("[creds] cache miss (group=%d), fetching", gid)

	if err := ctx.Err(); err != nil {
		return Credentials{}, err
	}

	c.fetchMu.Lock()
	creds, err := p.Fetch(ctx, hint, streamID)
	c.fetchMu.Unlock()

	if err != nil {
		c.logger.Warnf("[creds] fetch error (group=%d): %v", gid, err)
		return Credentials{}, err
	}

	lifetime := creds.ExpiresIn
	if lifetime <= 0 {
		lifetime = DefaultLifetime
	}
	margin := DefaultSafetyMargin
	if margin > lifetime/2 {
		margin = lifetime / 2
	}

	e.creds = creds
	e.link = hint
	e.expires = time.Now().Add(lifetime - margin)

	c.logger.Infof("[creds] fetched (group=%d, expires=%v)", gid, e.expires)
	return creds, nil
}

// HandleAuthError counts an auth error against streamID's group. After
// MaxCacheErrors errors within ErrorWindow the entry is invalidated;
// the next Get for that group will refetch. Returns true if the entry
// was invalidated by this call.
func (c *Cache) HandleAuthError(streamID int) bool {
	e := c.getEntry(streamID)
	gid := c.groupID(streamID)
	now := time.Now().Unix()

	if now-e.lastErrorTime.Load() > int64(ErrorWindow.Seconds()) {
		e.errorCount.Store(0)
	}

	count := e.errorCount.Add(1)
	e.lastErrorTime.Store(now)
	c.logger.Warnf("[creds] auth error (group=%d, count=%d/%d)", gid, count, MaxCacheErrors)

	if count >= MaxCacheErrors {
		e.mu.Lock()
		e.creds = Credentials{}
		e.link = ""
		e.expires = time.Time{}
		e.mu.Unlock()
		e.errorCount.Store(0)
		e.lastErrorTime.Store(0)
		c.logger.Warnf("[creds] auth-error threshold reached; invalidated group=%d", gid)
		return true
	}
	return false
}

// InvalidateAll drops every cached entry. Useful on network change.
func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	c.entries = make(map[int]*entry)
	c.mu.Unlock()
	c.logger.Infof("[creds] all entries invalidated")
}

// IsAuthError is a string-match heuristic for "this is a TURN/STUN
// authentication failure". The pion/turn library wraps several distinct
// error shapes here, so we keep the same heuristic kiper292 uses.
func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, needle := range []string{
		"401",
		"Unauthorized",
		"authentication",
		"invalid credential",
		"stale nonce",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// IsRateLimitError reports whether err is a VK anonymous-token rate-limit
// (error_code 29). String-matched (same approach as IsAuthError) so the proxy
// package can pick a longer reconnect backoff without importing pkg/wgturn
// (which would be a circular import). Matching both the code and VK's message
// keeps it working if either side of the envelope changes.
func IsRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "code=29") || strings.Contains(s, "Rate limit")
}
