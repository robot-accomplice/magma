// Package detect determines a repository's dominant language from its build
// manifest. A manifest is a far stronger signal than counting source files: a
// repo with a go.mod IS a Go project regardless of how many .ts files a tool
// directory contains.
package detect

import (
	"os"
	"path/filepath"
)

// Lang is a language token; it selects the backend and is never faked.
type Lang string

const (
	Go      Lang = "go"
	Rust    Lang = "rust"
	Node    Lang = "node" // Node / React-TS / Next — one backend covers .js, .ts, .tsx
	Kotlin  Lang = "kotlin"
	Java    Lang = "java"
	Unknown Lang = "none"
)

// manifest maps a marker file to the language it proves. Order matters: earlier
// entries win, so a JVM repo that ships both a Kotlin DSL build and .java files
// resolves to Kotlin before Java. Unambiguous markers (go.mod, Cargo.toml) are
// order-independent.
var manifests = []struct {
	file string
	lang Lang
}{
	{"go.mod", Go},
	{"Cargo.toml", Rust},
	{"build.gradle.kts", Kotlin},
	{"settings.gradle.kts", Kotlin},
	{"package.json", Node},
	{"pom.xml", Java},
	{"build.gradle", Java}, // Groovy-DSL Gradle: assume Java unless a .kts above matched
}

// Detect returns the dominant language of the repo rooted at dir, or Unknown
// when no supported manifest is present.
func Detect(dir string) Lang {
	for _, m := range manifests {
		if fileExists(filepath.Join(dir, m.file)) {
			return m.lang
		}
	}
	return Unknown
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
