// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package vk

import (
	"fmt"

	"github.com/slovn/wgturn-core/pkg/wgturn"
)

// detectVKError inspects a successfully-decoded JSON body for VK / OK
// error envelopes. VK uses HTTP 200 with an "error" object; OK uses
// HTTP 200 with "error_code"/"error_msg" fields or with a captcha
// response. We map the well-known cases to the public sentinel errors
// in pkg/wgturn so callers can branch on them.
func detectVKError(resp vkResponse) error {
	// VK shape: {"error": {"error_code": N, "error_msg": "..."}}
	// or       {"error": "captcha_required", "captcha_sid": "...", "captcha_img": "..."}
	if errAny, ok := resp["error"]; ok && errAny != nil {
		switch e := errAny.(type) {
		case string:
			// VK's auth flow sometimes returns "error":"need_captcha" etc.
			if e == "need_captcha" || e == "captcha_required" {
				return fmt.Errorf("%w: %s", wgturn.ErrCaptchaRequired, e)
			}
			return fmt.Errorf("vk error: %s", e)
		case map[string]any:
			code, _ := e["error_code"].(float64)
			msg, _ := e["error_msg"].(string)
			// VK uses code 14 for captcha_needed (legacy), 15 for access denied.
			// https://dev.vk.com/api/errors
			if code == 14 || msg == "Captcha needed" {
				return fmt.Errorf("%w: code=%d msg=%q", wgturn.ErrCaptchaRequired, int(code), msg)
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
		if code == 14 || msg == "Captcha needed" {
			return fmt.Errorf("%w: ok code=%d msg=%q", wgturn.ErrCaptchaRequired, int(code), msg)
		}
		return fmt.Errorf("ok error code=%d msg=%q", int(code), msg)
	}

	return nil
}
