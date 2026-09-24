// Package service orchestrates the platform: CRUD with quota enforcement,
// syntax/schema validation, version commits and rollback, the three-layer
// effective-config computation, gray release lifecycle and the serving-time
// gate that decides whether a connected instance sees a new value.
package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"configcenter/internal/domain"
	"configcenter/internal/push"
	"configcenter/internal/store"
	"configcenter/internal/validator"
	"configcenter/internal/version"
)

// Service is the single backend orchestrator.
type Service struct {
	store store.Store
	bus   push.EventBus
	hub   *push.Hub
}

// New wires a service.
func New(st store.Store, bus push.EventBus, hub *push.Hub) *Service {
	return &Service{store: st, bus: bus, hub: hub}
}

// Hub is exposed so the API layer can attach transport channels.
func (s *Service) Hub() *push.Hub { return s.hub }

// ---------- validation DTO ----------

// ValidationFailure carries structured reasons back to the API.
type ValidationFailure struct {
	Errors []*validator.Error
}

func (e *ValidationFailure) Error() string {
	parts := make([]string, 0, len(e.Errors))
	for _, x := range e.Errors {
		parts = append(parts, x.Message)
	}
	return strings.Join(parts, "; ")
}

// ---------- tenants ----------

// DefaultQuota is applied to newly bootstrapped tenants.
type Quota struct {
	MaxNamespaces    int `json:"max_namespaces"`
	MaxItemsPerGroup int `json:"max_items_per_group"`
	VersionRetention int `json:"version_retention"`
}

var DefaultQuota = Quota{MaxNamespaces: 20, MaxItemsPerGroup: 200, VersionRetention: 100}

func (s *Service) CreateTenant(ctx context.Context, id, name string, q *Quota) (*domain.Tenant, error) {
	if id == "" {
		id = uuid.NewString()
	}
	if q == nil {
		q = &DefaultQuota
	}
	t := &domain.Tenant{
		ID: id, Name: name, MaxNamespaces: q.MaxNamespaces,
		MaxItemsPerGroup: q.MaxItemsPerGroup, VersionRetention: q.VersionRetention,
	}
	if err := s.store.CreateTenant(ctx, t); err != nil {
		return nil, err
	}
	// Every tenant gets a shared "_public" namespace hosting the public layer.
	pub := &domain.Namespace{ID: domain.PublicNamespaceID, TenantID: id, Name: "公共配置"}
	if err := s.store.CreateNamespace(ctx, pub); err != nil && err != store.ErrDuplicate {
		return nil, err
	}
	pg := &domain.Group{ID: domain.PublicNamespaceID, TenantID: id, NamespaceID: domain.PublicNamespaceID, Name: "公共配置"}
	if err := s.store.CreateGroup(ctx, pg); err != nil && err != store.ErrDuplicate {
		return nil, err
	}
	return t, nil
}

func (s *Service) GetTenant(ctx context.Context, id string) (*domain.Tenant, error) {
	return s.store.GetTenant(ctx, id)
}

func (s *Service) ListTenants(ctx context.Context) ([]*domain.Tenant, error) {
	return s.store.ListTenants(ctx)
}

func (s *Service) UpdateQuota(ctx context.Context, id string, q Quota) error {
	return s.store.UpdateTenantQuota(ctx, id, q.MaxNamespaces, q.MaxItemsPerGroup, q.VersionRetention)
}

// ---------- namespaces / groups ----------

func (s *Service) CreateNamespace(ctx context.Context, tenantID, id, name string) (*domain.Namespace, error) {
	t, err := s.store.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	count, err := s.store.CountNamespaces(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	// The system "_public" namespace is provisioned automatically and does
	// not consume the tenant's namespace quota.
	count--
	if count < 0 {
		count = 0
	}
	if count+1 > t.MaxNamespaces {
		return nil, fmt.Errorf("%w: namespace quota %d reached", store.ErrQuota, t.MaxNamespaces)
	}
	n := &domain.Namespace{ID: id, TenantID: tenantID, Name: name}
	if err := s.store.CreateNamespace(ctx, n); err != nil {
		return nil, err
	}
	// Namespace-wide defaults group ("namespace layer").
	g := &domain.Group{ID: domain.DefaultGroupID, TenantID: tenantID, NamespaceID: id, Name: "命名空间默认值"}
	if err := s.store.CreateGroup(ctx, g); err != nil && err != store.ErrDuplicate {
		return nil, err
	}
	return n, nil
}

func (s *Service) ListNamespaces(ctx context.Context, tenantID string) ([]*domain.Namespace, error) {
	return s.store.ListNamespaces(ctx, tenantID)
}

func (s *Service) CreateGroup(ctx context.Context, tenantID, namespaceID, id, name string) (*domain.Group, error) {
	if domain.IsSystemID(namespaceID) && id != domain.PublicNamespaceID {
		return nil, fmt.Errorf("cannot create groups inside the public namespace")
	}
	if _, err := s.store.GetNamespace(ctx, tenantID, namespaceID); err != nil {
		return nil, err
	}
	g := &domain.Group{ID: id, TenantID: tenantID, NamespaceID: namespaceID, Name: name}
	if err := s.store.CreateGroup(ctx, g); err != nil {
		return nil, err
	}
	return g, nil
}

func (s *Service) ListGroups(ctx context.Context, tenantID, namespaceID string) ([]*domain.Group, error) {
	return s.store.ListGroups(ctx, tenantID, namespaceID)
}

// ---------- items ----------

// CreateItemParams creates a configuration key in one of the three layers.
type CreateItemParams struct {
	TenantID    string
	NamespaceID string
	GroupID     string
	Key         string
	Format      domain.Format
	Schema      string
}

func (s *Service) CreateItem(ctx context.Context, p CreateItemParams) (*domain.Item, error) {
	if p.Key == "" {
		return nil, fmt.Errorf("key is required")
	}
	if !p.Format.Valid() {
		return nil, fmt.Errorf("unsupported format %q", p.Format)
	}
	// Resolve which layer this item belongs to.
	layer, err := s.resolveLayer(ctx, p.TenantID, p.NamespaceID, p.GroupID)
	if err != nil {
		return nil, err
	}
	// Namespace/group existence (system rows are provisioned implicitly).
	if err := s.assertContainer(ctx, p.TenantID, p.NamespaceID, p.GroupID); err != nil {
		return nil, err
	}
	// Quota on items per concrete group.
	t, err := s.store.GetTenant(ctx, p.TenantID)
	if err != nil {
		return nil, err
	}
	count, err := s.store.CountItems(ctx, p.TenantID, p.NamespaceID, p.GroupID)
	if err != nil {
		return nil, err
	}
	if count+1 > t.MaxItemsPerGroup {
		return nil, fmt.Errorf("%w: item quota %d reached for this group", store.ErrQuota, t.MaxItemsPerGroup)
	}
	// Format consistency across layers for the same key.
	if err := s.assertKeyFormat(ctx, p.TenantID, p.NamespaceID, p.GroupID, p.Key, p.Format); err != nil {
		return nil, err
	}
	// Schema document itself must compile.
	if strings.TrimSpace(p.Schema) != "" {
		if out := validator.ValidateSchemaText(p.Schema); !out.Valid {
			return nil, &ValidationFailure{Errors: out.Errors}
		}
	}
	item := &domain.Item{
		ID: uuid.NewString(), TenantID: p.TenantID, NamespaceID: p.NamespaceID,
		GroupID: p.GroupID, Layer: layer, Key: p.Key, Format: p.Format,
		Schema: p.Schema, Values: map[string]*domain.EnvValue{},
	}
	if err := s.store.CreateItem(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *Service) resolveLayer(_ context.Context, tenantID, namespaceID, groupID string) (domain.Layer, error) {
	switch {
	case namespaceID == domain.PublicNamespaceID && groupID == domain.PublicNamespaceID:
		return domain.LayerPublic, nil
	case groupID == domain.DefaultGroupID:
		return domain.LayerNamespace, nil
	case namespaceID != domain.PublicNamespaceID && !domain.IsSystemID(groupID):
		return domain.LayerGroup, nil
	default:
		return "", fmt.Errorf("invalid layer location ns=%q group=%q", namespaceID, groupID)
	}
}

func (s *Service) assertContainer(ctx context.Context, tenantID, nsID, groupID string) error {
	if _, err := s.store.GetNamespace(ctx, tenantID, nsID); err != nil {
		return err
	}
	if _, err := s.store.GetGroup(ctx, tenantID, nsID, groupID); err != nil {
		return err
	}
	return nil
}

func (s *Service) assertKeyFormat(ctx context.Context, tenantID, nsID, groupID, key string, f domain.Format) error {
	// Look for the same key in the other two layer containers of this tenant.
	candidates := [][2]string{
		{domain.PublicNamespaceID, domain.PublicNamespaceID},
		{nsID, domain.DefaultGroupID},
	}
	for _, c := range candidates {
		if c[0] == nsID && c[1] == groupID {
			continue
		}
		if other, err := s.store.GetItemByKey(ctx, tenantID, c[0], c[1], key); err == nil {
			if other.Format != f {
				return fmt.Errorf("key %q already exists as %s in another layer; formats must match", key, other.Format)
			}
		} else if err != store.ErrNotFound {
			return err
		}
	}
	return nil
}

func (s *Service) GetItem(ctx context.Context, tenantID, id string) (*domain.Item, error) {
	return s.store.GetItem(ctx, tenantID, id)
}

func (s *Service) ListItems(ctx context.Context, tenantID, nsID, groupID string) ([]*domain.Item, error) {
	return s.store.ListItems(ctx, tenantID, nsID, groupID)
}

// UpdateSchema replaces the JSON Schema attached to an item.
func (s *Service) UpdateSchema(ctx context.Context, tenantID, id, schema string) error {
	item, err := s.store.GetItem(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(schema) != "" {
		if out := validator.ValidateSchemaText(schema); !out.Valid {
			return &ValidationFailure{Errors: out.Errors}
		}
	}
	_ = item
	return s.store.UpdateItemSchema(ctx, tenantID, id, schema)
}

// ---------- value commits ----------

// CommitParams is a write to one item environment.
type CommitParams struct {
	TenantID        string
	ItemID          string
	Env             string
	Value           string
	Operator        string
	ExpectedVersion int64
	Gray            *GrayRequest
}

// GrayRequest asks for a canary delivery of the new value.
type GrayRequest struct {
	Strategy domain.GrayStrategy `json:"strategy"`
	IPs      []string            `json:"ips,omitempty"`
	Percent  int                 `json:"percent,omitempty"`
}

// CommitResult returns the new version and, when gray, the release.
type CommitResult struct {
	Version *domain.Version `json:"version"`
	Release *domain.Release `json:"release,omitempty"`
}

// Commit validates, writes a new immutable version, optionally opens a gray
// release, and broadcasts the change.
func (s *Service) Commit(ctx context.Context, p CommitParams) (*CommitResult, error) {
	item, err := s.store.GetItem(ctx, p.TenantID, p.ItemID)
	if err != nil {
		return nil, err
	}
	// 1) syntax + schema validation — invalid content never reaches the DB.
	if out := validator.Validate(item.Format, p.Value, item.Schema); !out.Valid {
		return nil, &ValidationFailure{Errors: out.Errors}
	}
	// 2) gray strategy sanity.
	if p.Gray != nil {
		switch p.Gray.Strategy {
		case domain.GrayIPList:
			if len(p.Gray.IPs) == 0 {
				return nil, fmt.Errorf("ip gray strategy requires a non-empty ip list")
			}
		case domain.GrayPercent:
			if p.Gray.Percent <= 0 || p.Gray.Percent > 100 {
				return nil, fmt.Errorf("percent gray strategy requires percent in 1..100")
			}
		default:
			return nil, fmt.Errorf("unknown gray strategy %q", p.Gray.Strategy)
		}
	}
	t, err := s.store.GetTenant(ctx, p.TenantID)
	if err != nil {
		return nil, err
	}
	changeType := "update"
	if p.ExpectedVersion == 0 {
		changeType = "create"
	}
	v, err := s.store.CommitValue(ctx, store.CommitValueParams{
		TenantID: p.TenantID, ItemID: p.ItemID, Env: p.Env, Value: p.Value,
		Operator: p.Operator, ChangeType: changeType, ExpectedVersion: p.ExpectedVersion,
		Retention: t.VersionRetention,
	})
	if err != nil {
		return nil, err
	}

	res := &CommitResult{Version: v}

	// 3) broadcast, optionally scoped by a gray release.
	if p.Gray != nil {
		r := &domain.Release{
			ID: uuid.NewString(), TenantID: p.TenantID, NamespaceID: item.NamespaceID,
			GroupID: item.GroupID, ItemID: item.ID, Env: p.Env,
			Version: v.Version, PrevVersion: p.ExpectedVersion,
			Strategy: p.Gray.Strategy, IPs: p.Gray.IPs, Percent: p.Gray.Percent,
			Status: domain.ReleaseGray, Operator: p.Operator, StartedAt: time.Now(),
		}
		if err := s.store.CreateRelease(ctx, r); err != nil {
			return nil, err
		}
		res.Release = r
		s.fanout(ctx, domain.PushEvent{
			Type: domain.EventChange, TenantID: p.TenantID, NamespaceID: item.NamespaceID,
			GroupID: item.GroupID, ItemID: item.ID, Key: item.Key, Env: p.Env,
			Version: v.Version, ReleaseID: r.ID,
		})
	} else {
		s.fanout(ctx, domain.PushEvent{
			Type: domain.EventChange, TenantID: p.TenantID, NamespaceID: item.NamespaceID,
			GroupID: item.GroupID, ItemID: item.ID, Key: item.Key, Env: p.Env,
			Version: v.Version,
		})
	}
	return res, nil
}

// fanout delivers an event to a business namespace and, for changes to the
// public layer, to every namespace of the tenant.
func (s *Service) fanout(ctx context.Context, ev domain.PushEvent) {
	if ev.NamespaceID != domain.PublicNamespaceID {
		_ = s.bus.Publish(ctx, ev)
		return
	}
	nss, err := s.store.ListNamespaces(ctx, ev.TenantID)
	if err != nil {
		return
	}
	for _, ns := range nss {
		cp := ev
		cp.NamespaceID = ns.ID
		cp.GroupID = domain.PublicNamespaceID
		_ = s.bus.Publish(ctx, cp)
	}
}

// ---------- versions / rollback / diff ----------

func (s *Service) ListVersions(ctx context.Context, tenantID, itemID, env string, limit int) ([]*domain.Version, error) {
	return s.store.ListVersions(ctx, tenantID, itemID, env, limit)
}

func (s *Service) Diff(ctx context.Context, tenantID, itemID, env string, from, to int64) ([]domain.DiffLine, *domain.Version, *domain.Version, error) {
	a, err := s.store.GetVersion(ctx, tenantID, itemID, env, from)
	if err != nil {
		return nil, nil, nil, err
	}
	b, err := s.store.GetVersion(ctx, tenantID, itemID, env, to)
	if err != nil {
		return nil, nil, nil, err
	}
	return version.Diff(a.Value, b.Value), a, b, nil
}

// Rollback re-commits the value of an arbitrary historical version as a new
// version. Nothing in between is erased.
func (s *Service) Rollback(ctx context.Context, tenantID, itemID, env string, target int64, operator string) (*CommitResult, error) {
	old, err := s.store.GetVersion(ctx, tenantID, itemID, env, target)
	if err != nil {
		return nil, err
	}
	item, err := s.store.GetItem(ctx, tenantID, itemID)
	if err != nil {
		return nil, err
	}
	current := int64(0)
	if ev, ok := item.Values[env]; ok {
		current = ev.Version
	}
	t, err := s.store.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	v, err := s.store.CommitValue(ctx, store.CommitValueParams{
		TenantID: tenantID, ItemID: itemID, Env: env, Value: old.Value,
		Operator: operator, ChangeType: "rollback",
		Note:            fmt.Sprintf("回退到版本 %d", target),
		ExpectedVersion: current, Retention: t.VersionRetention,
	})
	if err != nil {
		return nil, err
	}
	s.fanout(ctx, domain.PushEvent{
		Type: domain.EventChange, TenantID: tenantID, NamespaceID: item.NamespaceID,
		GroupID: item.GroupID, ItemID: itemID, Key: item.Key, Env: env, Version: v.Version,
	})
	return &CommitResult{Version: v}, nil
}

// ---------- gray lifecycle ----------

func (s *Service) GetRelease(ctx context.Context, tenantID, id string) (*domain.ReleaseView, error) {
	r, err := s.store.GetRelease(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	delivered, total := s.hub.ReleaseStats(tenantID, r.NamespaceID, r.ID)
	return &domain.ReleaseView{Release: *r, DeliveredCount: delivered, TotalInstances: total}, nil
}

func (s *Service) ListReleases(ctx context.Context, tenantID, namespaceID string, limit int) ([]*domain.Release, error) {
	return s.store.ListReleases(ctx, tenantID, namespaceID, limit)
}

// Promote ends the observation window and pushes the gray value to everyone.
func (s *Service) Promote(ctx context.Context, tenantID, releaseID, operator string) (*domain.Release, error) {
	r, err := s.store.GetRelease(ctx, tenantID, releaseID)
	if err != nil {
		return nil, err
	}
	if r.Status != domain.ReleaseGray {
		return nil, fmt.Errorf("release %s is not in gray status", releaseID)
	}
	now := time.Now()
	r.Status = domain.ReleasePromoted
	r.PromotedAt = &now
	if err := s.store.UpdateRelease(ctx, r); err != nil {
		return nil, err
	}
	s.fanout(ctx, domain.PushEvent{
		Type: domain.EventPromote, TenantID: tenantID, NamespaceID: r.NamespaceID,
		GroupID: r.GroupID, ItemID: r.ItemID, Env: r.Env, Version: r.Version,
		ReleaseID: r.ID,
	})
	return r, nil
}

// GrayRollback withdraws a gray release: the previous value is re-committed as
// a new version and only instances that had received the gray value are woken
// to pull back. Instances outside the gray never left the old value.
func (s *Service) GrayRollback(ctx context.Context, tenantID, releaseID, operator string) (*domain.Release, *domain.Version, error) {
	r, err := s.store.GetRelease(ctx, tenantID, releaseID)
	if err != nil {
		return nil, nil, err
	}
	if r.Status != domain.ReleaseGray {
		return nil, nil, fmt.Errorf("release %s is not in gray status", releaseID)
	}
	prev, err := s.store.GetVersion(ctx, tenantID, r.ItemID, r.Env, r.PrevVersion)
	if err != nil {
		return nil, nil, err
	}
	item, err := s.store.GetItem(ctx, tenantID, r.ItemID)
	if err != nil {
		return nil, nil, err
	}
	t, err := s.store.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, nil, err
	}
	v, err := s.store.CommitValue(ctx, store.CommitValueParams{
		TenantID: tenantID, ItemID: r.ItemID, Env: r.Env, Value: prev.Value,
		Operator: operator, ChangeType: "gray_rollback",
		Note:            fmt.Sprintf("灰度 %s 观察失败，撤回版本 %d", releaseID, r.Version),
		ExpectedVersion: r.Version, Retention: t.VersionRetention,
	})
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	r.Status = domain.ReleaseRolledBack
	r.EndedAt = &now
	if err := s.store.UpdateRelease(ctx, r); err != nil {
		return nil, nil, err
	}
	_ = item
	s.fanout(ctx, domain.PushEvent{
		Type: domain.EventRollback, TenantID: tenantID, NamespaceID: r.NamespaceID,
		GroupID: r.GroupID, ItemID: r.ItemID, Env: r.Env, Version: v.Version,
		ReleaseID: r.ID,
	})
	return r, v, nil
}
