package main

func main() {
	Live()
}

// Live is called from main, so it is production-reachable. It calls a method to
// give the graph a static call edge and a method node.
func Live() {
	var t T
	t.M()
}

// T carries one reached method (M) and one dead method (P). The pointer receiver
// on P exercises prettyName's *types.Pointer branch.
type T struct{}

func (t T) M()  {}
func (t *T) P() {}

// Dead is never called from anywhere: dead code.
func Dead() {}

// OnlyTest is called only from a _test.go file: production code kept reachable
// solely by tests.
func OnlyTest() {}
