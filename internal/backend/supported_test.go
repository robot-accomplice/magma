package backend

import (
	"strings"
	"testing"
)

// Supported must be derived from the registry, never hand-written. The
// hand-written version went stale exactly as expected: both the CLI help and
// the machine-readable refusal reason claimed "supports Go" through two
// releases that supported Rust.
func TestSupportedNamesEveryRegisteredBackend(t *testing.T) {
	got := Supported()
	if len(got) != len(registry) {
		t.Fatalf("Supported() has %d entries but %d backends are registered", len(got), len(registry))
	}
	for l := range registry {
		if !strings.Contains(strings.Join(got, ","), string(l)) {
			t.Errorf("registered backend %q missing from Supported() = %v", l, got)
		}
	}
	// Sorted, so help text and refusal reasons are deterministic.
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Errorf("Supported() not sorted: %v", got)
		}
	}
}

// Registering Rust in v0.2.0 should have updated both messages and did not.
// This pins the regression rather than the current membership.
func TestSupportedIncludesRust(t *testing.T) {
	if !strings.Contains(strings.Join(Supported(), ","), "rust") {
		t.Errorf("rust backend is registered but Supported() = %v", Supported())
	}
}
