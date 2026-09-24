// Package deps maintains the reference dependency graph.
//
// Every graph node is one (item, environment) pair: an item referencing a
// target in the same environment contributes an edge env->env, while a
// cross-environment placeholder contributes env->targetEnv. The graph is a
// plain adjacency structure rebuilt from the persisted raw edges; it answers
// the four questions the platform needs:
//
//   - out(n) / in(n): forward and reverse edges, for the console views;
//   - CycleReachable(n): the ordered cycle a write would close;
//   - ReverseClosure(n): the transitive blast radius of changing n.
//
// Cycle search is iterative DFS with an explicit stack frame, so pathological
// graphs can neither recurse infinitely nor overflow the stack.
package deps

import "sort"

// Node is one vertex of the graph: an item in one environment.
type Node struct {
	ItemID string
	Env    string
}

func (n Node) key() string { return n.ItemID + "\x00" + n.Env }

// Edge is one directed reference: From -> To. TargetKey/TargetBare carry the
// placeholder as written so the graph can be rebuilt and rendered; TargetID
// is the concrete item the placeholder resolves to.
type Edge struct {
	From       Node
	To         Node
	TargetKey  string
	TargetBare bool
}

// Cycle describes a directed cycle in reference path order: Path starts at the
// repeated node, visits every other node once and ends back at it.
type Cycle struct {
	Path []Node
}

func (c Cycle) String(pathLabel func(Node) string) string {
	if len(c.Path) == 0 {
		return ""
	}
	parts := make([]string, 0, len(c.Path)+1)
	for _, n := range c.Path {
		parts = append(parts, pathLabel(n))
	}
	if len(c.Path) > 0 {
		parts = append(parts, pathLabel(c.Path[0]))
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " -> "
		}
		out += p
	}
	return out
}

// Graph is an immutable adjacency view over a set of edges.
type Graph struct {
	out  map[string][]Node
	in   map[string][]Node
	nodes map[string]Node
}

// Build constructs a graph from concrete edges. Duplicate edges collapse.
func Build(edges []Edge) *Graph {
	g := &Graph{
		out:   map[string][]Node{},
		in:    map[string][]Node{},
		nodes: map[string]Node{},
	}
	seen := map[string]struct{}{}
	addNode := func(n Node) {
		k := n.key()
		if _, ok := g.nodes[k]; !ok {
			g.nodes[k] = n
		}
	}
	for _, e := range edges {
		addNode(e.From)
		addNode(e.To)
		ek := e.From.key() + ">" + e.To.key()
		if _, dup := seen[ek]; dup {
			continue
		}
		seen[ek] = struct{}{}
		g.out[e.From.key()] = append(g.out[e.From.key()], e.To)
		g.in[e.To.key()] = append(g.in[e.To.key()], e.From)
	}
	for k := range g.out {
		sortNodes(g.out[k])
	}
	for k := range g.in {
		sortNodes(g.in[k])
	}
	return g
}

func sortNodes(ns []Node) {
	sort.Slice(ns, func(i, j int) bool {
		if ns[i].ItemID != ns[j].ItemID {
			return ns[i].ItemID < ns[j].ItemID
		}
		return ns[i].Env < ns[j].Env
	})
}

// Out returns the items/environments this node references directly.
func (g *Graph) Out(n Node) []Node { return g.out[n.key()] }

// In returns the items/environments directly referencing this node.
func (g *Graph) In(n Node) []Node { return g.in[n.key()] }

// Has reports whether the graph contains a node.
func (g *Graph) Has(n Node) bool { _, ok := g.nodes[n.key()]; return ok }

// ReverseClosure returns the transitive set of nodes that (directly or
// indirectly) reference n, including n itself. It is the blast radius of
// changing n.
func (g *Graph) ReverseClosure(n Node) []Node {
	visited := map[string]struct{}{n.key(): {}}
	stack := []Node{n}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, pred := range g.in[cur.key()] {
			if _, ok := visited[pred.key()]; ok {
				continue
			}
			visited[pred.key()] = struct{}{}
			stack = append(stack, pred)
		}
	}
	out := make([]Node, 0, len(visited))
	for k := range visited {
		out = append(out, g.nodes[k])
	}
	sortNodes(out)
	return out
}

// FindCycle returns any cycle reachable by following outgoing edges from
// start (start itself is usually a freshly written node already present in
// the graph). nil means the graph reachable from start is acyclic.
func (g *Graph) FindCycle(start Node) *Cycle {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	parent := map[string]Node{}

	type frame struct {
		node Node
		next int
	}
	stack := []frame{{node: start}}
	color[start.key()] = gray

	for len(stack) > 0 {
		f := &stack[len(stack)-1]
		neighbors := g.out[f.node.key()]
		if f.next >= len(neighbors) {
			color[f.node.key()] = black
			stack = stack[:len(stack)-1]
			continue
		}
		next := neighbors[f.next]
		f.next++
		switch color[next.key()] {
		case black:
			continue
		case gray:
			// Back edge: reconstruct the cycle next -> ... -> f.node -> next
			path := []Node{next}
			cur := f.node
			for cur.key() != next.key() {
				path = append(path, cur)
				p, ok := parent[cur.key()]
				if !ok {
					break
				}
				cur = p
			}
			// reverse so the order runs from next back to next
			for i, j := 1, len(path)-1; i < j; i, j = i+1, j-1 {
				path[i], path[j] = path[j], path[i]
			}
			path = append(path, next)
			return &Cycle{Path: path}
		default:
			color[next.key()] = gray
			parent[next.key()] = f.node
			stack = append(stack, frame{node: next})
		}
	}
	return nil
}

// DetectCycleAny checks the whole graph and returns the first cycle found,
// deterministically starting from the smallest node. It is the defensive
// read-time check used when a graph that was never write-gated is resolved.
func (g *Graph) DetectCycleAny() *Cycle {
	keys := make([]string, 0, len(g.nodes))
	for k := range g.nodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if c := g.FindCycle(g.nodes[k]); c != nil {
			return c
		}
	}
	return nil
}
