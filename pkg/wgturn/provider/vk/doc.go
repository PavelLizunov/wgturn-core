// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// Package vk implements a wgturn.CredentialsProvider that obtains
// anonymous TURN credentials from VK Calls' public API given a regular
// "https://vk.com/call/join/<id>" invite link.
//
// # Wire flow
//
// One Fetch call performs six HTTP round-trips to VK / OK CDN:
//
//  1. POST login.vk.ru?act=get_anonym_token → primary anonymous token
//  2. POST api.vk.ru/method/calls.getAnonymousAccessTokenPayload → payload
//  3. POST login.vk.ru?act=get_anonym_token (with payload) → secondary token
//  4. POST api.vk.ru/method/calls.getAnonymousToken → call-scoped token
//  5. POST calls.okcdn.ru/fb.do auth.anonymLogin → OK session_key
//  6. POST calls.okcdn.ru/fb.do vchat.joinConversationByLink → TURN credentials
//
// All six calls go through the same *http.Client; the credentials
// cache in pkg/wgturn (internal/creds) memoizes the result for ~9 minutes
// so this dance only happens at warmup and during refresh.
//
// # Captcha
//
// VK can challenge with a captcha after sustained load or when its bot
// heuristics fire. This package does NOT solve captchas; it surfaces
// the situation as wgturn.ErrCaptchaRequired and lets the embedder
// decide (manual UI, third-party solver, retry-after, etc.). A
// future sub-module under provider/vk/captcha may bundle a solver.
//
// # Bot detection
//
// We rotate User-Agent + Sec-CH-UA hint headers per call from a small
// pool of plausible Chrome / Edge profiles. We do NOT use uTLS or
// browser-fingerprint TLS — that level of evasion is overkill for the
// volume one user generates and would drag in ~12 transitive deps.
//
// # Stability
//
// VK API endpoints can change without notice. The current shape was
// observed in 2025-Q4 and matches both cacggghp/vk-turn-proxy and
// kiper292/vk-turn-proxy v2. Treat as best-effort; pin a tested
// version in production.
package vk
