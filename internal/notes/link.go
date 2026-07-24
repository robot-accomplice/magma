package notes

import (
	"path"
	"strings"

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

// wikiTarget is the [[...]] target (no .md), e.g. "internal/backend/BuildGraph",
// resolved by Obsidian via path suffix.
func wikiTarget(n contract.Node, module string) string {
	return path.Join(relPkg(n.Pkg, module), n.Symbol)
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
