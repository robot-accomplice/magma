package backend

import (
	"go/ast"
	"go/types"
	"sort"

	"golang.org/x/tools/go/packages"

	"github.com/robot-accomplice/magma/internal/contract"
)

// collectInterfaces finds the module's own interfaces with exactly ONE
// module-local implementor — the seed for the audit gate's family E, where an
// abstraction with a single concrete type may not be earning its indirection.
//
// This is the one thing the call graph cannot answer. Nodes and edges record
// what calls what; implementing an interface is a type-level relation with no
// call site at all. The type information is already loaded — `packages.Load`
// runs with LoadAllSyntax — and was previously discarded.
//
// Four exclusions, each a case in testdata/ifacemod rather than a claim here:
//
//   - EMPTY interfaces. Every type satisfies `interface{}`, so "one
//     implementor" is not a meaningful count.
//   - GENERIC CONSTRAINTS. `interface{ ~int | ~float64 }` is a type set, not a
//     method set; nothing implements it in the sense being reported.
//     `IsMethodSet` is the exact question, so it is the exact test.
//   - ZERO implementors. A dead abstraction is a DIFFERENT finding with a
//     different remedy. The consuming gate treats every row in a file
//     identically, so folding both in would make one row mean two things.
//   - OUT-OF-MODULE types, on both sides. An interface implemented once here
//     but also by a dependency is not single-implementation, and counting
//     implementors magma cannot see would make the number a guess.
//
// Pointer receivers count: `func (i *Impl) Ping()` puts the method on `*Impl`,
// not `Impl`, so both are tested — checking only the value type would silently
// miss the most common way Go code implements an interface.
func collectInterfaces(repo, modPath string, initial []*packages.Package) []contract.Row {
	type iface struct {
		obj  *types.TypeName
		it   *types.Interface
		file string
		line int
	}
	var ifaces []iface
	var concrete []*types.Named

	packages.Visit(initial, nil, func(p *packages.Package) {
		if !inModule(p.PkgPath, modPath) {
			return
		}
		for _, file := range p.Syntax {
			for _, d := range file.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range gd.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					obj, _ := p.TypesInfo.Defs[ts.Name].(*types.TypeName)
					if obj == nil || obj.Type() == nil {
						continue
					}
					named, ok := obj.Type().(*types.Named)
					if !ok {
						continue
					}
					if it, ok := named.Underlying().(*types.Interface); ok {
						// Empty interfaces and generic constraints are excluded
						// here rather than filtered later, so they never reach
						// the implementor count at all.
						if it.NumMethods() == 0 || !it.IsMethodSet() {
							continue
						}
						posn := p.Fset.Position(obj.Pos())
						ifaces = append(ifaces, iface{
							obj:  obj,
							it:   it,
							file: rel(repo, posn.Filename),
							line: posn.Line,
						})
						continue
					}
					// A concrete module-local type, stored ONCE. The value and
					// pointer forms are tested together below rather than
					// collected separately: a value-receiver method set belongs
					// to both T and *T, so collecting both double-counts a
					// single implementor and hides the very finding this
					// produces. Caught by testdata/ifacemod's Speaker/Dog.
					concrete = append(concrete, named)
				}
			}
		}
	})

	// NON-NIL from the start. nil means "this backend cannot answer" and drives
	// a refusal in InterfacesView; an empty slice means "answered, and there are
	// none". Conflating them would report a clean module as unanalysed — the
	// same null-versus-[] distinction Signature and Limitations enforce, and the
	// same failure this project exists to avoid: absence reading as a result.
	rows := []contract.Row{}
	for _, f := range ifaces {
		n := 0
		for _, c := range concrete {
			// One implementor, tested in both forms: a pointer-receiver method
			// set lives on *T only, while a value-receiver one lives on both.
			// Testing only T would miss the commonest way Go implements an
			// interface; counting T and *T separately would double-count.
			if types.Implements(c, f.it) || types.Implements(types.NewPointer(c), f.it) {
				n++
				if n > 1 {
					break // two is enough to know it is not single-implementation
				}
			}
		}
		if n == 1 {
			rows = append(rows, contract.Row{Symbol: f.obj.Name(), File: f.file, Line: f.line})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].File != rows[j].File {
			return rows[i].File < rows[j].File
		}
		return rows[i].Line < rows[j].Line
	})
	return rows
}
