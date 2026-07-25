package architext

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// FileName is the fixed artifact name written into each destination directory.
const FileName = "code-graph.json"

// Write marshals cg once (indented, newline-terminated) and writes the identical
// bytes to every destination path, creating parent directories as needed. Each
// dest is a full file path (…/code-graph.json). Marshalling once guarantees the
// dual-written copies are byte-identical.
func Write(cg CodeGraph, dests ...string) error {
	b, err := json.MarshalIndent(cg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling code-graph: %w", err)
	}
	b = append(b, '\n')
	for _, dest := range dests {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", filepath.Dir(dest), err)
		}
		if err := os.WriteFile(dest, b, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", dest, err)
		}
	}
	return nil
}
