// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package vk

import (
	"errors"
	"net"
	"strings"
)

// ErrInvalidLink is returned when the VK call link cannot be parsed.
var ErrInvalidLink = errors.New("vk: invalid call link")

// extractCallID pulls the bare call ID out of a "VK call invite link".
//
// Accepted forms (case-insensitive scheme; trailing /, ?, # tolerated):
//
//	https://vk.com/call/join/<id>
//	https://vk.ru/call/join/<id>
//	http://vk.com/call/join/<id>?utm_source=foo
//	vk.com/call/join/<id>
//	<id>                                      (already a bare id)
func extractCallID(link string) (string, error) {
	s := strings.TrimSpace(link)
	if s == "" {
		return "", ErrInvalidLink
	}

	// Track whether the input looked URL-shaped: if it did, we must
	// see the join/ marker; otherwise we'd happily accept "vk.com/"
	// or "vk.com/call" as an id.
	urlLike := strings.Contains(s, "/") || strings.Contains(s, "://")

	// Strip scheme if present.
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}

	const marker = "join/"
	if i := strings.Index(s, marker); i >= 0 {
		s = s[i+len(marker):]
	} else if urlLike {
		return "", ErrInvalidLink
	}

	// Trim trailing query/fragment/path delimiters.
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}

	s = strings.TrimSpace(s)
	if s == "" {
		return "", ErrInvalidLink
	}

	// Sanity: VK call IDs are URL-safe-ish (alphanumerics + a few
	// punctuation marks). Reject anything obviously off.
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

// parseTurnURL extracts host:port from a TURN URL of the form
//
//	turn:host:port?transport=tcp
//	turns:host:port
//	turn:host:port
//
// It tolerates and discards any query string. The result is suitable
// for net.ResolveUDPAddr / net.Dial.
func parseTurnURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("vk: empty turn URL")
	}
	// Drop scheme (turn: / turns: / stun:).
	for _, prefix := range []string{"turns:", "turn:", "stun:", "stuns:"} {
		if strings.HasPrefix(s, prefix) {
			s = s[len(prefix):]
			break
		}
	}
	// Drop query string.
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	// Validate host:port form.
	if _, _, err := net.SplitHostPort(s); err != nil {
		return "", errors.New("vk: turn URL missing host:port: " + s)
	}
	return s, nil
}
