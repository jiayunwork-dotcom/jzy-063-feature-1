package service

import (
	"context"
	"sort"

	"configcenter/internal/domain"
	"configcenter/internal/gray"
	"configcenter/internal/merge"
	"configcenter/internal/push"
)

// FieldSource attributes one merged leaf field to its winning layer.
type FieldSource struct {
	Path          string       `json:"path"`
	Source        domain.Layer `json:"source"`
	SourceVersion int64        `json:"source_version"`
}

// EffectiveEntry is one key after three-layer merge, provenance included.
type EffectiveEntry struct {
	Key           string        `json:"key"`
	Format        domain.Format `json:"format"`
	Value         string        `json:"value"`
	Source        domain.Layer  `json:"source"`
	SourceVersion int64         `json:"source_version"`
	Fields        []FieldSource `json:"fields"`
	Version       int64         `json:"version"` // served version of the winning layer
}

// GroupSnapshot is the effective configuration of one group.
type GroupSnapshot struct {
	GroupID string           `json:"group_id"`
	Entries []EffectiveEntry `json:"entries"`
	Version int64            `json:"version"` // max served version, used for polling
}

// EffectiveSnapshot is what a client receives for a namespace.
type EffectiveSnapshot struct {
	TenantID    string                    `json:"tenant_id"`
	NamespaceID string                    `json:"namespace_id"`
	GroupID     string                    `json:"group_id,omitempty"`
	Env         string                    `json:"env"`
	Version     int64                     `json:"version"`
	Groups      map[string]*GroupSnapshot `json:"groups,omitempty"`  // when group unspecified
	Entries     []EffectiveEntry          `json:"entries,omitempty"` // when group specified
}

// InstanceContext identifies the polling instance for gray selection.
type InstanceContext struct {
	InstanceID string
	IP         string
}

func (ic InstanceContext) grayInstance() gray.Instance {
	id := ic.InstanceID
	if id == "" {
		id = ic.IP
	}
	return gray.Instance{ID: id, IP: ic.IP}
}

// layerContainers lists the three stores that contribute to a business group,
// weakest first.
func layerContainers(nsID, groupID string) []struct {
	ns, grp string
	layer   domain.Layer
} {
	return []struct {
		ns, grp string
		layer   domain.Layer
	}{
		{domain.PublicNamespaceID, domain.PublicNamespaceID, domain.LayerPublic},
		{nsID, domain.DefaultGroupID, domain.LayerNamespace},
		{nsID, groupID, domain.LayerGroup},
	}
}

// releaseByItem collects every active gray release touching this snapshot,
// from both the business namespace and the public namespace.
func (s *Service) releaseByItem(ctx context.Context, tenantID, nsID string) (map[string]*domain.Release, error) {
	out := map[string]*domain.Release{}
	for _, scope := range []string{nsID, domain.PublicNamespaceID} {
		rs, err := s.store.ActiveReleases(ctx, tenantID, scope)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			out[r.ItemID] = r
		}
	}
	return out, nil
}

// servedValue resolves the value/version an instance is entitled to for one
// item, honoring an ongoing gray release.
func (s *Service) servedValue(ctx context.Context, item *domain.Item, env string,
	releases map[string]*domain.Release, ic InstanceContext) (string, int64, error) {

	ev, exists := item.Values[env]
	if !exists {
		return "", 0, nil
	}
	r, grayed := releases[item.ID]
	if !grayed {
		return ev.Value, ev.Version, nil
	}
	if gray.ShouldDeliver(r.Strategy, r.Percent, r.IPs, r.ID, ic.grayInstance()) {
		// selected: sees the gray value (which equals the current value)
		return ev.Value, ev.Version, nil
	}
	// not selected: must see the value before the gray started
	if r.PrevVersion == 0 {
		return "", 0, nil
	}
	prev, err := s.store.GetVersion(ctx, item.TenantID, item.ID, env, r.PrevVersion)
	if err != nil {
		return "", 0, err
	}
	return prev.Value, r.PrevVersion, nil
}

// Effective computes the merged configuration visible to an instance. When
// groupID is empty every business group of the namespace is included.
func (s *Service) Effective(ctx context.Context, tenantID, nsID, groupID, env string, ic InstanceContext) (*EffectiveSnapshot, error) {
	releases, err := s.releaseByItem(ctx, tenantID, nsID)
	if err != nil {
		return nil, err
	}

	snap := &EffectiveSnapshot{
		TenantID: tenantID, NamespaceID: nsID, GroupID: groupID, Env: env,
	}

	groups := []string{groupID}
	if groupID == "" {
		gs, err := s.store.ListGroups(ctx, tenantID, nsID)
		if err != nil {
			return nil, err
		}
		for _, g := range gs {
			if g.ID == domain.DefaultGroupID {
				continue
			}
			groups = append(groups, g.ID)
		}
		snap.Groups = map[string]*GroupSnapshot{}
	}

	for _, gid := range groups {
		gs, err := s.groupSnapshot(ctx, tenantID, nsID, gid, env, releases, ic)
		if err != nil {
			return nil, err
		}
		if gs.Version > snap.Version {
			snap.Version = gs.Version
		}
		if groupID == "" {
			snap.Groups[gid] = gs
		} else {
			snap.Entries = gs.Entries
		}
	}
	return snap, nil
}

func (s *Service) groupSnapshot(ctx context.Context, tenantID, nsID, groupID, env string,
	releases map[string]*domain.Release, ic InstanceContext) (*GroupSnapshot, error) {

	// key -> layers that define it
	type layerData struct {
		layer   domain.Layer
		format  domain.Format
		value   string
		version int64
	}
	byKey := map[string][]layerData{}
	formatOf := map[string]domain.Format{}

	for _, loc := range layerContainers(nsID, groupID) {
		items, err := s.store.ListItems(ctx, tenantID, loc.ns, loc.grp)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			val, ver, err := s.servedValue(ctx, item, env, releases, ic)
			if err != nil {
				return nil, err
			}
			if ver == 0 {
				continue // item has no value in this env visible to the instance
			}
			byKey[item.Key] = append(byKey[item.Key], layerData{
				layer: loc.layer, format: item.Format, value: val, version: ver,
			})
			formatOf[item.Key] = item.Format
		}
	}

	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	g := &GroupSnapshot{GroupID: groupID}
	for _, key := range keys {
		layers := byKey[key]
		f := formatOf[key]
		inputs := make([]merge.LayerInput, 0, len(layers))
		for _, l := range layers {
			inputs = append(inputs, merge.LayerInput{
				Layer: l.layer, Format: l.format, Value: l.value, Version: l.version,
			})
		}
		res, err := merge.Merge(f, inputs)
		if err != nil {
			return nil, err
		}
		entry := EffectiveEntry{
			Key: key, Format: f, Value: res.Render,
		}
		// strongest layer among surviving leaves = the key's overall source
		var strongest domain.Layer
		var strongestVer int64
		for _, kr := range res.Keys {
			entry.Fields = append(entry.Fields, FieldSource{
				Path: kr.Path, Source: kr.Source, SourceVersion: kr.SourceVersion,
			})
			if domain.LayerPriority[kr.Source] >= domain.LayerPriority[strongest] {
				strongest = kr.Source
				strongestVer = kr.SourceVersion
			}
		}
		// version of the winning layer contribution (used for snapshot version)
		for _, l := range layers {
			if l.layer == strongest && l.version > entry.Version {
				entry.Version = l.version
			}
		}
		entry.Source = strongest
		entry.SourceVersion = strongestVer
		if entry.Version > g.Version {
			g.Version = entry.Version
		}
		g.Entries = append(g.Entries, entry)
	}
	return g, nil
}

// Gate is the push.Decider that enforces gray selection at delivery time.
type Gate struct{ svc *Service }

// NewGate builds the delivery decider.
func (s *Service) NewGate() *Gate { return &Gate{svc: s} }

// ShouldDeliver decides whether one connection is woken by an event.
func (g *Gate) ShouldDeliver(ev domain.PushEvent, c *push.Conn) bool {
	// environment filter (empty env subscribes to all envs)
	if c.Env != "" && ev.Env != "" && c.Env != ev.Env {
		return false
	}
	// group filter: group-scoped clients only receive their own group plus
	// namespace defaults and the public layer.
	if c.GroupID != "" && ev.GroupID != "" {
		switch ev.GroupID {
		case c.GroupID, domain.DefaultGroupID, domain.PublicNamespaceID:
		default:
			return false
		}
	}

	switch ev.Type {
	case domain.EventRollback:
		// only instances that actually received the gray value are pulled back
		return g.svc.hub.Tracker().DeliveredTo(ev.ReleaseID, c.InstanceID)
	case domain.EventPromote:
		return true
	default:
		if ev.ReleaseID == "" {
			return true
		}
		in := gray.Instance{ID: c.InstanceID, IP: c.IP}
		r, err := g.svc.store.GetRelease(context.Background(), ev.TenantID, ev.ReleaseID)
		if err != nil {
			return false
		}
		return gray.ShouldDeliver(r.Strategy, r.Percent, r.IPs, r.ID, in)
	}
}
