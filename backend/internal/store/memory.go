package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"configcenter/internal/domain"
)

// Memory is a thread-safe in-memory Store used by unit tests and as a
// reference implementation.
type Memory struct {
	mu sync.RWMutex

	tenants    map[string]*domain.Tenant
	namespaces map[string]*domain.Namespace // key: tenantID + "/" + id
	groups     map[string]*domain.Group     // key: tenantID + "/" + nsID + "/" + id
	items      map[string]*domain.Item      // key: item id
	itemByKey  map[string]string            // tenant/ns/group/key -> item id
	versions   map[string][]*domain.Version // key: tenant/itemID/env
	versionSeq map[string]int64             // key: tenant/itemID/env
	releases   map[string]*domain.Release
	verDBSeq   int64
}

// NewMemory constructs an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		tenants:    map[string]*domain.Tenant{},
		namespaces: map[string]*domain.Namespace{},
		groups:     map[string]*domain.Group{},
		items:      map[string]*domain.Item{},
		itemByKey:  map[string]string{},
		versions:   map[string][]*domain.Version{},
		versionSeq: map[string]int64{},
		releases:   map[string]*domain.Release{},
	}
}

func (m *Memory) Ping(context.Context) error { return nil }

func (m *Memory) CreateTenant(_ context.Context, t *domain.Tenant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tenants[t.ID]; ok {
		return ErrDuplicate
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	cp := *t
	m.tenants[t.ID] = &cp
	return nil
}

func (m *Memory) GetTenant(_ context.Context, id string) (*domain.Tenant, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tenants[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *t
	return &cp, nil
}

func (m *Memory) ListTenants(_ context.Context) ([]*domain.Tenant, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*domain.Tenant, 0, len(m.tenants))
	for _, t := range m.tenants {
		cp := *t
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *Memory) UpdateTenantQuota(_ context.Context, id string, maxNs, maxItems, retention int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tenants[id]
	if !ok {
		return ErrNotFound
	}
	t.MaxNamespaces = maxNs
	t.MaxItemsPerGroup = maxItems
	t.VersionRetention = retention
	return nil
}

func (m *Memory) CreateNamespace(_ context.Context, n *domain.Namespace) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := n.TenantID + "/" + n.ID
	if _, ok := m.namespaces[key]; ok {
		return ErrDuplicate
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now()
	}
	cp := *n
	m.namespaces[key] = &cp
	return nil
}

func (m *Memory) GetNamespace(_ context.Context, tenantID, id string) (*domain.Namespace, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n, ok := m.namespaces[tenantID+"/"+id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *n
	return &cp, nil
}

func (m *Memory) ListNamespaces(_ context.Context, tenantID string) ([]*domain.Namespace, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*domain.Namespace
	for _, n := range m.namespaces {
		if n.TenantID == tenantID {
			cp := *n
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *Memory) CountNamespaces(_ context.Context, tenantID string) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c := 0
	for _, n := range m.namespaces {
		if n.TenantID == tenantID {
			c++
		}
	}
	return c, nil
}

func (m *Memory) CreateGroup(_ context.Context, g *domain.Group) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := g.TenantID + "/" + g.NamespaceID + "/" + g.ID
	if _, ok := m.groups[key]; ok {
		return ErrDuplicate
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now()
	}
	cp := *g
	m.groups[key] = &cp
	return nil
}

func (m *Memory) GetGroup(_ context.Context, tenantID, namespaceID, id string) (*domain.Group, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	g, ok := m.groups[tenantID+"/"+namespaceID+"/"+id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *g
	return &cp, nil
}

func (m *Memory) ListGroups(_ context.Context, tenantID, namespaceID string) ([]*domain.Group, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*domain.Group
	for _, g := range m.groups {
		if g.TenantID == tenantID && g.NamespaceID == namespaceID {
			cp := *g
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *Memory) CreateItem(_ context.Context, item *domain.Item) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[item.ID]; ok {
		return ErrDuplicate
	}
	key := item.TenantID + "/" + item.NamespaceID + "/" + item.GroupID + "/" + item.Key
	if _, ok := m.itemByKey[key]; ok {
		return ErrDuplicate
	}
	if item.Values == nil {
		item.Values = map[string]*domain.EnvValue{}
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.CreatedAt
	}
	cp := cloneItem(item)
	m.items[item.ID] = cp
	m.itemByKey[key] = item.ID
	return nil
}

func (m *Memory) GetItem(_ context.Context, tenantID, id string) (*domain.Item, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	item, ok := m.items[id]
	if !ok || item.TenantID != tenantID {
		return nil, ErrNotFound
	}
	return cloneItem(item), nil
}

func (m *Memory) GetItemByKey(_ context.Context, tenantID, namespaceID, groupID, key string) (*domain.Item, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.itemByKey[tenantID+"/"+namespaceID+"/"+groupID+"/"+key]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneItem(m.items[id]), nil
}

func (m *Memory) ListItems(_ context.Context, tenantID, namespaceID, groupID string) ([]*domain.Item, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*domain.Item
	for _, item := range m.items {
		if item.TenantID != tenantID || item.NamespaceID != namespaceID || item.GroupID != groupID {
			continue
		}
		out = append(out, cloneItem(item))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (m *Memory) CountItems(_ context.Context, tenantID, namespaceID, groupID string) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c := 0
	for _, item := range m.items {
		if item.TenantID == tenantID && item.NamespaceID == namespaceID && item.GroupID == groupID {
			c++
		}
	}
	return c, nil
}

func (m *Memory) UpdateItemSchema(_ context.Context, tenantID, id, schema string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.items[id]
	if !ok || item.TenantID != tenantID {
		return ErrNotFound
	}
	item.Schema = schema
	item.UpdatedAt = time.Now()
	return nil
}

func (m *Memory) CommitValue(_ context.Context, p CommitValueParams) (*domain.Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.items[p.ItemID]
	if !ok || item.TenantID != p.TenantID {
		return nil, ErrNotFound
	}
	seqKey := p.TenantID + "/" + p.ItemID + "/" + p.Env
	current := int64(0)
	if ev, ok := item.Values[p.Env]; ok {
		current = ev.Version
	}
	if p.ExpectedVersion != current {
		return nil, ErrConflict
	}
	newVer := m.versionSeq[seqKey] + 1
	m.versionSeq[seqKey] = newVer
	v := &domain.Version{
		ID:         m.verDBSeq + 1,
		ItemID:     p.ItemID,
		TenantID:   p.TenantID,
		Env:        p.Env,
		Version:    newVer,
		Value:      p.Value,
		Operator:   p.Operator,
		ChangeType: p.ChangeType,
		Note:       p.Note,
		CreatedAt:  time.Now(),
	}
	m.verDBSeq++
	m.versions[seqKey] = append(m.versions[seqKey], v)
	if p.Retention > 0 && len(m.versions[seqKey]) > p.Retention {
		m.versions[seqKey] = m.versions[seqKey][len(m.versions[seqKey])-p.Retention:]
	}
	if item.Values == nil {
		item.Values = map[string]*domain.EnvValue{}
	}
	item.Values[p.Env] = &domain.EnvValue{
		Value: p.Value, Version: newVer, UpdatedAt: v.CreatedAt, UpdatedBy: p.Operator,
	}
	item.UpdatedAt = v.CreatedAt
	cp := *v
	return &cp, nil
}

func (m *Memory) ListVersions(_ context.Context, tenantID, itemID, env string, limit int) ([]*domain.Version, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := m.versions[tenantID+"/"+itemID+"/"+env]
	out := make([]*domain.Version, 0, len(list))
	for _, v := range list {
		cp := *v
		out = append(out, &cp)
	}
	// newest first
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *Memory) GetVersion(_ context.Context, tenantID, itemID, env string, ver int64) (*domain.Version, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, v := range m.versions[tenantID+"/"+itemID+"/"+env] {
		if v.Version == ver {
			cp := *v
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (m *Memory) CreateRelease(_ context.Context, r *domain.Release) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.releases[r.ID]; ok {
		return ErrDuplicate
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now()
	}
	cp := *r
	m.releases[r.ID] = &cp
	return nil
}

func (m *Memory) GetRelease(_ context.Context, tenantID, id string) (*domain.Release, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.releases[id]
	if !ok || r.TenantID != tenantID {
		return nil, ErrNotFound
	}
	cp := *r
	return &cp, nil
}

func (m *Memory) UpdateRelease(_ context.Context, r *domain.Release) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.releases[r.ID]
	if !ok || old.TenantID != r.TenantID {
		return ErrNotFound
	}
	cp := *r
	m.releases[r.ID] = &cp
	return nil
}

func (m *Memory) ListReleases(_ context.Context, tenantID, namespaceID string, limit int) ([]*domain.Release, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*domain.Release
	for _, r := range m.releases {
		if r.TenantID != tenantID || r.NamespaceID != namespaceID {
			continue
		}
		cp := *r
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *Memory) ActiveReleases(_ context.Context, tenantID, namespaceID string) ([]*domain.Release, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*domain.Release
	for _, r := range m.releases {
		if r.TenantID == tenantID && r.NamespaceID == namespaceID && r.Status == domain.ReleaseGray {
			cp := *r
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, nil
}

func (m *Memory) Close() error { return nil }

func cloneItem(in *domain.Item) *domain.Item {
	cp := *in
	if in.Values != nil {
		cp.Values = make(map[string]*domain.EnvValue, len(in.Values))
		for k, v := range in.Values {
			vcp := *v
			cp.Values[k] = &vcp
		}
	}
	return &cp
}
