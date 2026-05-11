// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// version is the build-time version string. Stamped via:
//
//	go build -ldflags="-X main.version=<git-sha>" ./cmd/wgturn-cli
//
// Defaults to "dev" for `go run` / unstamped builds. Falls back to the
// module's VCS info embedded by `go build` when unset.
var version = "dev"

// printVersion writes the resolved version + Go runtime to stdout.
// Format intentionally kept stable for downstream tooling that may grep
// the output (e.g. VPNRouter's installer health-check).
func printVersion() {
	v := resolveVersion()
	fmt.Printf("wgturn-cli %s (%s %s/%s)\n",
		v, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// resolveVersion returns the ldflags-stamped version if present, else
// the VCS revision embedded by `go build`, else "dev".
func resolveVersion() string {
	if version != "dev" && version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				if len(s.Value) > 12 {
					return s.Value[:12]
				}
				return s.Value
			}
		}
	}
	return "dev"
}
