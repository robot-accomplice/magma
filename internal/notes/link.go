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
