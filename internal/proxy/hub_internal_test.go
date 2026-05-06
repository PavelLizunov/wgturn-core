// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package proxy

import "testing"

// TestHub_HintFor_RoundRobin pins the round-robin contract that
// stream.runOnce relies on to fan out across multiple call links.
//
// With StreamsPerCred=4, group 0 = streams 0..3, group 1 = streams
// 4..7, etc. With Hints = [a, b, c]:
//
//	stream 0..3   -> hint a (group 0)
//	stream 4..7   -> hint b (group 1)
//	stream 8..11  -> hint c (group 2)
//	stream 12..15 -> hint a (group 3 wraps back)
func TestHub_HintFor_RoundRobin(t *testing.T) {
	h := &Hub{cfg: HubConfig{
		StreamsPerCred: 4,
		Hints:          []string{"a", "b", "c"},
	}}
	cases := []struct {
		streamID int
		want     string
	}{
		{0, "a"}, {1, "a"}, {2, "a"}, {3, "a"},
		{4, "b"}, {5, "b"}, {7, "b"},
		{8, "c"}, {11, "c"},
		{12, "a"}, {15, "a"},
		{16, "b"}, {19, "b"},
	}
	for _, c := range cases {
		if got := h.hintFor(c.streamID); got != c.want {
			t.Errorf("hintFor(%d) = %q, want %q", c.streamID, got, c.want)
		}
	}
}

// TestHub_HintFor_SingleHint reproduces the legacy single-Hint
// behaviour: every stream sees the same value.
func TestHub_HintFor_SingleHint(t *testing.T) {
	h := &Hub{cfg: HubConfig{
		StreamsPerCred: 4,
		Hints:          []string{"only-link"},
	}}
	for i := 0; i < 32; i++ {
		if got := h.hintFor(i); got != "only-link" {
			t.Fatalf("stream %d: got %q, want only-link", i, got)
		}
	}
}

// TestHub_HintFor_EmptyHints — providers that ignore the hint
// (ModeStub) should see "" and not crash.
func TestHub_HintFor_EmptyHints(t *testing.T) {
	h := &Hub{cfg: HubConfig{
		StreamsPerCred: 4,
		Hints:          nil,
	}}
	for i := 0; i < 8; i++ {
		if got := h.hintFor(i); got != "" {
			t.Errorf("stream %d: got %q, want empty", i, got)
		}
	}
}

// TestHub_HintFor_StreamsPerCredOne — degenerate group size: every
// stream is its own cred-group, so each stream gets the next hint.
func TestHub_HintFor_StreamsPerCredOne(t *testing.T) {
	h := &Hub{cfg: HubConfig{
		StreamsPerCred: 1,
		Hints:          []string{"a", "b"},
	}}
	cases := []struct {
		streamID int
		want     string
	}{
		{0, "a"}, {1, "b"}, {2, "a"}, {3, "b"},
	}
	for _, c := range cases {
		if got := h.hintFor(c.streamID); got != c.want {
			t.Errorf("hintFor(%d) = %q, want %q", c.streamID, got, c.want)
		}
	}
}
