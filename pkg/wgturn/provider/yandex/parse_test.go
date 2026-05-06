// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package yandex

import (
	"errors"
	"testing"
)

func TestExtractCallID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"https://telemost.yandex.ru/j/abc123", "abc123", false},
		{"https://telemost.yandex.com/j/abc-123_xy.z", "abc-123_xy.z", false},
		{"telemost.yandex.ru/j/abc", "abc", false},
		{"telemost:abc123", "abc123", false},
		{"telemost:https://telemost.yandex.ru/j/abc", "abc", false}, // tag + URL
		{"abc123", "abc123", false},                                 // bare
		{"http://telemost.yandex.ru/j/abc?utm=1", "abc", false},
		{"http://telemost.yandex.ru/j/abc/extra", "abc", false},

		// Negative
		{"", "", true},
		{"https://vk.com/call/join/abc", "", true}, // wrong service
		{"https://telemost.yandex.ru/", "", true},  // no /j/<id>
		{"abc 123", "", true},                      // space invalid
		{"абвгд", "", true},                        // non-latin
	}
	for _, c := range cases {
		got, err := extractCallID(c.in)
		if c.wantErr {
			if !errors.Is(err, ErrInvalidLink) {
				t.Errorf("extractCallID(%q) err = %v, want ErrInvalidLink", c.in, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("extractCallID(%q) unexpected err: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("extractCallID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsTelemostLink(t *testing.T) {
	t.Parallel()
	yes := []string{
		"https://telemost.yandex.ru/j/abc",
		"telemost.yandex.com/j/abc",
		"telemost:abc",
		"TELEMOST:abc",
	}
	no := []string{
		"",
		"abc",
		"https://vk.com/call/join/abc",
		"vk:abc",
	}
	for _, s := range yes {
		if !IsTelemostLink(s) {
			t.Errorf("IsTelemostLink(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if IsTelemostLink(s) {
			t.Errorf("IsTelemostLink(%q) = true, want false", s)
		}
	}
}

func TestParseTurnURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"turn:5.255.211.241:3478", "5.255.211.241:3478", false},
		{"turns:5.255.211.241:443", "5.255.211.241:443", false},
		{"turn:5.255.211.241:3478?transport=udp", "5.255.211.241:3478", false},
		{"turns:5.255.211.241:443?transport=tcp", "5.255.211.241:443", false},
		{"5.255.211.241:3478", "5.255.211.241:3478", false}, // no scheme
		{"", "", true},
		{"turn:noport", "", true},
		{"turn:host:not-a-port", "", true},
	}
	for _, c := range cases {
		got, err := parseTurnURL(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseTurnURL(%q) want error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseTurnURL(%q) unexpected err: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("parseTurnURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
