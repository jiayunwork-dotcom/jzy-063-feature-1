// Package graph implements the directed dependency algorithms used by the
// reference layer: write-time cycle detection and the reverse transitive
// closure ("who is affected if I change this key").
//
// The graph itself is node-agnostic: nodes are string ids supplied by the
// service (item id + environment), edges are "references" links. Keeping the
// algorithms free of storage/domain concerns makes them exhaustively
// testable in isolation.
package graph

import "sort"

// Directed adjacency: out[node] = nodes it references.
type Adjacency map[string][]string

// CycleError reports a closed reference chain. Path is the ordered chain,
// starting at the revisited node and ending back on it, e.g.
//
//	[A, B, C, A]
//
// for the cycle A -> B -> C -> A. A self reference is [A, A].
type CycleError struct {
	Path []string
}

func (e *CycleError) Error() string {
	return "reference cycle: " + join(e.Path)
}

func join(nodes []string) string {
	out := ""
	for i, n := range nodes {
		if i > 0 {
			out += " -> "
		}
		out += n
	}
	return out
}

// CheckCycle runs an iterative DFS from start over out, reporting the first
// back edge as a CycleError. The iterative walk cannot overflow the stack on
// long or cyclic chains.
func CheckCycle(out Adjacency, start string) *CycleError {
	const (
		white = 0
		gray  = 1 // on the current DFS path
		black = 2 // fully explored
	)
	color := map[string]int{start: gray}
	parent := map[string]string{}

	// Explicit stack of (node, next-edge-index).
	type frame struct {
		node string
		i    int
	}
	stack := []frame{{node: start}}

	for len(stack) > 0 {
		top := &stack[len(stack)-1]
		neighbors := out[top.node]
		if top.i >= len(neighbors) {
			color[top.node] = black
			stack = stack[:len(stack)-1]
			continue
		}
		next := neighbors[top.i]
		top.i++
		switch color[next] {
		case gray:
			return &CycleError{Path: cyclePath(parent, top.node, next)}
		case white:
			color[next] = gray
			parent[next] = top.node
			stack = append(stack, frame{node: next})
		}
	}
	return nil
}

// cyclePath reconstructs the closed path start...from -> backTo(=start of
// cycle), always beginning at the first repeated node and ending on it.
func cyclePath(parent map[string]string, from, backTo string) []string {
	// Walk from `from` up to `backTo`.
	path := []string{}
	cur := from
	for {
		path = append(path, cur)
		if cur == backTo {
			break
		}
		p, ok := parent[cur]
		if !ok {
			break
		}
		cur = p
	}
	// parent links point back to the DFS root; reverse to present forward.
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	path = append(path, backTo)
	return path
}

// ReverseClosure returns every node that depends on start through zero or
// more reverse edges (start itself excluded). Iterative BFS, safe on cyclic
// graphs: a node is visited once regardless of how many paths reach it.
func ReverseClosure(out Adjacency, starts ...string) []string {
	rev := reverse(out)
	seen := map[string]struct{}{}
	for _, s := range starts {
		seen[s] = struct{}{}
	}
	var queue []string
	queue = append(queue, starts...)
	affected := []string{}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, pred := range rev[n] {
			if _, ok := seen[pred]; ok {
				continue
			}
			seen[pred] = struct{}{}
			affected = append(affected, pred)
			queue = append(queue, pred)
		}
	}
	sort.Strings(affected)
	return affected
}

// Outbound returns the direct successors of a node, sorted and deduplicated.
func Outbound(out Adjacency, node string) []string {
	seen := map[string]struct{}{}
	var res []string
	for _, n := range out[node] {
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		res = append(res, n)
	}
	sort.Strings(res)
	return res
}

func reverse(out Adjacency) Adjacency {
	rev := Adjacency{}
	for n, succs := range out {
		if _, ok := rev[n]; !ok {
			rev[n] = nil
		}
		for _, m := range succs {
			rev[m] = append(rev[m], n)
		}
	}
	return rev
}
