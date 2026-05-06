// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"reflect"
	"testing"
)

// TestSplitLinks pins the parsing contract for -vk-link:
// commas, whitespace, and newlines are all valid separators; empty
// fragments are dropped; surrounding whitespace is trimmed.
func TestSplitLinks(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{"a, b, c", []string{"a", "b", "c"}},
		{"a , ,b", []string{"a", "b"}},
		{"a\tb", []string{"a", "b"}},
		{"a\nb\nc", []string{"a", "b", "c"}},
		{",,,a,,,", []string{"a"}},
		{"https://vk.ru/call/join/X,https://vk.ru/call/join/Y",
			[]string{"https://vk.ru/call/join/X", "https://vk.ru/call/join/Y"}},
	}
	for _, c := range cases {
		got := splitLinks(c.in)
		if (len(got) == 0 && len(c.want) == 0) || reflect.DeepEqual(got, c.want) {
			continue
		}
		t.Errorf("splitLinks(%q) = %v, want %v", c.in, got, c.want)
	}
}
