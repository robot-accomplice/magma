package main

import "fmt"

// Speaker has EXACTLY ONE implementor in this module -> the family-E finding.
type Speaker interface{ Speak() string }

type Dog struct{}

func (Dog) Speak() string { return "woof" }

// Multi has TWO implementors -> not a single-implementation interface.
type Multi interface{ Do() }

type A struct{}

func (A) Do() {}

type B struct{}

func (B) Do() {}

// Orphan has ZERO implementors. A different finding (a dead abstraction), not
// this one — see InterfacesView's doc comment.
type Orphan interface{ Never() }

// Empty is satisfied by every type, so "one implementor" is meaningless.
type Empty interface{}

// Number is a generic CONSTRAINT: a type set, not a method set. Nothing
// "implements" it in the sense this file reports.
type Number interface{ ~int | ~float64 }

func Sum[T Number](xs []T) T {
	var t T
	for _, x := range xs {
		t += x
	}
	return t
}

// PtrOnly is implemented only via a POINTER receiver, which types.Implements
// sees on *Impl and not on Impl.
type PtrOnly interface{ Ping() }

type Impl struct{}

func (i *Impl) Ping() {}

func main() {
	var s Speaker = Dog{}
	fmt.Println(s.Speak(), Sum([]int{1}), len([]Multi{A{}, B{}}))
	(&Impl{}).Ping()
}
