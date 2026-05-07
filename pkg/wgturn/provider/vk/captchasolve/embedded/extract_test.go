// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package embedded

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// makeFixtureZip builds a tiny zip with one binary entry shaped like
// chrome-headless-shell — just enough to exercise the extraction code
// without dragging the real ~100 MB blob into the test.
func makeFixtureZip(t *testing.T, binarySubpath, contents string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	hdr := &zip.FileHeader{Name: binarySubpath, Method: zip.Deflate}
	hdr.SetMode(0o755)
	f, err := w.CreateHeader(hdr)
	if err != nil {
		t.Fatalf("CreateHeader: %v", err)
	}
	if _, err := f.Write([]byte(contents)); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// pinCacheRootEnv routes chromiumCacheRoot() at a tmpdir for the test.
// XDG_CACHE_HOME / equivalents on macOS / Windows are honoured by
// os.UserCacheDir; setting it on the env points the helper at our
// scratch.
func pinCacheRootEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	switch runtime.GOOS {
	case "darwin":
		t.Setenv("HOME", dir)
	case "windows":
		t.Setenv("LocalAppData", dir)
	default: // linux + bsd
		t.Setenv("XDG_CACHE_HOME", dir)
	}
}

func TestExtractFrom_HappyPath(t *testing.T) {
	pinCacheRootEnv(t)

	bin := "chrome-headless-shell-fixture/chrome-headless-shell"
	z := makeFixtureZip(t, bin, "fake-binary-contents")

	got, err := extractFrom(z, "1.2.3", "fixture", bin)
	if err != nil {
		t.Fatalf("extractFrom: %v", err)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read extracted: %v", err)
	}
	if string(data) != "fake-binary-contents" {
		t.Errorf("extracted contents = %q, want %q", data, "fake-binary-contents")
	}

	// Exec bit preserved on Unix (Windows has no concept here).
	if runtime.GOOS != "windows" {
		st, err := os.Stat(got)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if st.Mode().Perm()&0o100 == 0 {
			t.Errorf("exec bit not preserved: mode=%v", st.Mode())
		}
	}
}

func TestExtractFrom_Idempotent(t *testing.T) {
	pinCacheRootEnv(t)

	bin := "fix/binary"
	z := makeFixtureZip(t, bin, "v1")

	first, err := extractFrom(z, "1.0.0", "test", bin)
	if err != nil {
		t.Fatalf("first extract: %v", err)
	}
	// Tamper with the version dir so a re-extract would overwrite.
	// Idempotent path must NOT touch existing files.
	stamp := filepath.Join(filepath.Dir(first), "tamper")
	if err := os.WriteFile(stamp, []byte("untouched"), 0o644); err != nil {
		t.Fatalf("write tamper: %v", err)
	}

	second, err := extractFrom(z, "1.0.0", "test", bin)
	if err != nil {
		t.Fatalf("second extract: %v", err)
	}
	if first != second {
		t.Errorf("path differs across calls: %q vs %q", first, second)
	}
	if _, err := os.Stat(stamp); err != nil {
		t.Errorf("idempotent re-extract obliterated tamper file: %v", err)
	}
}

func TestExtract_UnsupportedPlatformWhenZipEmpty(t *testing.T) {
	if len(chromiumZip) > 0 {
		t.Skip("this build embeds a real Chromium; skip the unsupported-platform check")
	}
	_, err := Extract()
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Errorf("err = %v, want ErrUnsupportedPlatform", err)
	}
}

// TestExtractFrom_RejectsZipSlip plants a malicious entry whose name
// resolves outside the destination dir. The extractor must refuse
// rather than write to the parent directory.
func TestExtractFrom_RejectsZipSlip(t *testing.T) {
	pinCacheRootEnv(t)

	z := makeFixtureZip(t, "../escape", "should-not-write")

	_, err := extractFrom(z, "1.0.0", "evil", "any")
	if err == nil {
		t.Fatal("expected error on zip-slip path")
	}
	// The unzip helper should mention the unsafe path; bail before
	// touching the filesystem.
}

// TestExtractFrom_MissingBinaryAfterExtract simulates a stale archive
// whose top-level binary path no longer matches what chromium_*.go
// declared. Common failure mode if someone bumps `chromiumVersion`
// without re-running fetch-chromium and the new archive shape differs.
func TestExtractFrom_MissingBinaryAfterExtract(t *testing.T) {
	pinCacheRootEnv(t)

	// Zip contains "wrong/path", we ask for "right/path".
	z := makeFixtureZip(t, "wrong/path", "x")

	_, err := extractFrom(z, "1.0.0", "test", "right/path")
	if err == nil {
		t.Fatal("expected error when binary not present after extract")
	}
}
