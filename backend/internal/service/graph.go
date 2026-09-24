package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"configcenter/internal/domain"
	"configcenter/internal/graph"
	"configcenter/internal/ref"
	"configcenter/internal/store"
)

// Reference error codes surfaced through the API.
const (
	CodeRefSyntax      = "ref_syntax"
	CodeRefMissing     = "ref_missing_target"
	CodeRefCycle       = "ref_cycle"
	CodeRefNoValue     = "ref_no_value"
	CodeRefCrossTenant = "ref_cross_tenant"
)

// RefError is a structural reference failure. It is used both at write time
// (commit is rejected) and at read time (effective resolution fails loudly
// instead of leaking an unresolved placeholder).
type RefError struct {
	Code    string   `json:"code"`
	Message string   `json:"error"`
	Source  string   `json:"source,omitempty"` // ns/group/key[env]
	Target  string   `json:"target,omitempty"`
	Cycle   []string `json:"cycle,omitempty"` // ordered closed chain
}

func (e *RefError) Error() string { return e.Message }

// nodeID identifies one vertex of the dependency graph: one item in one
// environment. References with ?env= therefore point at another vertex.
func nodeID(itemID, env string) string { return itemID + "#" + env }

// coordLabel renders an item coordinate for messages and the console.
func coordLabel(item *domain.Item, env string) string {
	if env == "" {
		return fmt.Sprintf("%s/%s/%s", item.NamespaceID, item.GroupID, item.Key)
	}
	return fmt.Sprintf("%s/%s/%s[%s]", item.NamespaceID, item.GroupID, item.Key, env)
}

// targetLabel renders a (possibly bare) target as authored.
func targetLabel(t domain.RefTarget, env string) string {
	body := t.Key
	if t.Qualified() {
		body = t.NamespaceID + "/" + t.GroupID + "/" + t.Key
	}
	if e := t.Env; e != "" {
		body += "[" + e + "]"
	}
	_ = env
	return body
}

// extractEdges parses a raw value into persisted placeholder edges.
func extractEdges(tenantID, itemID, env, raw string) ([]domain.RefEdge, error) {
	toks, err := ref.Scan(raw)
	if err != nil {
		var se *ref.SyntaxError
		if asRefSyntax(err, &se) {
			return nil, &RefError{
				Code:    CodeRefSyntax,
				Message: fmt.Sprintf("占位符语法错误（位置 %d）：%s", se.Pos, se.Message),
			}
		}
		return nil, err
	}
	edges := make([]domain.RefEdge, 0)
	seq := 0
	for _, t := range toks {
		if !t.Placeholder {
			continue
		}
		e := domain.RefEdge{TenantID: tenantID, ItemID: itemID, Env: env, Seq: seq, Raw: t.Raw}
		if parts := strings.Split(t.Target, "/"); len(parts) == 3 {
			e.Target = domain.RefTarget{NamespaceID: parts[0], GroupID: parts[1], Key: parts[2], Env: t.Env}
		} else {
			e.Target = domain.RefTarget{Key: t.Target, Env: t.Env}
		}
		edges = append(edges, e)
		seq++
	}
	return edges, nil
}

func asRefSyntax(err error, se **ref.SyntaxError) bool {
	x, ok := err.(*ref.SyntaxError)
	if ok {
		*se = x
	}
	return ok
}

// graphView is the materialized adjacency plus item lookup for one tenant.
type graphView struct {
	adj   graph.Adjacency
	items map[string]*domain.Item // node item id -> item (shared across env nodes)
}

// loadGraph builds the current dependency graph of a tenant (all envs).
func (s *Service) loadGraph(ctx context.Context, tenantID string) (*graphView, error) {
	edges, err := s.store.AllItemRefs(ctx, tenantID, "")
	if err != nil {
		return nil, err
	}
	gv := &graphView{adj: graph.Adjacency{}, items: map[string]*domain.Item{}}
	itemCache := map[string]*domain.Item{}
	getItem := func(id string) (*domain.Item, error) {
		if it, ok := itemCache[id]; ok {
			return it, nil
		}
		it, err := s.store.GetItem(ctx, tenantID, id)
		if err != nil {
			return nil, err
		}
		itemCache[id] = it
		return it, nil
	}
	// Resolve every coordinate to an item id, memoized.
	byCoord := map[string]*domain.Item{}
	resolveTarget := func(from *domain.Item, e domain.RefEdge) (*domain.Item, error) {
		key := ""
		if e.Target.Qualified() {
			key = "q:" + e.Target.NamespaceID + "/" + e.Target.GroupID + "/" + e.Target.Key
		} else {
			key = "b:" + from.ID + "/" + e.Target.Key
		}
		if t, ok := byCoord[key]; ok {
			return t, nil
		}
		t, err := s.resolveTargetItem(ctx, tenantID, from, e.Target)
		if err != nil {
			return nil, err
		}
		byCoord[key] = t
		return t, nil
	}
	for _, e := range edges {
		from := nodeID(e.ItemID, e.Env)
		gv.adj[from] = append(gv.adj[from], "") // ensure node exists
		fromItem, err := getItem(e.ItemID)
		if err != nil {
			return nil, err
		}
		toItem, err := resolveTarget(fromItem, e)
		if err != nil {
			if re, ok := err.(*RefError); ok {
				return nil, re
			}
			return nil, err
		}
		gv.items[e.ItemID] = fromItem
		gv.items[toItem.ID] = toItem
		gv.adj[from][len(gv.adj[from])-1] = nodeID(toItem.ID, envOr(e.Target.Env, e.Env))
	}
	return gv, nil
}

func (s *Service) cachedItem(ctx context.Context, m map[string]*domain.Item, id string) (*domain.Item, error) {
	if it, ok := m[id]; ok {
		return it, nil
	}
	it, err := s.store.GetItem(ctx, id, id)
	_ = it
	_ = err
	// GetItem needs tenant; callers always operate within one tenant, so use
	// the tenant-aware path via a small indirection below.
	return nil, fmt.Errorf("internal: use cachedItemT")
}

// resolveTargetItem maps a target coordinate to its strongest item.
//
// Qualified targets (@{ns/group/key}) point exactly at that container. Bare
// targets (@{key}) resolve inside the source item's business context, with
// the same strongest-first precedence as the three-layer merge:
// group > namespace defaults > public.
func (s *Service) resolveTargetItem(ctx context.Context, tenantID string, from *domain.Item, t domain.RefTarget) (*domain.Item, error) {
	if t.Qualified() {
		it, err := s.store.GetItemByKey(ctx, tenantID, t.NamespaceID, t.GroupID, t.Key)
		if err == store.ErrNotFound {
			return nil, &RefError{
				Code:    CodeRefMissing,
				Source:  coordLabel(from, ""),
				Target:  targetLabel(t, ""),
				Message: fmt.Sprintf("键 %s 引用了不存在的目标 %s", coordLabel(from, ""), targetLabel(t, "")),
			}
		}
		return it, err
	}
	for _, loc := range ownerContainers(from) {
		it, err := s.store.GetItemByKey(ctx, tenantID, loc[0], loc[1], t.Key)
		if err == nil {
			return it, nil
		}
		if err != store.ErrNotFound {
			return nil, err
		}
	}
	return nil, &RefError{
		Code:    CodeRefMissing,
		Source:  coordLabel(from, ""),
		Target:  targetLabel(t, ""),
		Message: fmt.Sprintf("键 %s 引用了不存在的目标 %s", coordLabel(from, ""), targetLabel(t, "")),
	}
}

// ownerContainers lists strongest-first merge containers for a bare
// reference authored in item `from`.
func ownerContainers(from *domain.Item) [][2]string {
	switch from.Layer {
	case domain.LayerGroup:
		return [][2]string{
			{from.NamespaceID, from.GroupID},
			{from.NamespaceID, domain.DefaultGroupID},
			{domain.PublicNamespaceID, domain.PublicNamespaceID},
		}
	case domain.LayerNamespace:
		return [][2]string{
			{from.NamespaceID, domain.DefaultGroupID},
			{domain.PublicNamespaceID, domain.PublicNamespaceID},
		}
	default:
		return [][2]string{{domain.PublicNamespaceID, domain.PublicNamespaceID}}
	}
}

// validateCommitRefs extracts the new value's edges and rejects the commit
// when a target does not exist or the resulting graph contains a cycle.
// It returns the edges to persist plus the post-commit affected node set.
func (s *Service) validateCommitRefs(ctx context.Context, item *domain.Item, env, value string) (
	edges []domain.RefEdge, affected []string, err error) {

	edges, err = extractEdges(item.TenantID, item.ID, env, value)
	if err != nil {
		return nil, nil, err
	}
	gv, err := s.loadGraph(ctx, item.TenantID)
	if err != nil {
		return nil, nil, err
	}
	self := nodeID(item.ID, env)
	// Tentative outgoing edges of this node.
	tentative := make([]string, 0, len(edges))
	for _, e := range edges {
		t, rerr := s.resolveTargetItem(ctx, item.TenantID, item, e.Target)
		if rerr != nil {
			if re, ok := rerr.(*RefError); ok {
				re.Source = coordLabel(item, env)
				return nil, nil, re
			}
			return nil, nil, rerr
		}
		tentative = append(tentative, nodeID(t.ID, envOr(e.Target.Env, env)))
	}
	gv.adj[self] = tentative
	if cyc := graph.CheckCycle(gv.adj, self); cyc != nil {
		return nil, nil, s.cycleError(ctx, gv, cyc.Path, item, env)
	}
	aff := graph.ReverseClosure(gv.adj, self)
	return edges, aff, nil
}

func (s *Service) cycleError(ctx context.Context, gv *graphView, path []string, item *domain.Item, env string) error {
	labels := make([]string, 0, len(path))
	itemCache := map[string]*domain.Item{}
	for _, n := range path {
		id, e := splitNode(n)
		it, ok := itemCache[id]
		if !ok {
			var err error
			it, err = s.store.GetItem(ctx, item.TenantID, id)
			if err != nil {
				labels = append(labels, n)
				itemCache[id] = nil
				continue
			}
			itemCache[id] = it
		}
		if it != nil {
			labels = append(labels, coordLabel(it, e))
		}
	}
	return &RefError{
		Code:    CodeRefCycle,
		Cycle:   labels,
		Message: "引用成环：" + strings.Join(labels, " → "),
	}
}

func envOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func splitNode(n string) (id, env string) {
	if i := strings.IndexByte(n, '#'); i >= 0 {
		return n[:i], n[i+1:]
	}
	return n, ""
}

// ImpactView is the blast-radius preview shown before editing a key.
type ImpactView struct {
	Node       string        `json:"node"`
	Direct     []RefNodeView `json:"direct"`
	Transitive []RefNodeView `json:"transitive"`
}

// RefNodeView describes one graph vertex for the console.
type RefNodeView struct {
	ItemID      string        `json:"item_id"`
	NamespaceID string        `json:"namespace_id"`
	GroupID     string        `json:"group_id"`
	Key         string        `json:"key"`
	Layer       domain.Layer  `json:"layer"`
	Format      domain.Format `json:"format"`
	Env         string        `json:"env"`
}

func nodeView(item *domain.Item, env string) RefNodeView {
	return RefNodeView{
		ItemID: item.ID, NamespaceID: item.NamespaceID, GroupID: item.GroupID,
		Key: item.Key, Layer: item.Layer, Format: item.Format, Env: env,
	}
}

// Impact computes the direct and transitive reverse closure of one item+env.
func (s *Service) Impact(ctx context.Context, tenantID, itemID, env string) (*ImpactView, error) {
	if _, err := s.store.GetItem(ctx, tenantID, itemID); err != nil {
		return nil, err
	}
	gv, err := s.loadGraph(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	self := nodeID(itemID, env)
	// Direct: predecessors that point at self.
	var direct []string
	for n, succs := range gv.adj {
		for _, m := range succs {
			if m == self {
				direct = append(direct, n)
				break
			}
		}
	}
	sort.Strings(direct)
	trans := graph.ReverseClosure(gv.adj, self)

	view := &ImpactView{Node: coordLabelMust(s, ctx, tenantID, itemID, env)}
	toViews := func(nodes []string) []RefNodeView {
		out := make([]RefNodeView, 0, len(nodes))
		for _, n := range nodes {
			id, e := splitNode(n)
			it, err := s.store.GetItem(ctx, tenantID, id)
			if err != nil {
				continue
			}
			out = append(out, nodeView(it, e))
		}
		return out
	}
	view.Direct = toViews(direct)
	view.Transitive = toViews(trans)
	return view, nil
}

func coordLabelMust(s *Service, ctx context.Context, tenantID, itemID, env string) string {
	it, err := s.store.GetItem(ctx, tenantID, itemID)
	if err != nil {
		return itemID
	}
	return coordLabel(it, env)
}

// ItemGraph returns the outbound references of one item (all envs or one).
func (s *Service) ItemGraph(ctx context.Context, tenantID, itemID, env string) (outbound []RefNodeView, err error) {
	item, err := s.store.GetItem(ctx, tenantID, itemID)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var envs []string
	if env != "" {
		envs = []string{env}
	} else {
		envs = sortedMapKeys(item.Values)
	}
	for _, e := range envs {
		edges, gerr := s.store.ItemRefs(ctx, tenantID, itemID, e)
		if gerr != nil {
			return nil, gerr
		}
		for _, edge := range edges {
			t, rerr := s.resolveTargetItem(ctx, tenantID, item, edge.Target)
			if rerr != nil {
				continue // broken edge shown via validation, not graph listing
			}
			n := nodeID(t.ID, envOr(edge.Target.Env, e))
			if _, ok := seen[n]; ok {
				continue
			}
			seen[n] = struct{}{}
			outbound = append(outbound, nodeView(t, envOr(edge.Target.Env, e)))
		}
	}
	sortSlice(outbound)
	return outbound, nil
}

func sortedMapKeys(m map[string]*domain.EnvValue) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortSlice(v []RefNodeView) {
	sort.Slice(v, func(i, j int) bool {
		if v[i].NamespaceID != v[j].NamespaceID {
			return v[i].NamespaceID < v[j].NamespaceID
		}
		if v[i].GroupID != v[j].GroupID {
			return v[i].GroupID < v[j].GroupID
		}
		if v[i].Key != v[j].Key {
			return v[i].Key < v[j].Key
		}
		return v[i].Env < v[j].Env
	})
}
