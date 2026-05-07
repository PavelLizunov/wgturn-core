// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturnsrv_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"

	"github.com/PavelLizunov/wgturn-core/pkg/wgturnsrv"
)

// TestWGKernelBackend_RoundTrip exercises the conn.Bind <-> net.Conn
// bridge end-to-end without spinning up an actual wgkernel: we play
// the role of the kernel ourselves by calling Open / Send and reading
// from the ReceiveFunc.
func TestWGKernelBackend_RoundTrip(t *testing.T) {
	be := wgturnsrv.NewWGKernelBackend()
	bind := be.Bind()

	// "Kernel" side: Open the bind to get the ReceiveFunc the wgturn
	// device would use.
	fns, port, err := bind.Open(0)
	if err != nil {
		t.Fatalf("bind.Open: %v", err)
	}
	if port == 0 {
		t.Errorf("bind.Open returned port 0")
	}
	if len(fns) != 1 {
		t.Fatalf("bind.Open returned %d ReceiveFuncs, want 1", len(fns))
	}
	recv := fns[0]

	// "Proxy" side: Open the backend conn.
	bConn, err := be.Open(context.Background(), "test-session")
	if err != nil {
		t.Fatalf("backend Open: %v", err)
	}
	t.Cleanup(func() { _ = bConn.Close() })

	// Proxy → Kernel: write a packet, kernel pulls it via ReceiveFunc.
	want := []byte("packet-from-proxy")
	if _, err := bConn.Write(want); err != nil {
		t.Fatalf("backend Write: %v", err)
	}

	pkts := [][]byte{make([]byte, 1500)}
	sizes := make([]int, 1)
	eps := make([]conn.Endpoint, 1)
	n, err := recv(pkts, sizes, eps)
	if err != nil {
		t.Fatalf("ReceiveFunc: %v", err)
	}
	if n != 1 {
		t.Errorf("ReceiveFunc returned n=%d, want 1", n)
	}
	if !bytes.Equal(pkts[0][:sizes[0]], want) {
		t.Errorf("kernel got %q, want %q", pkts[0][:sizes[0]], want)
	}
	if eps[0] == nil {
		t.Error("ReceiveFunc returned nil Endpoint")
	}

	// Kernel → Proxy: kernel calls Send, backend Read returns the bytes.
	out := []byte("packet-from-kernel")
	if err := bind.Send([][]byte{out}, eps[0]); err != nil {
		t.Fatalf("bind.Send: %v", err)
	}
	rd := make([]byte, 1500)
	_ = bConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	rn, err := bConn.Read(rd)
	if err != nil {
		t.Fatalf("backend Read: %v", err)
	}
	if !bytes.Equal(rd[:rn], out) {
		t.Errorf("backend got %q, want %q", rd[:rn], out)
	}
}

// TestWGKernelBackend_SingleSession enforces that a second Open errors:
// the bind has only one input channel, so two backend conns can't share
// it without a multi-peer rewrite.
func TestWGKernelBackend_SingleSession(t *testing.T) {
	be := wgturnsrv.NewWGKernelBackend()
	c1, err := be.Open(context.Background(), "a")
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	t.Cleanup(func() { _ = c1.Close() })

	if _, err := be.Open(context.Background(), "b"); err == nil {
		t.Error("second Open returned nil error, want busy error")
	}

	// After closing the first conn, Open should succeed again.
	_ = c1.Close()
	c2, err := be.Open(context.Background(), "b")
	if err != nil {
		t.Errorf("Open after close: %v", err)
	} else {
		_ = c2.Close()
	}
}

// TestWGKernelBackend_BindCloseUnblocksReceive checks that closing the
// bind makes a blocked ReceiveFunc return net.ErrClosed promptly. This
// matches conn.Bind's documented contract.
func TestWGKernelBackend_BindCloseUnblocksReceive(t *testing.T) {
	be := wgturnsrv.NewWGKernelBackend()
	bind := be.Bind()
	fns, _, err := bind.Open(0)
	if err != nil {
		t.Fatalf("bind.Open: %v", err)
	}
	recv := fns[0]

	done := make(chan error, 1)
	go func() {
		pkts := [][]byte{make([]byte, 1500)}
		sizes := make([]int, 1)
		eps := make([]conn.Endpoint, 1)
		_, err := recv(pkts, sizes, eps)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	_ = bind.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("ReceiveFunc returned %v, want net.ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReceiveFunc did not unblock within 2s")
	}
}

// TestWGKernelBackend_ReadDeadline: a Read with no traffic times out
// when the deadline fires, returning a net.Error with Timeout()==true
// so callers can react via errors.As.
func TestWGKernelBackend_ReadDeadline(t *testing.T) {
	be := wgturnsrv.NewWGKernelBackend()
	c, err := be.Open(context.Background(), "deadline")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	_ = c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 16)
	start := time.Now()
	_, err = c.Read(buf)
	if err == nil {
		t.Fatal("Read returned no error; expected timeout")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Read blocked %v, want ≤ 1s", elapsed)
	}
	type timeoutErr interface{ Timeout() bool }
	te, ok := err.(timeoutErr)
	if !ok || !te.Timeout() {
		t.Errorf("err = %v, want a Timeout()==true net.Error", err)
	}
}

// TestWGKernelBackend_Concurrent writes packets through the backend and
// reads them on the kernel side concurrently to surface any race in
// the channel handling under -race.
func TestWGKernelBackend_Concurrent(t *testing.T) {
	be := wgturnsrv.NewWGKernelBackend()
	bind := be.Bind()
	fns, _, err := bind.Open(0)
	if err != nil {
		t.Fatalf("bind.Open: %v", err)
	}
	recv := fns[0]

	bConn, err := be.Open(context.Background(), "x")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = bConn.Close() })

	const N = 64
	var wg sync.WaitGroup
	wg.Add(2)

	// Writer goroutine: feed N packets into the backend.
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			if _, err := bConn.Write([]byte{byte(i)}); err != nil {
				t.Errorf("Write %d: %v", i, err)
				return
			}
		}
	}()

	// Reader goroutine: drain N packets via the ReceiveFunc.
	got := 0
	go func() {
		defer wg.Done()
		pkts := [][]byte{make([]byte, 1500)}
		sizes := make([]int, 1)
		eps := make([]conn.Endpoint, 1)
		deadline := time.Now().Add(3 * time.Second)
		for got < N && time.Now().Before(deadline) {
			n, err := recv(pkts, sizes, eps)
			if err != nil {
				return
			}
			got += n
		}
	}()

	wg.Wait()
	if got != N {
		t.Errorf("kernel received %d packets, want %d", got, N)
	}
}
