// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build embedded && darwin && arm64

package embedded

import _ "embed"

//go:embed chromium/chrome-headless-shell-mac-arm64.zip
var chromiumZip []byte

const (
	chromiumVersion  = "148.0.7778.97"
	chromiumPlatform = "mac-arm64"
	chromiumBinary   = "chrome-headless-shell-mac-arm64/chrome-headless-shell"
)
