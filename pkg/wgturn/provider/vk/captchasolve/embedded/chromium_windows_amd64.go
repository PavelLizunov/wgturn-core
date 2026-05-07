// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build embedded && windows && amd64

package embedded

import _ "embed"

//go:embed chromium/chrome-headless-shell-win64.zip
var chromiumZip []byte

const (
	chromiumVersion  = "148.0.7778.97"
	chromiumPlatform = "win64"
	chromiumBinary   = "chrome-headless-shell-win64/chrome-headless-shell.exe"
)
