package main

import "testing"

// TestOnlyTest is the sole caller of OnlyTest, and only from test code — so
// OnlyTest is Reachable (with tests) but not ProdReachable.
func TestOnlyTest(t *testing.T) {
	OnlyTest()
}
