// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package proxy

import "sync"

// PacketBufSize is the max packet size the proxy is willing to forward.
// 2048 is enough for any IPv4 MTU and typical IPv6 paths; the WireGuard
// recommendation when wrapped is MTU 1280 anyway.
const PacketBufSize = 2048

// packetPool reuses byte slices of length PacketBufSize. Get returns a
// slice whose len == PacketBufSize; callers narrow it with [:n] before
// putting it back via Put.
var packetPool = sync.Pool{
	New: func() any {
		b := make([]byte, PacketBufSize)
		return &b
	},
}

// getBuf returns a fresh PacketBufSize-sized slice.
func getBuf() []byte {
	bp := packetPool.Get().(*[]byte)
	return (*bp)[:PacketBufSize]
}

// putBuf returns a slice to the pool. b's underlying array must have
// capacity == PacketBufSize (i.e. it must have come from getBuf).
func putBuf(b []byte) {
	if cap(b) != PacketBufSize {
		return
	}
	full := b[:PacketBufSize]
	packetPool.Put(&full)
}
