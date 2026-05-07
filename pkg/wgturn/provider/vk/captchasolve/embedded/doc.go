// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// Package embedded ships a build-tagged Chromium (chrome-headless-shell)
// archive inside the binary so wgturn-cli can solve VK captchas
// without the user pre-installing Chrome.
//
// # Use
//
// Build with the `embedded` build tag:
//
//	go build -tags embedded ./cmd/wgturn-cli
//
// At runtime, the CLI's findChromeOnPath() falls back to embedded.Extract()
// when it can't find a system Chrome / Chromium. The first invocation
// unpacks the bundled archive into the user cache directory
// (~/.cache/wgturn/chromium/<version>-<platform>/) and returns the path
// to chrome-headless-shell; subsequent invocations are a stat call.
//
// # Cost
//
// The package adds ~95-115 MB to the binary (the chrome-headless-shell
// archive size, see chromium_*.go). Default builds DO NOT import this
// package, so size impact is opt-in.
//
// # Supported platforms
//
// Chrome for Testing publishes chrome-headless-shell for:
//
//   - linux/amd64
//   - darwin/amd64
//   - darwin/arm64
//   - windows/amd64
//
// linux/arm64 is NOT published by Chrome for Testing (no official
// headless_shell build); on that platform Extract() returns
// ErrUnsupportedPlatform and the user must install chromium-browser
// themselves.
//
// # Build prerequisite
//
// The chromium/*.zip archives are NOT in git (~400 MB total) — they
// are fetched at build time by `make fetch-chromium`. Without that
// step, `go build -tags embedded` fails with "no matching files
// found" from go:embed.
//
// # Provenance
//
// Archives are fetched from chrome-for-testing-public storage
// (https://googlechromelabs.github.io/chrome-for-testing/). Chromium
// is BSD-licensed; redistribution is permitted but downstream binaries
// must include the Chromium NOTICE/LICENSE. wgturn-core's own NOTICE
// covers this when the embedded tag is on.
package embedded
