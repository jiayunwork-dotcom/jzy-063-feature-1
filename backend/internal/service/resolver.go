package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"configcenter/internal/domain"
	"configcenter/internal/merge"
	"configcenter/internal/ref"
)

// RefSegment attributes one substituted piece of an effective leaf to the
// referenced key it ultimately came from. Through is the chain of keys the
// value passed through (immediate target first, final source last),
// preserving provenance across chained references.
type RefSegment struct {
	Placeholder   string       `json:"placeholder"`
	NamespaceID   string       `json:"namespace_id"`
	GroupID       string       `json:"group_id"`
	Key           string       `json:"key"`
	Env           string       `json:"env"`
	Source        domain.Layer `json:"source"`
	SourceVersion int64        `json:"source_version"`
	Through       []RefHop     `json:"through,omitempty"`
}

// RefHop is one hop of a reference chain.
type RefHop struct {
	NamespaceID string `json:"namespace_id"`
	GroupID     string `json:"group_id"`
	Key         string `json:"key"`
	Env         string `json:"env"`
}

// ResolvedField extends the existing per-leaf provenance with the reference
// segments that filled its value or its map key (empty for plain fields).
type ResolvedField struct {
	FieldSource
	ValueSegments []RefSegment `json:"value_refs,omitempty"`
	KeySegments   []RefSegment `json:"key_refs,omitempty"`
}

type nodeCoord struct {
	ns, grp, key, env string
}

func (c nodeCoord) label() string { return c.ns + "/" + c.grp + "/" + c.key + "[" + c.env + "]" }

type resolvedNode struct {
	entry  EffectiveEntry
	fields []ResolvedField
	root   map[string]any
	item   *domain.Item
	rev    int64
}

// resolver dereferences one effective snapshot for one instance. Every key
// is resolved lazily through the same instance-specific gray selection, so an
// instance outside a gray release dereferences against the old versions
// everywhere in the chain as well.
type resolver struct {
	s        *Service
	ctx      context.Context
	tenantID string
	env      string
	ic       InstanceContext
	// latestOnly skips gray selection and always uses the current committed
	// value. It serves authoring-time (commit) validation, which checks the
	// shape of "what the value will be once promoted".
	latestOnly bool

	releases map[string]map[string]*domain.Release // ns -> itemID -> active release
	items    map[string]map[string]*domain.Item    // cache key -> key -> visible item

	memo   map[string]*resolvedNode
	onPath map[string]bool
	stack  []nodeCoord
}

func newResolver(s *Service, ctx context.Context, tenantID, env string, ic InstanceContext) *resolver {
	return &resolver{
		s: s, ctx: ctx, tenantID: tenantID, env: env, ic: ic,
		releases: map[string]map[string]*domain.Release{},
		items:    map[string]map[string]*domain.Item{},
		memo:     map[string]*resolvedNode{},
		onPath:   map[string]bool{},
	}
}

func (r *resolver) cacheKey(c nodeCoord) string {
	return c.env + "|" + c.ns + "/" + c.grp
}

func (r *resolver) activeReleases(ns string) (map[string]*domain.Release, error) {
	if m, ok := r.releases[ns]; ok {
		return m, nil
	}
	rs, err := r.s.store.ActiveReleases(r.ctx, r.tenantID, ns)
	if err != nil {
		return nil, err
	}
	m := map[string]*domain.Release{}
	for _, x := range rs {
		m[x.ItemID] = x
	}
	r.releases[ns] = m
	return m, nil
}

// containerItems returns instance-visible items of one container for one
// env, applying the same servedValue gray gate as the non-ref path.
func (r *resolver) containerItems(c nodeCoord) (map[string]*domain.Item, error) {
	ck := r.cacheKey(c)
	if m, ok := r.items[ck]; ok {
		return m, nil
	}
	list, err := r.s.store.ListItems(r.ctx, r.tenantID, c.ns, c.grp)
	if err != nil {
		return nil, err
	}
	var releases map[string]*domain.Release
	if !r.latestOnly {
		releases, err = r.activeReleases(c.ns)
		if err != nil {
			return nil, err
		}
	}
	m := map[string]*domain.Item{}
	for _, it := range list {
		var val string
		var ver, rev int64
		if r.latestOnly {
			ev, ok := it.Values[c.env]
			if !ok {
				continue
			}
			val, ver, rev = ev.Value, ev.Version, ev.Revision
		} else {
			val, ver, rev, err = r.s.servedValue(r.ctx, it, c.env, releases, r.ic)
			if err != nil {
				return nil, err
			}
			if ver == 0 {
				continue
			}
		}
		cp := *it
		cp.Values = map[string]*domain.EnvValue{c.env: {Value: val, Version: ver, Revision: rev}}
		m[it.Key] = &cp
	}
	r.items[ck] = m
	return m, nil
}

// resolveKey resolves one key as seen from a business group context.
func (r *resolver) resolveKey(ns, grp, key string) (*resolvedNode, error) {
	return r.resolve(nodeCoord{ns: ns, grp: grp, key: key, env: r.env})
}

func (r *resolver) resolve(c nodeCoord) (*resolvedNode, error) {
	if c.env == "" {
		c.env = r.env
	}
	key := r.cacheKey(c) + "/" + c.key
	if n, ok := r.memo[key]; ok {
		return n, nil
	}
	if r.onPath[key] {
		return nil, &RefError{Code: CodeRefCycle, Message: "解引用时发现引用环", Cycle: r.cycleChain(c)}
	}
	r.onPath[key] = true
	r.stack = append(r.stack, c)
	defer func() {
		delete(r.onPath, key)
		r.stack = r.stack[:len(r.stack)-1]
	}()

	contribs, strongest, rev, err := r.gatherLayers(c)
	if err != nil {
		return nil, err
	}
	if len(contribs) == 0 {
		return nil, &RefError{
			Code:    CodeRefNoValue,
			Target:  c.label(),
			Message: fmt.Sprintf("引用目标 %s 在环境 %s 中没有值", c.ns+"/"+c.grp+"/"+c.key, c.env),
		}
	}
	inputs := make([]merge.LayerInput, 0, len(contribs))
	for _, cc := range contribs {
		inputs = append(inputs, merge.LayerInput{Layer: cc.layer, Format: cc.format, Value: cc.value, Version: cc.ver})
	}
	n, err := r.resolveDocument(strongest.format, inputs, strongest.item, c, key, rev)
	if err != nil {
		return nil, err
	}
	r.memo[key] = n
	return n, nil
}

// resolveDocument merges one key's layer inputs and dereferences the result.
// It is split out so commit-time validation can resolve a hypothetical value
// that is not stored yet.
func (r *resolver) resolveDocument(f domain.Format, inputs []merge.LayerInput,
	item *domain.Item, c nodeCoord, memoKey string, baseRev int64) (*resolvedNode, error) {

	mr, err := merge.Merge(f, inputs)
	if err != nil {
		return nil, err
	}
	rev := baseRev

	entry := EffectiveEntry{Key: c.key, Format: f}
	var fields []ResolvedField
	root := mr.Root
	// Reflow only when the document contains a reference (raw placeholder,
	// embedded in a string) or a masked structural placeholder or an escaped
	// delimiter. Reference-free configs never pay for the walk and keep the
	// merge kernel's exact rendered bytes.
	if ref.HasPlaceholder(mr.Render) || ref.HasMarker(mr.Render) || strings.Contains(mr.Render, "@@{") {
		sub, vs, ks, srev, serr := r.substitute(mr, f, c)
		if serr != nil {
			return nil, serr
		}
		root = sub
		rendered, err := merge.Encode(f, sub)
		if err != nil {
			return nil, err
		}
		entry.Value = rendered
		if srev > rev {
			rev = srev
		}
		for _, kr := range mr.Keys {
			rf := ResolvedField{FieldSource: FieldSource{
				Path: kr.Path, Source: kr.Source, SourceVersion: kr.SourceVersion,
			}}
			if s, ok := vs[kr.Path]; ok {
				rf.ValueSegments = s
			}
			if ks, ok := ks[kr.Path]; ok {
				rf.KeySegments = ks
			}
			fields = append(fields, rf)
		}
	} else {
		entry.Value = mr.Render
		for _, kr := range mr.Keys {
			fields = append(fields, ResolvedField{FieldSource: FieldSource{
				Path: kr.Path, Source: kr.Source, SourceVersion: kr.SourceVersion,
			}})
		}
	}
	for i := range fields {
		entry.Fields = append(entry.Fields, fields[i].FieldSource)
		if domain.LayerPriority[fields[i].Source] >= domain.LayerPriority[entry.Source] {
			entry.Source = fields[i].Source
			entry.SourceVersion = fields[i].SourceVersion
		}
	}
	if hasAnySegments(fields) {
		entry.ResolvedFields = fields
	}
	entry.Version = rev
	return &resolvedNode{entry: entry, fields: fields, root: root, item: item, rev: rev}, nil
}

type layerContrib struct {
	layer  domain.Layer
	format domain.Format
	value  string
	ver    int64
	rev    int64
	item   *domain.Item
}

// gatherLayers collects strongest-first layer contributions of a key at a
// coordinate and the max revision over them.
func (r *resolver) gatherLayers(c nodeCoord) ([]layerContrib, layerContrib, int64, error) {
	var strongest layerContrib
	var out []layerContrib
	rev := int64(0)
	seenItem := map[string]bool{}
	for _, loc := range servingContainers(c.ns, c.grp) {
		m, err := r.containerItems(nodeCoord{ns: loc.ns, grp: loc.grp, env: c.env})
		if err != nil {
			return nil, strongest, 0, err
		}
		it, ok := m[c.key]
		if !ok || seenItem[it.ID] {
			continue
		}
		seenItem[it.ID] = true
		ev := it.Values[c.env]
		lc := layerContrib{layer: loc.layer, format: it.Format, value: ev.Value, ver: ev.Version, rev: ev.Revision, item: it}
		out = append([]layerContrib{lc}, out...) // prepend -> strongest first
		strongest = lc
		if ev.Revision > rev {
			rev = ev.Revision
		}
	}
	return out, strongest, rev, nil
}

// cycleChain renders the DFS stack from the first occurrence of the repeated
// coordinate through to it again (both ends included).
func (r *resolver) cycleChain(c nodeCoord) []string {
	start := 0
	for i, x := range r.stack {
		if x == c {
			start = i
			break
		}
	}
	out := make([]string, 0, len(r.stack)-start+1)
	for _, x := range r.stack[start:] {
		out = append(out, x.label())
	}
	out = append(out, c.label())
	return out
}

// servingContainers lists weakest-first contributing layers for a coordinate.
func servingContainers(ns, grp string) []struct {
	ns, grp string
	layer   domain.Layer
} {
	pub := struct {
		ns, grp string
		layer   domain.Layer
	}{domain.PublicNamespaceID, domain.PublicNamespaceID, domain.LayerPublic}
	switch {
	case ns == domain.PublicNamespaceID:
		return []struct {
			ns, grp string
			layer   domain.Layer
		}{pub}
	case grp == domain.DefaultGroupID:
		return []struct {
			ns, grp string
			layer   domain.Layer
		}{
			pub,
			{ns, domain.DefaultGroupID, domain.LayerNamespace},
		}
	default:
		return []struct {
			ns, grp string
			layer   domain.Layer
		}{
			pub,
			{ns, domain.DefaultGroupID, domain.LayerNamespace},
			{ns, grp, domain.LayerGroup},
		}
	}
}

// substitute walks the merged document and replaces placeholders with
// resolved effective values, collecting segment provenance per leaf path.
func (r *resolver) substitute(mr *merge.Result, f domain.Format, from nodeCoord) (
	map[string]any, map[string][]RefSegment, map[string][]RefSegment, int64, error) {

	valSeg := map[string][]RefSegment{}
	keySeg := map[string][]RefSegment{}
	maxRev := int64(0)

	resolveToken := func(t ref.Token) (*resolvedNode, RefSegment, error) {
		to := targetCoord(t, from)
		node, err := r.resolve(to)
		if err != nil {
			return nil, RefSegment{}, err
		}
		seg := RefSegment{
			Placeholder: t.Raw,
			NamespaceID: node.item.NamespaceID, GroupID: node.item.GroupID,
			Key: node.item.Key, Env: to.env,
			Source: node.entry.Source, SourceVersion: node.entry.SourceVersion,
			Through: []RefHop{{NamespaceID: to.ns, GroupID: to.grp, Key: to.key, Env: to.env}},
		}
		// Extend the chain through placeholders inside the target's value.
		for _, nf := range node.fields {
			if len(nf.ValueSegments) > 0 {
				seg.Through = append(seg.Through, nf.ValueSegments[0].Through...)
				break
			}
		}
		if node.rev > maxRev {
			maxRev = node.rev
		}
		return node, seg, nil
	}

	var walkMap func(m map[string]any, prefix string, inherited []RefSegment) (map[string]any, error)
	walkMap = func(m map[string]any, prefix string, inherited []RefSegment) (map[string]any, error) {
		out := make(map[string]any, len(m))
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			path := joinDotted(prefix, k)
			// Map keys only embed text.
			nk, ksegs, err := substituteScalar(k, f, from, inherited, resolveToken, false)
			if err != nil {
				return nil, err
			}
			newKey, _ := nk.(string)
			switch vv := m[k].(type) {
			case map[string]any:
				sub, err := walkMap(vv, path, ksegs)
				if err != nil {
					return nil, err
				}
				out[newKey] = sub
			default:
				// Whole-value document expansion (properties/TOML key-level
				// merge). Detect the placeholder the value consists of:
				// masked sentinel for TOML, raw single token for properties.
				var docToken *ref.Token
				if f == domain.FormatTOML {
					if s, isStr := vv.(string); isStr {
						if raw := ref.UnmaskMarker(s); raw != "" {
							if t, perr := tokenFromRaw(raw); perr == nil {
								docToken = &t
							}
						}
					}
				} else if f == domain.FormatProperties {
					if s, isStr := vv.(string); isStr {
						if toks, terr := ref.Scan(s); terr == nil && len(toks) == 1 && toks[0].Placeholder {
							t := toks[0]
							docToken = &t
						}
					}
				}
				if docToken != nil {
					node, seg, rerr := resolveToken(*docToken)
					if rerr == nil && isDocumentNode(node) {
						// Key-level merge: referencing key replaced by the
						// target's key set.
						for tk, tv := range node.root {
							out[tk] = cloneValue(tv)
							valSeg[joinDotted(prefix, tk)] = append(valSeg[joinDotted(prefix, tk)], seg)
						}
						continue
					}
					if rerr != nil {
						return nil, rerr
					}
					// Single-scalar target falls through to scalar/value
					// substitution below.
				}
				// For properties a value that is exactly one placeholder means
				// "inline the referenced document" (whole position); a token
				// embedded in surrounding text substitutes as a scalar.
				whole := f == domain.FormatJSON || f == domain.FormatYAML || f == domain.FormatTOML
				if !whole {
					if s, isStr := vv.(string); isStr {
						if toks, terr := ref.Scan(s); terr == nil && len(toks) == 1 && toks[0].Placeholder {
							whole = true
						}
					}
				}
				nv, vsegs, err := substituteScalar(vv, f, from, ksegs, resolveToken, whole)
				if err != nil {
					return nil, err
				}
				out[newKey] = nv
				if len(vsegs) > 0 {
					valSeg[path] = append(valSeg[path], vsegs...)
				}
				if len(ksegs) > 0 {
					keySeg[path] = append(keySeg[path], ksegs...)
				}
			}
		}
		return out, nil
	}

	sub, err := walkMap(mr.Root, "", nil)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	return sub, valSeg, keySeg, maxRev, nil
}

// tokenResolver resolves one placeholder token to its node.
type tokenResolver func(t ref.Token) (*resolvedNode, RefSegment, error)

// substituteScalar replaces placeholders in one scalar string.
//
// wholePos=true means this string occupies an entire document scalar
// position (a masked structural placeholder):
//   - JSON/YAML: single-scalar target substitutes with its typed scalar
//     (numbers/booleans keep type), a multi-field target substitutes with the
//     target object (embedded structurally);
//   - properties/TOML whole-position expansion is handled by the caller as a
//     key-level merge; here single scalars substitute textually.
//
// Embedded placeholders (wholePos=false) always contribute text.
func substituteScalar(v any, f domain.Format, from nodeCoord, inherited []RefSegment,
	resolve tokenResolver, wholePos bool) (any, []RefSegment, error) {

	s, ok := v.(string)
	if !ok {
		return v, inherited, nil
	}
	// Whole scalar position: detect a single placeholder, whether it arrived
	// as a masked sentinel (JSON/YAML/TOML) or as raw text (properties).
	if wholePos {
		var wholeToken ref.Token
		isWhole := false
		if raw := ref.UnmaskMarker(s); raw != "" {
			if t, perr := tokenFromRaw(raw); perr == nil {
				wholeToken, isWhole = t, true
			}
		} else if toks0, terr := ref.Scan(s); terr == nil && len(toks0) == 1 && toks0[0].Placeholder {
			wholeToken, isWhole = toks0[0], true
		}
		if isWhole {
			node, seg, err := resolve(wholeToken)
			if err != nil {
				return nil, nil, err
			}
			if f == domain.FormatJSON || f == domain.FormatYAML {
				if scalar, single := singleScalar(node.root); single {
					return coerceScalar(scalar, f), append(copySegs(inherited), seg), nil
				}
				return cloneValue(node.root), append(copySegs(inherited), seg), nil
			}
			// properties/TOML at a whole scalar position: a single-scalar
			// target always substitutes its scalar value (so k=@{other} and
			// x=a-@{b} behave consistently); a multi-field document key-merges
			// one level up. Rendered whole text is not used here.
			if scalar, single := singleScalar(node.root); single {
				return merge.FormatScalar(scalar), append(copySegs(inherited), seg), nil
			}
			return strings.TrimRight(node.entry.Value, "\n"), append(copySegs(inherited), seg), nil
		}
	}
	toks, err := ref.Scan(s)
	if err != nil {
		return nil, nil, &RefError{Code: CodeRefSyntax, Message: err.Error()}
	}
	hasPH := false
	for _, t := range toks {
		if t.Placeholder {
			hasPH = true
		}
	}
	if !hasPH {
		// No reference: at most collapse escaped "@@{" delimiters. Rebuilding
		// through the tokens keeps every other byte identical.
		rebuilt := concatLiterals(toks)
		if rebuilt == s {
			return v, inherited, nil
		}
		return rebuilt, inherited, nil
	}
	segs := copySegs(inherited)
	if len(toks) == 1 && toks[0].Placeholder {
		node, seg, err := resolve(toks[0])
		if err != nil {
			return nil, nil, err
		}
		// A single embedded token in a text format substitutes the target's
		// scalar value for single-scalar documents (m=@{other} -> m=value).
		if f != domain.FormatJSON && f != domain.FormatYAML {
			if v, ok := singleScalar(node.root); ok {
				return merge.FormatScalar(v), append(segs, seg), nil
			}
		}
		return wholeText(node), append(segs, seg), nil
	}
	var b strings.Builder
	for _, t := range toks {
		if !t.Placeholder {
			b.WriteString(t.Literal)
			continue
		}
		node, seg, err := resolve(t)
		if err != nil {
			return nil, nil, err
		}
		b.WriteString(scalarText(node))
		segs = append(segs, seg)
	}
	return b.String(), segs, nil
}

// wholeText renders a referenced node as text for an embedded scalar slot.
func wholeText(node *resolvedNode) string {
	return strings.TrimRight(node.entry.Value, "\n")
}

// wholeValue returns the native value to embed when an entire scalar position
// is one placeholder. Multi-field document targets are structurally merged by
// the caller (substitute walk); this only handles single-scalar documents.
func wholeValue(node *resolvedNode, f domain.Format) any {
	if scalar, ok := singleScalar(node.root); ok {
		return coerceScalar(scalar, f)
	}
	// Multi-field target reached from a quoted/embedded scalar position:
	// render as text.
	return strings.TrimRight(node.entry.Value, "\n")
}

// isDocumentNode reports whether a resolved target carries more than a single
// scalar (and therefore expands structurally at a whole-value position).
func isDocumentNode(node *resolvedNode) bool {
	_, single := singleScalar(node.root)
	return !single
}

// scalarText returns the text an embedded placeholder contributes.
func scalarText(node *resolvedNode) string {
	if v, ok := singleScalar(node.root); ok {
		return merge.FormatScalar(v)
	}
	return strings.TrimRight(node.entry.Value, "\n")
}

// singleScalar reports whether a document consists of exactly one top-level
// scalar (e.g. properties "v=host" or a one-field JSON), returning it.
func singleScalar(root map[string]any) (any, bool) {
	if len(root) != 1 {
		return nil, false
	}
	for _, v := range root {
		if _, isMap := v.(map[string]any); isMap {
			return nil, false
		}
		return v, true
	}
	return nil, false
}

// coerceScalar maps a target scalar onto the type expected at a whole-value
// position. Everything is text in properties files; JSON/YAML numbers and
// booleans stay typed when the target scalar text represents them.
func coerceScalar(v any, f domain.Format) any {
	if f != domain.FormatJSON && f != domain.FormatYAML {
		return merge.FormatScalar(v)
	}
	if s, isStr := v.(string); isStr {
		return typedFromString(s, f)
	}
	return v
}

func typedFromString(s string, f domain.Format) any {
	switch f {
	case domain.FormatJSON:
		switch s {
		case "true":
			return true
		case "false":
			return false
		case "null":
			return nil
		}
		if looksNumeric(s) {
			if n, err := strconv.ParseFloat(s, 64); err == nil {
				return n
			}
		}
		return s
	default: // YAML
		switch s {
		case "true":
			return true
		case "false":
			return false
		case "null", "~":
			return nil
		}
		if looksNumeric(s) {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				return n
			}
			if n, err := strconv.ParseFloat(s, 64); err == nil {
				return n
			}
		}
		return s
	}
}

func cloneValue(v any) any {
	if m, ok := v.(map[string]any); ok {
		out := make(map[string]any, len(m))
		for k, x := range m {
			out[k] = cloneValue(x)
		}
		return out
	}
	return v
}

func concatLiterals(toks []ref.Token) string {
	var b strings.Builder
	for _, t := range toks {
		if !t.Placeholder {
			b.WriteString(t.Literal)
		}
	}
	return b.String()
}

// tokenFromRaw rebuilds the single placeholder token of a masked scalar.
func tokenFromRaw(raw string) (ref.Token, error) {
	toks, err := ref.Scan(raw)
	if err != nil || len(toks) != 1 || !toks[0].Placeholder {
		return ref.Token{}, &RefError{Code: CodeRefSyntax, Message: "损坏的结构性占位符标记：" + raw}
	}
	return toks[0], nil
}

func targetCoord(t ref.Token, from nodeCoord) nodeCoord {
	env := t.Env
	if env == "" {
		env = from.env
	}
	if strings.Count(t.Target, "/") == 2 {
		p := strings.Split(t.Target, "/")
		return nodeCoord{ns: p[0], grp: p[1], key: p[2], env: env}
	}
	return nodeCoord{ns: from.ns, grp: from.grp, key: t.Target, env: env}
}

func copySegs(in []RefSegment) []RefSegment {
	if len(in) == 0 {
		return nil
	}
	out := make([]RefSegment, len(in))
	copy(out, in)
	return out
}

func joinDotted(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func looksNumeric(s string) bool {
	if s == "" {
		return false
	}
	dot := false
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r == '.':
			if dot {
				return false
			}
			dot = true
		case r == '-' || r == '+':
			if i != 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func hasAnySegments(fs []ResolvedField) bool {
	for _, f := range fs {
		if len(f.ValueSegments) > 0 || len(f.KeySegments) > 0 {
			return true
		}
	}
	return false
}
