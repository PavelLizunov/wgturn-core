// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package embedded

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrUnsupportedPlatform indicates the current GOOS/GOARCH has no
// embedded chrome-headless-shell archive bundled in this build. Caller
// should fall back to system Chrome / Chromium or surface a "please
// install chromium" message.
var ErrUnsupportedPlatform = errors.New("embedded: chrome-headless-shell not bundled for this platform")

// Extract unpacks the embedded chrome-headless-shell archive into the
// user's cache directory and returns the absolute path to the binary.
//
// Idempotent: if the binary already exists at the expected path
// (cache/<version>-<platform>/<binarySubpath>), it's returned without
// re-extracting. Concurrent callers are safe — extraction goes through
// a temporary directory and a single rename, so two racing extracts
// either both win their own scratch space and one gets ENOTEMPTY on
// rename (then we re-stat the binary and return).
//
// Returns ErrUnsupportedPlatform on platforms with no bundled archive
// (notably linux/arm64).
func Extract() (string, error) {
	if len(chromiumZip) == 0 {
		return "", fmt.Errorf("%w (GOOS=%s GOARCH=%s)",
			ErrUnsupportedPlatform, runtime.GOOS, runtime.GOARCH)
	}
	return extractFrom(chromiumZip, chromiumVersion, chromiumPlatform, chromiumBinary)
}

// extractFrom is the inner mechanism, factored out so the test suite
// can exercise it with fixture archives instead of the real ~100 MB
// blob (which would slow the test suite to a crawl).
func extractFrom(zipBytes []byte, version, platformTag, binarySubpath string) (string, error) {
	cacheRoot, err := chromiumCacheRoot()
	if err != nil {
		return "", fmt.Errorf("embedded: cache dir: %w", err)
	}
	versionDir := filepath.Join(cacheRoot, version+"-"+platformTag)
	binaryPath := filepath.Join(versionDir, binarySubpath)

	// Idempotency: binary already on disk → done.
	if st, err := os.Stat(binaryPath); err == nil && !st.IsDir() {
		return binaryPath, nil
	}

	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return "", fmt.Errorf("embedded: mkdir cache root: %w", err)
	}

	// Extract into a scratch dir alongside cacheRoot (same filesystem so
	// rename is atomic), then rename into place.
	scratch, err := os.MkdirTemp(cacheRoot, ".extract-")
	if err != nil {
		return "", fmt.Errorf("embedded: mktemp: %w", err)
	}
	scratchOK := false
	defer func() {
		if !scratchOK {
			_ = os.RemoveAll(scratch)
		}
	}()

	if err := unzip(zipBytes, scratch); err != nil {
		return "", fmt.Errorf("embedded: unzip: %w", err)
	}

	scratchBinary := filepath.Join(scratch, binarySubpath)
	if st, err := os.Stat(scratchBinary); err != nil || st.IsDir() {
		return "", fmt.Errorf("embedded: extracted archive missing %q (have you re-fetched after a Chromium version bump?)",
			binarySubpath)
	}

	// Rename scratch → versionDir. If it loses to a concurrent extract,
	// re-check the binary and return success when present.
	if err := os.Rename(scratch, versionDir); err != nil {
		if st, err2 := os.Stat(binaryPath); err2 == nil && !st.IsDir() {
			// Concurrent extract won; the scratch dir gets cleaned up
			// by the deferred RemoveAll because scratchOK stays false.
			return binaryPath, nil
		}
		return "", fmt.Errorf("embedded: rename scratch into place: %w", err)
	}
	scratchOK = true
	return binaryPath, nil
}

// unzip extracts zipBytes into dstRoot. Rejects zip-slip paths and
// preserves Unix exec bits when present (so chrome-headless-shell stays
// executable on Linux/macOS).
func unzip(zipBytes []byte, dstRoot string) error {
	r, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}

	cleanRoot := filepath.Clean(dstRoot) + string(os.PathSeparator)
	for _, f := range r.File {
		// Reject absolute paths and zip-slip ("../").
		name := filepath.Clean(f.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return fmt.Errorf("zip entry %q has unsafe path", f.Name)
		}
		dst := filepath.Join(dstRoot, name)
		// Defence in depth: ensure the resolved path stays under dstRoot
		// (covers /-on-/ joining quirks and symlink-style traversal).
		if !strings.HasPrefix(filepath.Clean(dst)+string(os.PathSeparator), cleanRoot) &&
			filepath.Clean(dst) != filepath.Clean(dstRoot) {
			return fmt.Errorf("zip entry %q escapes destination", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return fmt.Errorf("mkdir %q: %w", dst, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("mkdir parent %q: %w", filepath.Dir(dst), err)
		}

		mode := f.Mode().Perm()
		// Default to 0644 when zip carries no meaningful perm (Windows
		// archives sometimes do this for regular files).
		if mode == 0 {
			mode = 0o644
		}

		if err := writeZipEntry(f, dst, mode); err != nil {
			return err
		}
	}
	return nil
}

func writeZipEntry(f *zip.File, dst string, mode os.FileMode) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open zip entry %q: %w", f.Name, err)
	}
	defer func() { _ = rc.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create %q: %w", dst, err)
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, rc); err != nil {
		return fmt.Errorf("copy %q: %w", f.Name, err)
	}
	return nil
}

// chromiumCacheRoot returns a per-user directory we own:
// $XDG_CACHE_HOME/wgturn/chromium (Linux), ~/Library/Caches/wgturn/chromium
// (macOS), %LocalAppData%/wgturn/chromium (Windows).
//
// We deliberately don't use os.UserCacheDir directly because that doesn't
// know our app name; we wrap it.
func chromiumCacheRoot() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "wgturn", "chromium"), nil
}
