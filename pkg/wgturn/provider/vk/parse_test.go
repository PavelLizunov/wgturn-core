// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package vk

import (
	"errors"
	"testing"
)

func TestExtractCallID(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		// Canonical forms
		{"https://vk.com/call/join/abcXYZ123", "abcXYZ123", false},
		{"https://vk.ru/call/join/abcXYZ123", "abcXYZ123", false},
		{"http://vk.com/call/join/abc-def_ghi.jkl", "abc-def_ghi.jkl", false},

		// With trailing query / fragment / path
		{"https://vk.com/call/join/abc?utm=foo", "abc", false},
		{"https://vk.com/call/join/abc#section", "abc", false},
		{"https://vk.com/call/join/abc/extra", "abc", false},
		{"https://vk.com/call/join/abc?x=1&y=2", "abc", false},

		// Without scheme
		{"vk.com/call/join/abc", "abc", false},
		{"vk.ru/call/join/abc", "abc", false},

		// Bare id (already extracted)
		{"abcXYZ123", "abcXYZ123", false},
		{"  abcXYZ123  ", "abcXYZ123", false}, // surrounding whitespace

		// Invalid
		{"", "", true},
		{"https://vk.com/", "", true},
		{"https://vk.com/call/join/", "", true},
		{"https://vk.com/call/join/!!!", "", true}, // disallowed chars
		{"https://vk.com/call/join/a b", "", true}, // space inside id
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := extractCallID(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if tc.wantErr && err != nil && !errors.Is(err, ErrInvalidLink) {
				t.Errorf("err = %v, want ErrInvalidLink", err)
			}
		})
	}
}

func TestParseTurnURL(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		// Canonical
		{"turn:1.2.3.4:3478", "1.2.3.4:3478", false},
		{"turns:1.2.3.4:5349", "1.2.3.4:5349", false},
		{"turn:relay.example.com:3478", "relay.example.com:3478", false},

		// With query
		{"turn:1.2.3.4:3478?transport=tcp", "1.2.3.4:3478", false},
		{"turn:relay.example.com:3478?transport=udp&extra=1", "relay.example.com:3478", false},

		// Trailing whitespace
		{"  turn:1.2.3.4:3478  ", "1.2.3.4:3478", false},

		// Schemeless host:port also works (defensive — sometimes upstream omits the prefix)
		{"1.2.3.4:3478", "1.2.3.4:3478", false},

		// Invalid
		{"", "", true},
		{"turn:no-port", "", true},
		{"http://wrong-protocol:80", "", true}, // host has scheme letters that SplitHostPort dislikes
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseTurnURL(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
