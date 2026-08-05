package notes

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/robot-accomplice/magma/internal/contract"
)

// relPkg makes an import path module-relative ("mod/internal/x" -> "internal/x";
// "mod" -> "").
func relPkg(pkg, module string) string {
	if pkg == module {
		return ""
	}
	return strings.TrimPrefix(pkg, module+"/")
}

// maxStem is the per-component byte budget for a note path. Filesystems cap a
// single name at 255 BYTES (not runes — a multibyte symbol costs more than it
// looks), and the last component also carries ".md", so the budget reserves it.
const maxStem = 255 - len(".md")

// hashLen is how much of the symbol digest is appended when a component had to
// be altered. 8 hex chars is 32 bits: ample against accidental collision within
// one repo's symbol set, and short enough to keep the name readable.
const hashLen = 8

// illegalInName are the characters Windows forbids in a filename, plus the
// path separator. Rust carries them STRUCTURALLY, not occasionally: on
// roboticus-rust, 3,383 of 10,921 symbols contain ':' (every `Type::method`)
// and 2,104 contain '<' and '>' (every `<T as Trait>::method`). magma ships a
// Windows binary, so before this fix roughly 31% of notes could not be written
// there for any Rust repo. The Go backend emits none of these, which is why it
// went unnoticed until Rust shipped.
const illegalInName = `<>:"|?*\/`

// reservedNames are Windows device names, which cannot be used as a filename
// even with an extension. A Go function called `Con` or `Nul` is entirely
// plausible, and `nul.md` is unopenable on Windows.
var reservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// safeComponent makes one path component writable on every platform magma
// ships for, and bounds its length, WITHOUT letting two distinct symbols land
// on the same file.
//
// Both sanitising and truncating destroy information, so either one alone
// would let `A::b` and `A<b` — or two generics differing only past the cut —
// silently become one note. A silent merge is worse than the "file name too
// long" abort it replaces: the abort at least stopped. So whenever the name is
// altered AT ALL, a digest of the ORIGINAL symbol is appended, which restores
// the distinction the transformation removed.
//
// Readability deliberately loses to correctness here: the result is not pretty
// for a heavily-generic symbol. The note's own heading carries the real symbol,
// so nothing is lost to a reader who opens it.
func safeComponent(s string) string {
	// The substitutions are chosen so a Rust name still READS: `::` becomes
	// `..` rather than `--`, and the angle brackets of `<T as Trait>::f` are
	// dropped rather than turned into noise. These are still lossy, which is
	// what the digest below is for — but a map a human browses should not be
	// gratuitously unreadable on the way to being correct.
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r == ':':
			return '.'
		case r == '<' || r == '>' || r == '"':
			return -1 // dropped
		case r < 0x20 || strings.ContainsRune(illegalInName, r):
			return '-'
		}
		return r
	}, s)
	// Trailing dots and spaces are silently stripped by Windows, which would
	// make two names collide after the fact.
	trimmed := strings.TrimRight(cleaned, ". ")
	if trimmed == "" {
		trimmed = cleaned
	}
	unchanged := trimmed == s && len(s) <= maxStem && !reservedNames[strings.ToLower(s)]
	if unchanged {
		return s // fast path: Go symbols are already safe and short
	}
	sum := sha256.Sum256([]byte(s))
	suffix := "-" + hex.EncodeToString(sum[:])[:hashLen]
	keep := maxStem - len(suffix)
	if len(trimmed) > keep {
		// Cut on a rune boundary so the name stays valid UTF-8.
		for keep > 0 && !utf8.RuneStart(trimmed[keep]) {
			keep--
		}
		trimmed = trimmed[:keep]
	}
	return trimmed + suffix
}

// wikiTarget is the [[...]] target (no .md), e.g. "internal/backend/BuildGraph",
// resolved by Obsidian via path suffix. Every component is passed through
// safeComponent, including the package segments — a Rust crate path is itself
// `crate::module::sub` and carries the same illegal characters.
func wikiTarget(n contract.Node, module string) string {
	var parts []string
	if p := relPkg(n.Pkg, module); p != "" {
		for _, seg := range strings.Split(p, "/") {
			parts = append(parts, safeComponent(seg))
		}
	}
	parts = append(parts, safeComponent(n.Symbol))
	return path.Join(parts...)
}

// notePath is the folder-relative path of a node's note, e.g.
// "nodes/internal/backend/BuildGraph.md". module is the graph's Module.
func notePath(n contract.Node, module string) string {
	return "nodes/" + wikiTarget(n, module) + ".md"
}

// projectTag is the per-project Obsidian tag stamped on every note this
// package writes for one map. A vault holds several maps' notes plus other
// wiki notes in one graph; this tag lets a reader filter the graph view down
// to a single project's call graph (tag:#magma/project/<folder>).
func projectTag(folderName string) string {
	return "magma/project/" + sanitizeTag(folderName)
}

// sanitizeTag replaces every character not safe in an Obsidian tag segment
// ([A-Za-z0-9_-]) with "-".
func sanitizeTag(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}
