// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

// Package yandex is a wgturn.CredentialsProvider that obtains TURN
// credentials from Yandex Telemost's anonymous-conference API.
//
// Why a second provider after VK?
//
// VK Calls' TURN servers (AS47764 / AS47542, Mail.ru group) shape
// per-call session bandwidth around voice-call quality. Empirical
// soak tests on a Russian client peak at ≈135 KB/s aggregate even
// with -streams 16 spread across 4 distinct VK call sessions —
// suggesting either per-source-IP shaping or shared backend capacity.
//
// Yandex Telemost runs on AS13238 (Yandex's own ASN) with an
// independent TURN pool (5.255.211.241..246 and friends). Mixing VK +
// Yandex Telemost links via wgturn.Config.Hints lets a single Tunnel
// hit two unrelated sets of TURN servers and (hopefully) ~double the
// aggregate bandwidth ceiling.
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
