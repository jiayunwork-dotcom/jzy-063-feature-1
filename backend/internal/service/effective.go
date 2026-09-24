package service

import (
	"context"
	"sort"

	"configcenter/internal/domain"
	"configcenter/internal/gray"
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
	// ResolvedFields carries per-leaf reference provenance (which referenced
	// key filled each fragment and through which chain). It is nil for keys
	// without any placeholder, keeping old payloads byte-for-byte compatible.
	ResolvedFields []ResolvedField `json:"resolved_fields,omitempty"`
	Version        int64           `json:"version"` // tenant revision of the key's reference closure
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

// servedValue resolves the value/version/revision an instance is entitled to
// for one item, honoring an ongoing gray release. The revision comes from the
// actual served version (current for selected instances, the historical
// version for instances a gray release hides it from), so downstream
// reference resolution advances the snapshot clock correctly for everyone.
func (s *Service) servedValue(ctx context.Context, item *domain.Item, env string,
	releases map[string]*domain.Release, ic InstanceContext) (string, int64, int64, error) {

	ev, exists := item.Values[env]
	if !exists {
		return "", 0, 0, nil
	}
	r, grayed := releases[item.ID]
	if !grayed {
		return ev.Value, ev.Version, ev.Revision, nil
	}
	if gray.ShouldDeliver(r.Strategy, r.Percent, r.IPs, r.ID, ic.grayInstance()) {
		// selected: sees the gray value (which equals the current value)
		return ev.Value, ev.Version, ev.Revision, nil
	}
	// not selected: must see the value before the gray started
	if r.PrevVersion == 0 {
		return "", 0, 0, nil
	}
	prev, err := s.store.GetVersion(ctx, item.TenantID, item.ID, env, r.PrevVersion)
	if err != nil {
		return "", 0, 0, err
	}
	return prev.Value, r.PrevVersion, prev.Revision, nil
}

// Effective computes the merged configuration visible to an instance. When
// groupID is empty every business group of the namespace is included.
func (s *Service) Effective(ctx context.Context, tenantID, nsID, groupID, env string, ic InstanceContext) (*EffectiveSnapshot, error) {
	r := newResolver(s, ctx, tenantID, env, ic)

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
		gs, err := s.groupSnapshot(r, nsID, gid)
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

// groupSnapshot resolves every effective key of one business group through
// the shared resolver. The snapshot version is the max tenant revision over
// each key and its whole reference closure, so changing a referenced key
// moves the served version of every dependent key without any new commit.
func (s *Service) groupSnapshot(r *resolver, nsID, groupID string) (*GroupSnapshot, error) {
	// Union of keys contributed by the three layer containers (as visible to
	// this instance in this env).
	keySet := map[string]struct{}{}
	keys := []string{}
	for _, loc := range servingContainers(nsID, groupID) {
		m, err := r.containerItems(nodeCoord{ns: loc.ns, grp: loc.grp, env: r.env})
		if err != nil {
			return nil, err
		}
		for k := range m {
			if _, ok := keySet[k]; !ok {
				keySet[k] = struct{}{}
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)

	g := &GroupSnapshot{GroupID: groupID}
	for _, key := range keys {
		n, err := r.resolveKey(nsID, groupID, key)
		if err != nil {
			return nil, err
		}
		g.Entries = append(g.Entries, n.entry)
		if n.rev > g.Version {
			g.Version = n.rev
		}
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
