package detect

import (
	"os"
	"path/filepath"
	"testing"
)

// Each manifest marker resolves to its language.
func TestDetectByManifest(t *testing.T) {
	cases := []struct {
		file string
		want Lang
	}{
		{"go.mod", Go},
		{"Cargo.toml", Rust},
		{"build.gradle.kts", Kotlin},
		{"settings.gradle.kts", Kotlin},
		{"package.json", Node},
		{"pom.xml", Java},
		{"build.gradle", Java},
	}
	for _, c := range cases {
		dir := t.TempDir()
		writeFile(t, dir, c.file)
		if got := Detect(dir); got != c.want {
			t.Errorf("%s => %q, want %q", c.file, got, c.want)
		}
	}
}

// A repo with no supported manifest is Unknown — never guessed.
func TestDetectUnknown(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md") // present, but not a build manifest
	if got := Detect(dir); got != Unknown {
		t.Errorf("no manifest => %q, want %q", got, Unknown)
	}
}

// A JVM repo shipping BOTH a Kotlin DSL build and a Groovy build.gradle must
// resolve to Kotlin: manifest order is the tie-break, and this is exactly the
// ambiguous case the ordering exists for.
func TestDetectKotlinBeatsJavaWhenBothPresent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "build.gradle")     // Java marker, later in the list
	writeFile(t, dir, "build.gradle.kts") // Kotlin marker, earlier — must win
	if got := Detect(dir); got != Kotlin {
		t.Errorf("both gradle files => %q, want %q (Kotlin wins by order)", got, Kotlin)
	}
}

// A directory named like a manifest must not be mistaken for the manifest file.
func TestDetectIgnoresDirectoryNamedLikeManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "go.mod"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Detect(dir); got != Unknown {
		t.Errorf("go.mod directory => %q, want %q (only files count)", got, Unknown)
	}
}

func writeFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}
