// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// Package yandex is a wgturn.CredentialsProvider that obtains TURN
// credentials from Yandex Telemost's anonymous-conference API.
//
// IMPORTANT — runtime-verified limitation (2026-05):
//
// Yandex Telemost's TURN service (turn.tel.yandex.net:443, the host
// returned by the GOLOOM media-server WebSocket) enforces a peer-IP
// allowlist that only permits relaying to Yandex SFU clusters
// (5.255.x.x / 37.9.x.x / 77.88.x.x in their internal RTC AS). Tested
// with a wgturn-server on a foreign VPS (93.95.226.167:56000): the
// TURN allocation succeeds, CreatePermission appears to succeed, but
// not a single relayed UDP packet reaches the peer — confirmed via
// tcpdump on the receiver. This makes Telemost UNUSABLE as a wgturn
// transport even though the credentials are real and anonymous.
//
// In contrast, VK Calls TURN (the existing provider/vk) imposes no
// peer-IP filter and happily relays to any destination, which is why
// VK works as a wgturn transport and Telemost does not.
//
// This package is still kept in the tree because:
//
//  1. The credential extraction is correct and could be useful for
//     future research / logging / non-relay use cases.
//  2. The CLI's routedProvider must dispatch Telemost-shaped hints
//     somewhere — better an explicit "peer-IP denied" failure than a
//     misleading parse error.
//  3. If Yandex relaxes their TURN filter (e.g., for a future
//     diagnostics endpoint) the provider will start working without
//     code changes.
//
// API surface (anonymous, no cookie / no account):
//
// API surface (anonymous, no cookie / no account):
//
//  1. POST https://cloud-api.yandex.ru/telemost_front/v2/telemost/
//     conferences/<URL-ENCODED-LINK>/connection?
//     next_gen_media_platform_allowed=false
//     where LINK = https://telemost.yandex.ru/j/<callID>.
//     Response carries media_server_url + bootstrap credentials.
//
//  2. WebSocket to media_server_url, send a "hello" frame, read
//     ServerHello which contains RtcConfiguration.IceServers —
//     each entry has urls/username/credential.
//
// Caveat: Yandex enforces a soft anti-flood limit at one concurrent
// participant per conference per source IP. Drive multiple Telemost
// links if you want N streams.
//
// Reference impl: kiper292/vk-turn-proxy/client/main.go (getYandexCreds).
package yandex
