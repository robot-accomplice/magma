package main

// Add returns the sum of a and b.
func Add(a int, b int) int { return a + b }

// sq returns n squared.
func sq(n int) int { return n * n }

func main() {
	_ = Add(sq(2), sq(3))
}
