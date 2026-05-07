// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build embedded && !((linux && amd64) || (darwin && amd64) || (darwin && arm64) || (windows && amd64))

package embedded

// chromiumZip is empty on platforms without an embedded
// chrome-headless-shell archive even when the binary was built with
// `-tags embedded`. Notably linux/arm64: Chrome for Testing doesn't
// publish that combination, so users on Raspberry Pi must install
// chromium-browser themselves.
//
// Extract() detects the empty slice and returns ErrUnsupportedPlatform.
var chromiumZip []byte

const (
	chromiumVersion  = ""
	chromiumPlatform = ""
	chromiumBinary   = ""
)
