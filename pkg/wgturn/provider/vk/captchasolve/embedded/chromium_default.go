// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build !embedded

package embedded

// Default build: no chromium archive embedded (binary stays small).
// Extract() returns ErrUnsupportedPlatform; the CLI falls back to its
// "install Chrome" install-hint error.
//
// This file lets the package compile + run its fixture-based tests
// without the multi-hundred-MB zip blobs being present, which is the
// developer-laptop default. The `-tags embedded` builds replace this
// file with one of chromium_<os>_<arch>.go (per platform) carrying a
// real go:embed.
var chromiumZip []byte

const (
	chromiumVersion  = ""
	chromiumPlatform = ""
	chromiumBinary   = ""
)
