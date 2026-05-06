// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// Package captchasolve hosts ready-made vk.CaptchaSolver
// implementations that ship outside the parent vk package so embedders
// pick up a websocket / image-processing dep only when they want to.
//
// Implementations:
//
//   - CDPSolver: drives a headless Chrome via the DevTools protocol to
//     render VK's not-a-robot challenge page, click the "I'm not a
//     robot" checkbox, and capture the success_token from the
//     captchaNotRobot.check API response. Works because real Chrome
//     already runs VK's anti-bot fingerprint JS for us — we only have
//     to dispatch a realistic mouse click.
//
// This package depends on github.com/coder/websocket. Embedders that
// supply their own CaptchaSolver (e.g. a 2captcha or in-app webview
// hookup) can ignore this package entirely.
package captchasolve
