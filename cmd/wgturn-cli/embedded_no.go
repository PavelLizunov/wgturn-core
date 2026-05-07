// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build !embedded

package main

// extractEmbeddedChrome stays nil in default builds. findChromeOnPath
// short-circuits the fallback when the variable is nil, so the binary
// behaves exactly as it did before the embedded build tag existed.
//
// This file exists only so the build-tag layout is symmetric and a
// reader looking at build tag combinations sees both branches.
