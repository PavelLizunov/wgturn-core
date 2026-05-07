// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build embedded && linux && amd64

package embedded

import _ "embed"

// chromiumZip is the chrome-headless-shell archive embedded into the
// binary at build time. Fetched by `make fetch-chromium` from
// https://googlechromelabs.github.io/chrome-for-testing/ — kept out
// of git (.gitignore) because it's ~115 MB.
//
//go:embed chromium/chrome-headless-shell-linux64.zip
var chromiumZip []byte

const (
	chromiumVersion  = "148.0.7778.97"
	chromiumPlatform = "linux64"
	chromiumBinary   = "chrome-headless-shell-linux64/chrome-headless-shell"
)
