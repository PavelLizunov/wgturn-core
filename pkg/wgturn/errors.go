// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturn

import "errors"

// ErrAlreadyStarted is returned by Tunnel.Start if the Tunnel has
// already been started. A Tunnel is single-shot: create, Start, Stop,
// throw away.
var ErrAlreadyStarted = errors.New("wgturn: tunnel already started")

// ErrNotStarted is returned by methods that require a started Tunnel
// (e.g. Stats) when called before Start.
var ErrNotStarted = errors.New("wgturn: tunnel not started")

// ErrStartTimeout is returned by Start if no stream becomes ready
// within the start deadline. This usually indicates the credentials
// fetch failed for every stream, or the TURN allocation was refused.
var ErrStartTimeout = errors.New("wgturn: timed out waiting for first stream")
