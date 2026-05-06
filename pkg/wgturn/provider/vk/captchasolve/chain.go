// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package captchasolve

import (
	"context"
	"errors"
	"fmt"

	vkprov "github.com/slovn/wgturn-core/pkg/wgturn/provider/vk"
)

// Compile-time interface satisfaction check.
var _ vkprov.CaptchaSolver = (*ChainSolver)(nil)

// ChainSolver tries an ordered list of inner solvers and returns the
// first non-error solution. If every inner solver fails, the joined
// error is returned (errors.Join, so callers can errors.Is against any
// of the underlying causes).
//
// Use cases:
//
//   - Fast path then fallback: CDPSolver (cheap, ~1 s) → AI-vision
//     solver (paid, slow, only if VK ever escalates from checkbox to
//     real slider).
//
//   - Cost-tiered: in-house solver → 2captcha.com → manual stdin.
//
//   - Geographic redundancy: solve via the LAN headless Chrome → if
//     that's down, fall back to an embedded webview in the host app.
//
// Context cancellation short-circuits the chain — once ctx is done,
// no further solvers are tried, and ctx.Err() is returned directly.
type ChainSolver struct {
	// Solvers are tried in order. An empty list is a programmer
	// error and yields a non-nil error from Solve.
	Solvers []vkprov.CaptchaSolver

	// OnAttempt, if non-nil, is invoked before each inner solver is
	// asked. Useful for telemetry / progress UIs. The index is
	// 0-based.
	OnAttempt func(index int, solver vkprov.CaptchaSolver)

	// OnFailure, if non-nil, is invoked after each inner solver
	// returns a non-nil error. The chain still proceeds to the next
	// solver (or returns Joined errors if this was the last).
	OnFailure func(index int, solver vkprov.CaptchaSolver, err error)
}

// Solve implements vkprov.CaptchaSolver.
func (c *ChainSolver) Solve(ctx context.Context, ch vkprov.CaptchaChallenge) (vkprov.Solution, error) {
	if len(c.Solvers) == 0 {
		return vkprov.Solution{}, errors.New("captchasolve chain: no solvers configured")
	}
	var failures []error
	for i, s := range c.Solvers {
		// Honor context before each attempt; one slow solver shouldn't
		// keep the chain alive past the caller's deadline.
		if err := ctx.Err(); err != nil {
			if len(failures) > 0 {
				return vkprov.Solution{}, errors.Join(append(failures, err)...)
			}
			return vkprov.Solution{}, err
		}
		if c.OnAttempt != nil {
			c.OnAttempt(i, s)
		}
		sol, err := s.Solve(ctx, ch)
		if err == nil {
			return sol, nil
		}
		if c.OnFailure != nil {
			c.OnFailure(i, s, err)
		}
		failures = append(failures, fmt.Errorf("solver[%d]: %w", i, err))
	}
	return vkprov.Solution{}, errors.Join(failures...)
}
