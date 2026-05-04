// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturn

// Stats is a snapshot of Tunnel runtime counters. All numbers are
// monotonically non-decreasing for the lifetime of one Tunnel; resetting
// them requires a fresh Tunnel. Stats is safe to copy and inspect from
// any goroutine.
type Stats struct {
	// StreamsRunning is the number of streams currently in the
	// "ready" state (DTLS handshake done, allocation acknowledged).
	StreamsRunning int

	// StreamsTotal is the configured number of streams.
	StreamsTotal int

	// BytesTx is the total bytes written from the local listener
	// towards the remote peer (after wrapping).
	BytesTx uint64

	// BytesRx is the total bytes read from the remote peer towards the
	// local listener (after unwrapping).
	BytesRx uint64

	// PacketsTx is the count of TX packets across all streams.
	PacketsTx uint64

	// PacketsRx is the count of RX packets across all streams.
	PacketsRx uint64

	// DropsTx is the count of TX-side drops (queue full or channel
	// closed). Symptomatic of credentials still being fetched, a stuck
	// stream, or sustained backpressure.
	DropsTx uint64

	// ErrorsTx and ErrorsRx are stream-level error counts, mostly useful
	// for diagnosing flaky upstream networks.
	ErrorsTx uint64
	ErrorsRx uint64
}
