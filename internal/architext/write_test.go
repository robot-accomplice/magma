package architext

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteIsDeterministicAndDual(t *testing.T) {
	cg := Emit(sampleGraph())
	d1 := filepath.Join(t.TempDir(), "repo", "docs", "architext", "data", FileName)
	d2 := filepath.Join(t.TempDir(), "vault", ".magma", FileName)
	if err := Write(cg, d1, d2); err != nil {
		t.Fatal(err)
	}
	b1, err := os.ReadFile(d1)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(d2)
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Error("dual-written files differ")
	}
	if b1[len(b1)-1] != '\n' {
		t.Error("output must end in a newline")
	}
	// Determinism: emitting + marshalling the same graph again is byte-identical.
	d3 := filepath.Join(t.TempDir(), FileName)
	if err := Write(Emit(sampleGraph()), d3); err != nil {
		t.Fatal(err)
	}
	b3, _ := os.ReadFile(d3)
	if string(b3) != string(b1) {
		t.Error("same graph produced different bytes across runs")
	}
}
