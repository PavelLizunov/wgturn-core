// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build embedded

package main

import "github.com/PavelLizunov/wgturn-core/pkg/wgturn/provider/vk/captchasolve/embedded"

// init wires findChromeOnPath's last-resort embedded-Chromium fallback.
// Only compiled with `-tags embedded`; default builds leave
// extractEmbeddedChrome nil and the binary stays small (~9 MB instead
// of ~100 MB).
func init() {
	extractEmbeddedChrome = embedded.Extract
}
