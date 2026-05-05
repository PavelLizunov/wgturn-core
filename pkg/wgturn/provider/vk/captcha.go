// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package vk

import (
	"context"
	"fmt"

	"github.com/slovn/wgturn-core/pkg/wgturn"
)

// CaptchaChallenge is the structured form of VK's captcha-needed
// response. It carries everything a solver needs to display the
// challenge to a user and POST a solution back via Solver.Solve.
type CaptchaChallenge struct {
	// SID is the VK captcha session id. Pass it back as
	// `captcha_sid` form param on the retry request.
	SID string

	// ImgURL is the JPEG/PNG URL of the textual captcha. Open in
	// any browser / image viewer; type the depicted characters.
	ImgURL string

	// RedirectURI is VK's "not-a-robot" challenge endpoint (slider
	// captcha + advanced fingerprinting). Solving this form yields
	// a `success_token` that bypasses the textual captcha; out of
	// scope for this minimal solver.
	RedirectURI string

	// Attempt is 1 on the first challenge, increments per retry.
	Attempt int
}

// captchaNeededMsg is VK's error_msg for code-14 challenges. Pulled
// out as a const so multiple match sites don't drift.
const captchaNeededMsg = "Captcha needed"

// Solution is what a CaptchaSolver returns. Either Key (text answer)
// or SuccessToken (from the slider/redirect flow) must be set.
type Solution struct {
	// Key is the text the user typed from ImgURL. Sent as
	// `captcha_key` form param.
	Key string

	// SuccessToken is the bearer-style token returned by the
	// slider captcha at RedirectURI. Sent as `captcha_token` form
	// param. If non-empty, Key is ignored.
	SuccessToken string
}

// CaptchaSolver is the embedder hook for resolving VK captcha
// challenges. The Tunnel calls Solve when step 3 returns
// "captcha needed"; the implementation is expected to display the
// challenge to a user (or hand it off to a third-party solver
// service) and return a Solution.
//
// Implementations must respect ctx cancellation and SHOULD bound
// their wait time — VK captcha sessions are short-lived (~minutes).
type CaptchaSolver interface {
	Solve(ctx context.Context, ch CaptchaChallenge) (Solution, error)
}

// SolverFunc adapts a function to the CaptchaSolver interface.
type SolverFunc func(ctx context.Context, ch CaptchaChallenge) (Solution, error)

// Solve satisfies CaptchaSolver.
func (f SolverFunc) Solve(ctx context.Context, ch CaptchaChallenge) (Solution, error) {
	return f(ctx, ch)
}

// rejectingSolver is the default when WithCaptchaSolver was not
// supplied: it returns wgturn.ErrCaptchaRequired so callers can branch
// on the public sentinel even without a configured solver.
type rejectingSolver struct{}

func (rejectingSolver) Solve(_ context.Context, _ CaptchaChallenge) (Solution, error) {
	return Solution{}, fmt.Errorf("%w: solver not configured", wgturn.ErrCaptchaRequired)
}

// extractCaptcha walks an "error" object as returned by VK and pulls
// out captcha_sid / captcha_img / redirect_uri. Returns nil if the
// error block is not a captcha challenge.
func extractCaptcha(errObj map[string]any) *CaptchaChallenge {
	code, _ := errObj["error_code"].(float64)
	msg, _ := errObj["error_msg"].(string)
	if int(code) != 14 && msg != captchaNeededMsg {
		return nil
	}
	sid, _ := errObj["captcha_sid"].(string)
	img, _ := errObj["captcha_img"].(string)
	redirect, _ := errObj["redirect_uri"].(string)
	if sid == "" && img == "" {
		// Captcha message but no fields — caller can't solve.
		return nil
	}
	attempt := 1
	if v, ok := errObj["captcha_attempt"].(float64); ok {
		attempt = int(v)
	}
	return &CaptchaChallenge{
		SID:         sid,
		ImgURL:      img,
		RedirectURI: redirect,
		Attempt:     attempt,
	}
}

// applySolution mutates the form values to include captcha_sid +
// captcha_key (or captcha_token) so the retry passes VK's gate.
func applySolution(form interface{ Set(k, v string) }, ch CaptchaChallenge, sol Solution) error {
	if ch.SID == "" {
		return fmt.Errorf("captcha SID is empty")
	}
	form.Set("captcha_sid", ch.SID)
	switch {
	case sol.SuccessToken != "":
		form.Set("captcha_token", sol.SuccessToken)
	case sol.Key != "":
		form.Set("captcha_key", sol.Key)
	default:
		return fmt.Errorf("solver returned empty solution")
	}
	return nil
}
