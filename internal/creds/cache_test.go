// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package creds

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProvider counts invocations and returns a configurable result.
type fakeProvider struct {
	mu      sync.Mutex
	calls   atomic.Int64
	creds   Credentials
	err     error
	delay   time.Duration
	hintLog []string
}

func (p *fakeProvider) Fetch(ctx context.Context, hint string, _ int) (Credentials, error) {
	p.calls.Add(1)
	p.mu.Lock()
	p.hintLog = append(p.hintLog, hint)
	d := p.delay
	c := p.creds
	e := p.err
	p.mu.Unlock()
	if d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return Credentials{}, ctx.Err()
		}
	}
	return c, e
}

func TestCache_GroupID(t *testing.T) {
	c := NewCache(4, nil)
	cases := map[int]int{
		0: 0, 1: 0, 2: 0, 3: 0,
		4: 1, 5: 1, 6: 1, 7: 1,
		8: 2, 11: 2, 12: 3,
	}
	for sid, want := range cases {
		if got := c.groupID(sid); got != want {
			t.Errorf("groupID(%d): got %d want %d", sid, got, want)
		}
	}

	c2 := NewCache(1, nil)
	if c2.groupID(0) != 0 || c2.groupID(7) != 7 {
		t.Errorf("streamsPerCred=1: groupID broken")
	}
}

func TestCache_SharedAcrossGroup_FetchOnce(t *testing.T) {
	p := &fakeProvider{creds: Credentials{Username: "u", Password: "p", ServerAddr: "s:3478"}}
	c := NewCache(4, nil)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		got, err := c.Get(ctx, "link-A", i, p)
		if err != nil {
			t.Fatalf("Get(%d): %v", i, err)
		}
		if got.Username != "u" {
			t.Errorf("Get(%d) returned %+v", i, got)
		}
	}
	if n := p.calls.Load(); n != 1 {
		t.Errorf("expected 1 fetch for 4 streams in group, got %d", n)
	}
}

func TestCache_DifferentGroups_FetchPerGroup(t *testing.T) {
	p := &fakeProvider{creds: Credentials{Username: "u"}}
	c := NewCache(4, nil)
	ctx := context.Background()

	// streams 0..7 -> 2 groups
	for i := 0; i < 8; i++ {
		if _, err := c.Get(ctx, "link", i, p); err != nil {
			t.Fatalf("Get(%d): %v", i, err)
		}
	}
	if n := p.calls.Load(); n != 2 {
		t.Errorf("expected 2 fetches for 2 groups, got %d", n)
	}
}

func TestCache_ExpiryRefetch(t *testing.T) {
	p := &fakeProvider{creds: Credentials{Username: "u", ExpiresIn: 100 * time.Millisecond}}
	c := NewCache(4, nil)
	ctx := context.Background()

	if _, err := c.Get(ctx, "link", 0, p); err != nil {
		t.Fatal(err)
	}
	if n := p.calls.Load(); n != 1 {
		t.Fatalf("first call: n=%d", n)
	}

	// ExpiresIn=100ms, safety margin clamps to half = 50ms, so cache valid ~50ms.
	time.Sleep(120 * time.Millisecond)

	if _, err := c.Get(ctx, "link", 0, p); err != nil {
		t.Fatal(err)
	}
	if n := p.calls.Load(); n != 2 {
		t.Errorf("expected refetch after expiry, n=%d", n)
	}
}

func TestCache_DifferentLinkRefetch(t *testing.T) {
	p := &fakeProvider{creds: Credentials{Username: "u"}}
	c := NewCache(4, nil)
	ctx := context.Background()

	if _, err := c.Get(ctx, "linkA", 0, p); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "linkB", 0, p); err != nil {
		t.Fatal(err)
	}
	if n := p.calls.Load(); n != 2 {
		t.Errorf("expected refetch on link change, n=%d", n)
	}
}

func TestCache_PropagatesProviderError(t *testing.T) {
	want := errors.New("boom")
	p := &fakeProvider{err: want}
	c := NewCache(4, nil)

	_, err := c.Get(context.Background(), "link", 0, p)
	if !errors.Is(err, want) {
		t.Errorf("Get returned %v, want %v", err, want)
	}
}

func TestCache_RespectsContext(t *testing.T) {
	p := &fakeProvider{delay: 200 * time.Millisecond, creds: Credentials{Username: "u"}}
	c := NewCache(4, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := c.Get(ctx, "link", 0, p)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Get returned %v, want DeadlineExceeded", err)
	}
}

func TestCache_HandleAuthError_InvalidatesAfterThreshold(t *testing.T) {
	p := &fakeProvider{creds: Credentials{Username: "u"}}
	c := NewCache(4, nil)

	if _, err := c.Get(context.Background(), "link", 0, p); err != nil {
		t.Fatal(err)
	}

	// First MaxCacheErrors-1 errors don't invalidate.
	for i := 0; i < MaxCacheErrors-1; i++ {
		if c.HandleAuthError(0) {
			t.Errorf("HandleAuthError(%d): unexpected invalidation", i)
		}
	}
	// Threshold-th error does.
	if !c.HandleAuthError(0) {
		t.Errorf("HandleAuthError(%d): expected invalidation", MaxCacheErrors-1)
	}

	// And next Get refetches.
	if _, err := c.Get(context.Background(), "link", 0, p); err != nil {
		t.Fatal(err)
	}
	if n := p.calls.Load(); n != 2 {
		t.Errorf("expected refetch after invalidation, n=%d", n)
	}
}

func TestCache_InvalidateAll(t *testing.T) {
	p := &fakeProvider{creds: Credentials{Username: "u"}}
	c := NewCache(4, nil)

	for i := 0; i < 8; i++ {
		_, _ = c.Get(context.Background(), "link", i, p)
	}
	pre := p.calls.Load()
	c.InvalidateAll()
	for i := 0; i < 8; i++ {
		_, _ = c.Get(context.Background(), "link", i, p)
	}
	if got := p.calls.Load() - pre; got != 2 {
		t.Errorf("after InvalidateAll, expected 2 refetches (2 groups), got %d", got)
	}
}

func TestCache_ConcurrentSameGroup_SerialisesFetch(t *testing.T) {
	// With concurrent Gets on the same group, only one fetch should
	// happen because one goroutine wins the entry mutex and populates
	// the cache before the others enter Get.
	p := &fakeProvider{
		creds: Credentials{Username: "u"},
		delay: 30 * time.Millisecond,
	}
	c := NewCache(4, nil)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(streamID int) {
			defer wg.Done()
			if _, err := c.Get(context.Background(), "link", streamID, p); err != nil {
				t.Errorf("Get: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if n := p.calls.Load(); n != 1 {
		t.Errorf("expected 1 fetch under contention, got %d", n)
	}
}

func TestIsAuthError(t *testing.T) {
	cases := map[string]bool{
		"":                             false,
		"some random error":            false,
		"401 Unauthorized from server": true,
		"authentication failed":        true,
		"invalid credential supplied":  true,
		"stale nonce — please retry":   true,
		"network unreachable":          false,
	}
	for s, want := range cases {
		var err error
		if s != "" {
			err = errors.New(s)
		}
		if got := IsAuthError(err); got != want {
			t.Errorf("IsAuthError(%q): got %v want %v", s, got, want)
		}
	}
}

func TestCache_NilProvider(t *testing.T) {
	c := NewCache(4, nil)
	_, err := c.Get(context.Background(), "link", 0, nil)
	if err == nil {
		t.Fatal("expected error for nil provider")
	}
}
