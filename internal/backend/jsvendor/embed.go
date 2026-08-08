// Package jsvendor carries the vendored TypeScript compiler and its
// standard-library type definitions, embedded in the magma binary.
//
// Embedded rather than installed, deliberately. The Rust helper is in neither
// the published release archive nor crates.io, so `go install` and the release
// binaries cannot analyse Rust at all — a defect found on 2026-08-01 and now
// documented in the README. JavaScript has no such gap: if you have magma and
// `node`, you can analyse it.
//
// See VENDOR.md for why the version is pinned exactly, why all 102 lib files
// are carried, and how to re-vendor.
package jsvendor

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

// Version is the exact pin. Load-bearing: `latest` is the Go-native 7.x line,
// which ships platform-specific native binaries that locked decision #3
// forbids. See VENDOR.md.
const Version = "5.9.3"

//go:embed typescript.js lib/*.d.ts
var files embed.FS

// Extract writes the vendored compiler and lib files into dir, skipping any
// file already present at the expected size.
//
// Idempotent because magma is designed to be run constantly — before every
// task, in CI, as a git hook — and rewriting 12.5 MB on each invocation would
// be a visible cost for no benefit. Size is the freshness check rather than a
// hash: the embedded bytes only change when the binary does, so a same-size
// file at this path came from this build.
func Extract(dir string) error {
	return fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		src, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, p)
		if fi, err := os.Stat(dst); err == nil && fi.Size() == int64(len(src)) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, src, 0o644)
	})
}
