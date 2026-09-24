package graph

import (
	"reflect"
	"testing"
)

func TestCheckCycleNoCycle(t *testing.T) {
	adj := Adjacency{
		"A": {"B", "C"},
		"B": {"C"},
		"C": {"D"},
		"D": {},
	}
	if err := CheckCycle(adj, "A"); err != nil {
		t.Fatalf("unexpected cycle: %v", err)
	}
}

func TestCheckCycleThreeNode(t *testing.T) {
	adj := Adjacency{
		"A": {"B"},
		"B": {"C"},
		"C": {"A"},
	}
	err := CheckCycle(adj, "A")
	if err == nil {
		t.Fatal("expected cycle")
	}
	if got, want := err.Path, []string{"A", "B", "C", "A"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cycle path = %v, want %v", got, want)
	}
}

func TestCheckCycleSelfReference(t *testing.T) {
	err := CheckCycle(Adjacency{"A": {"A"}}, "A")
	if err == nil {
		t.Fatal("expected self cycle")
	}
	if got, want := err.Path, []string{"A", "A"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("self cycle path = %v, want %v", got, want)
	}
}

// The iterative DFS must walk a long chain without stack growth proportional
// to recursion (guards against the naive recursive implementation blowing the
// stack on pathological inputs).
func TestCheckCycleDeepChain(t *testing.T) {
	adj := Adjacency{}
	for i := 0; i < 5000; i++ {
		adj[node(i)] = []string{node(i + 1)}
	}
	adj[node(5000)] = nil
	if err := CheckCycle(adj, node(0)); err != nil {
		t.Fatalf("deep acyclic chain: %v", err)
	}
	// close it at the tail
	adj[node(5000)] = []string{node(4000)}
	err := CheckCycle(adj, node(0))
	if err == nil {
		t.Fatal("expected deep-tail cycle")
	}
	if len(err.Path) < 3 || err.Path[0] != node(4000) || err.Path[len(err.Path)-1] != node(4000) {
		t.Fatalf("deep cycle chain wrong: head=%q tail=%q len=%d",
			err.Path[0], err.Path[len(err.Path)-1], len(err.Path))
	}
}

func TestReverseClosure(t *testing.T) {
	// A->B, B->C, D->C : dependents of C are B, A, D.
	adj := Adjacency{
		"A": {"B"},
		"B": {"C"},
		"D": {"C"},
	}
	got := ReverseClosure(adj, "C")
	want := []string{"A", "B", "D"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("closure = %v, want %v", got, want)
	}
}

func TestReverseClosureWithCycle(t *testing.T) {
	// Cycle X<->Y, both depend on C. Closure must terminate and not repeat.
	adj := Adjacency{
		"X": {"Y"},
		"Y": {"X", "C"},
	}
	got := ReverseClosure(adj, "C")
	want := []string{"X", "Y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cyclic closure = %v, want %v", got, want)
	}
}

func TestOutboundDedup(t *testing.T) {
	got := Outbound(Adjacency{"A": {"C", "B", "C"}}, "A")
	want := []string{"B", "C"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outbound = %v, want %v", got, want)
	}
}

func node(i int) string {
	return string(rune('a'+i/26)) + string(rune('a'+i%26))
}
