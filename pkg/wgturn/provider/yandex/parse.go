// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package yandex

import (
	"errors"
	"net"
	"strconv"
	"strings"
)

// ErrInvalidLink is returned when the Telemost link cannot be parsed.
var ErrInvalidLink = errors.New("yandex: invalid telemost link")

// extractCallID pulls the bare conference ID out of a Telemost invite
// link. Accepted shapes:
//
//	https://telemost.yandex.ru/j/<id>
//	https://telemost.yandex.com/j/<id>      (international mirror)
//	telemost.yandex.ru/j/<id>
//	telemost:<id>                            (CLI shorthand for multi-provider routing)
//	<id>                                      (already a bare id)
func extractCallID(link string) (string, error) {
	s := strings.TrimSpace(link)
	if s == "" {
		return "", ErrInvalidLink
	}

	// CLI multi-provider tag: "telemost:<id>" — strip the prefix.
	if strings.HasPrefix(strings.ToLower(s), "telemost:") {
		s = s[len("telemost:"):]
	}

	urlLike := strings.Contains(s, "/") || strings.Contains(s, "://")

	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}

	const marker = "/j/"
	if i := strings.Index(s, marker); i >= 0 {
		s = s[i+len(marker):]
	} else if urlLike {
		return "", ErrInvalidLink
	}

	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ErrInvalidLink
	}

	for _, r := range s {
		if !isCallIDRune(r) {
			return "", ErrInvalidLink
		}
	}
	return s, nil
}

func isCallIDRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z',
		r >= 'A' && r <= 'Z',
		r >= '0' && r <= '9',
		r == '-' || r == '_' || r == '.':
		return true
	}
	return false
}

// IsTelemostLink reports whether s looks like a Telemost invite link.
// Used by multi-provider routers to dispatch the right backend; never
// false-positives on VK call links (which have /call/join/, not /j/).
func IsTelemostLink(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	if strings.HasPrefix(low, "telemost:") {
		return true
	}
	return strings.Contains(low, "telemost.yandex.")
}

// parseTurnURL extracts host:port from a TURN URL. Yandex returns a
// mix of turn:/turns: + ?transport=udp/tcp; we accept all and strip
// the scheme + query. The port must parse as an integer in 1..65535
// — net.SplitHostPort alone is permissive and would accept e.g.
// "host:not-a-port".
func parseTurnURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("yandex: empty turn URL")
	}
	for _, prefix := range []string{"turns:", "turn:", "stuns:", "stun:"} {
		if strings.HasPrefix(s, prefix) {
			s = s[len(prefix):]
			break
		}
	}
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", errors.New("yandex: turn URL missing host:port: " + s)
	}
	if host == "" {
		return "", errors.New("yandex: turn URL missing host: " + s)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", errors.New("yandex: turn URL has bad port: " + s)
	}
	return s, nil
}
