// Package lib is a fixture with NO production main: only a library package plus
// a test. Its call graph is computable, but reachability has no external-caller
// root, so the derived views must refuse rather than report false positives.
package lib

// Exported is public API with no in-module caller.
func Exported() {}

// helper is called only from the test below.
func helper() {}
