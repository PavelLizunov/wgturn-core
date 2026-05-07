// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package vk

import (
	"errors"
	"fmt"

	"github.com/PavelLizunov/wgturn-core/pkg/wgturn"
)

// errCaptchaChallenge is the sentinel that the apiClient wraps around
// a captcha-flavoured VK error. Callers that want to invoke a
// CaptchaSolver use errors.Is(err, errCaptchaChallenge) and then
// lastCaptchaChallenge(err) to recover the structured payload.
var errCaptchaChallenge = errors.New("vk: captcha challenge")

// captchaErrWrapper carries the parsed captcha payload alongside an
// error chain that includes errCaptchaChallenge for errors.Is.
type captchaErrWrapper struct {
	challenge CaptchaChallenge
	parent    error
}

func (w *captchaErrWrapper) Error() string {
	return fmt.Sprintf("vk: captcha required (sid=%s, img=%s)", w.challenge.SID, w.challenge.ImgURL)
}

func (w *captchaErrWrapper) Unwrap() error { return w.parent }

// Is reports whether target is errCaptchaChallenge or anything in the
// parent chain. We can't put errCaptchaChallenge inside Unwrap (which
// is a single-target API) because parent already provides errors.Is
// against wgturn.ErrCaptchaRequired.
func (w *captchaErrWrapper) Is(target error) bool {
	return target == errCaptchaChallenge
}

// lastCaptchaChallenge unwraps an error chain looking for a
// captchaErrWrapper. Returns nil if the chain doesn't contain one.
func lastCaptchaChallenge(err error) *CaptchaChallenge {
	var w *captchaErrWrapper
	if errors.As(err, &w) {
		return &w.challenge
	}
	return nil
}

// detectVKError inspects a successfully-decoded JSON body for VK / OK
// error envelopes. VK uses HTTP 200 with an "error" object; OK uses
// HTTP 200 with "error_code"/"error_msg" fields or with a captcha
// response. We map the well-known cases to the public sentinel errors
// in pkg/wgturn so callers can branch on them.
//
// For captcha-needed (code 14) we additionally extract sid + img and
// wrap the error so a CaptchaSolver can resolve it.
func detectVKError(resp vkResponse) error {
	// VK shape: {"error": {"error_code": N, "error_msg": "..."}}
	// or       {"error": "captcha_required", "captcha_sid": "...", "captcha_img": "..."}
	if errAny, ok := resp["error"]; ok && errAny != nil {
		switch e := errAny.(type) {
		case string:
			// VK's auth flow sometimes returns "error":"need_captcha" etc.
			if e == "need_captcha" || e == "captcha_required" {
				ch := &CaptchaChallenge{
					SID:    asString(resp["captcha_sid"]),
					ImgURL: asString(resp["captcha_img"]),
				}
				if ch.SID == "" && ch.ImgURL == "" {
					return fmt.Errorf("%w: %s", wgturn.ErrCaptchaRequired, e)
				}
				return &captchaErrWrapper{challenge: *ch,
					parent: fmt.Errorf("%w: %s", wgturn.ErrCaptchaRequired, e)}
			}
			return fmt.Errorf("vk error: %s", e)
		case map[string]any:
			code, _ := e["error_code"].(float64)
			msg, _ := e["error_msg"].(string)
			if code == 14 || msg == captchaNeededMsg {
				ch := extractCaptcha(e)
				wrappedMsg := fmt.Errorf("%w: code=%d msg=%q",
					wgturn.ErrCaptchaRequired, int(code), msg)
				if ch != nil {
					return &captchaErrWrapper{challenge: *ch, parent: wrappedMsg}
				}
				return wrappedMsg
			}
			if code == 5 || code == 15 || code == 17 {
				return fmt.Errorf("%w: code=%d msg=%q", wgturn.ErrAuthFailure, int(code), msg)
			}
			return fmt.Errorf("vk error code=%d msg=%q", int(code), msg)
		}
	}

	// OK shape: {"error_code": N, "error_msg": "..."}  (no "error" wrapper)
	if codeAny, ok := resp["error_code"]; ok {
		code, _ := codeAny.(float64)
		msg, _ := resp["error_msg"].(string)
		if code == 14 || msg == captchaNeededMsg {
			return fmt.Errorf("%w: ok code=%d msg=%q", wgturn.ErrCaptchaRequired, int(code), msg)
		}
		return fmt.Errorf("ok error code=%d msg=%q", int(code), msg)
	}

	return nil
}

// asString returns x as string when possible, else "".
func asString(x any) string {
	s, _ := x.(string)
	return s
}
