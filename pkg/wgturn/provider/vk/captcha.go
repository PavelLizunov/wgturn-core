// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package vk

import (
	"context"
	"fmt"

	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
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
	// a `success_token` that bypasses the textual captcha.
	RedirectURI string

	// Attempt is 1 on the first challenge, increments per retry.
	// Echoed back as `captcha_attempt` on the retry — VK won't
	// accept the success_token without it.
	Attempt int

	// TS is the captcha-issued-at timestamp from VK's error envelope
	// (`captcha_ts`). Empty for legacy text-only captchas. Echoed
	// back verbatim on the retry; VK uses it to scope the solution
	// to the original challenge.
	TS string
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
// out captcha_sid / captcha_img / redirect_uri / captcha_ts. Returns
// nil if the error block is not a captcha challenge.
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
	switch v := errObj["captcha_attempt"].(type) {
	case float64:
		attempt = int(v)
	case string:
		// VK sometimes serialises this as a quoted string.
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			attempt = n
		}
	}
	// captcha_ts is a unix timestamp or float; we keep it as a
	// string for byte-exact echo back to VK.
	var ts string
	switch v := errObj["captcha_ts"].(type) {
	case string:
		ts = v
	case float64:
		// JSON "1234567890" parses as float64 in encoding/json. Round
		// to nearest integer to avoid scientific notation.
		ts = fmt.Sprintf("%d", int64(v))
	}
	return &CaptchaChallenge{
		SID:         sid,
		ImgURL:      img,
		RedirectURI: redirect,
		Attempt:     attempt,
		TS:          ts,
	}
}

// applySolution mutates the form values to include the captcha
// solution fields VK expects on retry.
//
// For the not-a-robot redirect flow (sol.SuccessToken is set):
//
//   - captcha_sid: from the original error envelope
//   - captcha_ts: ditto, echoed verbatim
//   - captcha_attempt: ditto, echoed verbatim
//   - is_sound_captcha: 0 (we always solve via the slider/checkbox path)
//   - captcha_key: empty (VK uses presence to disambiguate from text mode)
//   - success_token: URL-escaped JWT from captchaNotRobot.check
//
// For the legacy text captcha flow (sol.Key is set):
//
//   - captcha_sid + captcha_key only (mirrors the historical API).
//
// Field names match cacggghp/vk-turn-proxy's getTokenChain — VK
// rejects the success_token if any of {captcha_ts, captcha_attempt,
// is_sound_captcha, empty captcha_key} are missing, replying with a
// fresh challenge instead of advancing the flow.
func applySolution(form interface{ Set(k, v string) }, ch CaptchaChallenge, sol Solution) error {
	if ch.SID == "" {
		return fmt.Errorf("captcha SID is empty")
	}
	form.Set("captcha_sid", ch.SID)
	switch {
	case sol.SuccessToken != "":
		form.Set("captcha_key", "")
		form.Set("is_sound_captcha", "0")
		form.Set("success_token", sol.SuccessToken)
		if ch.TS != "" {
			form.Set("captcha_ts", ch.TS)
		}
		if ch.Attempt > 0 {
			form.Set("captcha_attempt", fmt.Sprintf("%d", ch.Attempt))
		}
	case sol.Key != "":
		form.Set("captcha_key", sol.Key)
	default:
		return fmt.Errorf("solver returned empty solution")
	}
	return nil
}
