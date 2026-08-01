package architext

import (
	"reflect"
	"regexp"
	"testing"

	"github.com/robot-accomplice/magma/internal/contract"
)

func TestAssignIDsStableAndSlugged(t *testing.T) {
	nodes := []contract.Node{
		{ID: 0, Pkg: "internal/backend", Symbol: "BuildGraph", File: "internal/backend/golang.go", Line: 44},
	}
	got := assignIDs(nodes)
	if got[0] != "internal-backend-buildgraph" {
		t.Errorf("id = %q, want internal-backend-buildgraph", got[0])
	}
	// Determinism: same input, same output.
	if again := assignIDs(nodes); again[0] != got[0] {
		t.Error("assignIDs is not deterministic")
	}
}

func TestAssignIDsDisambiguatesCollisions(t *testing.T) {
	// A value method T.M and a pointer method T.M in the same package slug the
	// same; disambiguate deterministically by (file, line) order.
	nodes := []contract.Node{
		{ID: 0, Pkg: "p", Symbol: "T.M", File: "b.go", Line: 20},
		{ID: 1, Pkg: "p", Symbol: "T.M", File: "a.go", Line: 10},
	}
	got := assignIDs(nodes)
	if got[1] != "p-t-m" || got[0] != "p-t-m-2" {
		t.Errorf("collision resolution = %v, want id1=p-t-m (a.go:10 first), id0=p-t-m-2", got)
	}
}

// TestAssignIDsCollisionTiebreakIsInputOrderIndependent pins the determinism
// hole found in review: when colliding nodes (same base slug) also share
// (File, Line), the old comparator returned false both ways, so sort.Slice's
// unstable pdqsort broke the tie by input/partition order rather than any
// property of the nodes. That makes id assignment depend on the order `nodes`
// happened to be passed to assignIDs — nondeterministic across calls in
// practice, since map/slice construction order upstream is not guaranteed.
// The fix adds Node.ID (unique, assigned independently of assignIDs's input
// order) as the final tiebreak, making the comparator a total order. This test
// builds a group of 3 nodes tied on (File, Line) and asserts assignIDs returns
// the identical map across several distinct input permutations. It must FAIL
// against the (File, Line)-only comparator and PASS with the (File, Line, ID)
// fix.
func TestSlugAlwaysStartsWithLetter(t *testing.T) {
	re := regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	cases := map[string]string{
		"internal/backend": "internal-backend", // unchanged: already letter-leading
		"2048game":         "x-2048game",       // digit-leading -> prefixed
		"123":              "x-123",
		"_Foo":             "foo", // underscore trimmed, then letter-leading
		"":                 "x",   // empty -> "x"
	}
	for in, want := range cases {
		got := slug(in)
		if got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
		if !re.MatchString(got) {
			t.Errorf("slug(%q) = %q does not match Architext id pattern", in, got)
		}
	}
}

func TestAssignIDsCollisionTiebreakIsInputOrderIndependent(t *testing.T) {
	tied := contract.Node{Pkg: "p", Symbol: "T.M", File: "a.go", Line: 10}
	n5 := tied
	n5.ID = 5
	n2 := tied
	n2.ID = 2
	n9 := tied
	n9.ID = 9

	orderings := [][]contract.Node{
		{n5, n2, n9},
		{n9, n2, n5},
		{n2, n9, n5},
		{n9, n5, n2},
		{n2, n5, n9},
	}

	var want map[int]string
	for i, nodes := range orderings {
		// Defensive copy: assignIDs's sort.Slice mutates its input slice.
		input := append([]contract.Node(nil), nodes...)
		got := assignIDs(input)
		if i == 0 {
			want = got
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ordering %d %v: assignIDs = %v, want %v (must match ordering 0's result — id assignment must not depend on input order)", i, nodes, got, want)
		}
	}
}
