// Package jsdriver carries the Node program that drives the vendored
// TypeScript compiler. Embedded alongside the compiler so magma stays a single
// self-contained binary with nothing to install.
package jsdriver

import (
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed driver.js
var driver []byte

// Extract writes driver.js into dir, skipping it when already present at the
// expected size. Same idempotence reasoning as jsvendor.Extract: magma runs
// constantly, and rewriting on every invocation would cost for no benefit.
func Extract(dir string) error {
	dst := filepath.Join(dir, "driver.js")
	if fi, err := os.Stat(dst); err == nil && fi.Size() == int64(len(driver)) {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, driver, 0o644)
}
