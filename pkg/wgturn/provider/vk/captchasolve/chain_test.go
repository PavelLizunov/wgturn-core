// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package captchasolve_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	vkprov "github.com/slovn/wgturn-core/pkg/wgturn/provider/vk"
	"github.com/slovn/wgturn-core/pkg/wgturn/provider/vk/captchasolve"
)

// stubSolver returns the configured solution / error and counts calls.
type stubSolver struct {
	name  string
	sol   vkprov.Solution
	err   error
	calls atomic.Int32
	delay time.Duration // optional artificial delay
}

func (s *stubSolver) Solve(ctx context.Context, _ vkprov.CaptchaChallenge) (vkprov.Solution, error) {
	s.calls.Add(1)
	if s.delay > 0 {
		t := time.NewTimer(s.delay)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return vkprov.Solution{}, ctx.Err()
		case <-t.C:
		}
	}
	return s.sol, s.err
}

func TestChainSolver_FirstWins(t *testing.T) {
	t.Parallel()
	a := &stubSolver{name: "a", sol: vkprov.Solution{SuccessToken: "TOK_A"}}
	b := &stubSolver{name: "b", sol: vkprov.Solution{SuccessToken: "TOK_B"}}
	chain := &captchasolve.ChainSolver{Solvers: []vkprov.CaptchaSolver{a, b}}

	sol, err := chain.Solve(context.Background(), vkprov.CaptchaChallenge{SID: "x"})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if sol.SuccessToken != "TOK_A" {
		t.Errorf("token = %q, want TOK_A", sol.SuccessToken)
	}
	if a.calls.Load() != 1 {
		t.Errorf("a.calls = %d, want 1", a.calls.Load())
	}
	if b.calls.Load() != 0 {
		t.Errorf("b.calls = %d, want 0 (chain stops on first success)", b.calls.Load())
	}
}

func TestChainSolver_FallbackOnFailure(t *testing.T) {
	t.Parallel()
	errA := errors.New("a-broke")
	a := &stubSolver{name: "a", err: errA}
	b := &stubSolver{name: "b", sol: vkprov.Solution{SuccessToken: "TOK_B"}}
	chain := &captchasolve.ChainSolver{Solvers: []vkprov.CaptchaSolver{a, b}}

	sol, err := chain.Solve(context.Background(), vkprov.CaptchaChallenge{SID: "x"})
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if sol.SuccessToken != "TOK_B" {
		t.Errorf("token = %q, want TOK_B (b should win after a fails)", sol.SuccessToken)
	}
	if a.calls.Load() != 1 {
		t.Errorf("a.calls = %d, want 1", a.calls.Load())
	}
	if b.calls.Load() != 1 {
		t.Errorf("b.calls = %d, want 1", b.calls.Load())
	}
}

func TestChainSolver_AllFailJoinedError(t *testing.T) {
	t.Parallel()
	errA := errors.New("a-broke")
	errB := errors.New("b-broke")
	chain := &captchasolve.ChainSolver{Solvers: []vkprov.CaptchaSolver{
		&stubSolver{err: errA},
		&stubSolver{err: errB},
	}}
	_, err := chain.Solve(context.Background(), vkprov.CaptchaChallenge{SID: "x"})
	if err == nil {
		t.Fatal("want error")
	}
	// errors.Join lets us errors.Is against either underlying cause —
	// embedders that branch on, say, wgturn.ErrCaptchaRequired keep
	// working through the chain wrapper.
	if !errors.Is(err, errA) {
		t.Errorf("err does not wrap errA: %v", err)
	}
	if !errors.Is(err, errB) {
		t.Errorf("err does not wrap errB: %v", err)
	}
}

func TestChainSolver_EmptyChain(t *testing.T) {
	t.Parallel()
	chain := &captchasolve.ChainSolver{}
	_, err := chain.Solve(context.Background(), vkprov.CaptchaChallenge{})
	if err == nil || !strings.Contains(err.Error(), "no solvers") {
		t.Errorf("want 'no solvers' error, got %v", err)
	}
}

func TestChainSolver_ContextCancelledMidChain(t *testing.T) {
	t.Parallel()
	a := &stubSolver{err: errors.New("a-broke")}
	b := &stubSolver{
		// Slow so that the cancel below fires while it's running.
		delay: 2 * time.Second,
		sol:   vkprov.Solution{SuccessToken: "TOK_B"},
	}
	chain := &captchasolve.ChainSolver{Solvers: []vkprov.CaptchaSolver{a, b}}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := chain.Solve(ctx, vkprov.CaptchaChallenge{SID: "x"})
	dur := time.Since(start)
	if err == nil {
		t.Fatal("want error from cancelled chain")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err missing DeadlineExceeded: %v", err)
	}
	// A solid bound: the chain must abort within ~1 s of the 200 ms
	// deadline (slack for stub solver's select to wake up).
	if dur > 2500*time.Millisecond {
		t.Errorf("chain didn't honor deadline; dur=%s", dur)
	}
}

func TestChainSolver_HooksFire(t *testing.T) {
	t.Parallel()
	a := &stubSolver{err: errors.New("a-broke")}
	b := &stubSolver{sol: vkprov.Solution{SuccessToken: "TOK_B"}}
	var attempts, failures atomic.Int32
	chain := &captchasolve.ChainSolver{
		Solvers:   []vkprov.CaptchaSolver{a, b},
		OnAttempt: func(_ int, _ vkprov.CaptchaSolver) { attempts.Add(1) },
		OnFailure: func(_ int, _ vkprov.CaptchaSolver, _ error) { failures.Add(1) },
	}
	if _, err := chain.Solve(context.Background(), vkprov.CaptchaChallenge{}); err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
	if got := failures.Load(); got != 1 {
		t.Errorf("failures = %d, want 1 (only solver-a failed)", got)
	}
}
