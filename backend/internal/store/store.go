// Package store defines persistence contracts and provides in-memory and
// PostgreSQL implementations. Every query is tenant-scoped so cross-tenant
// access is impossible at the data layer.
package store

import (
	"context"
	"errors"

	"configcenter/internal/domain"
)

// ErrNotFound is returned when a row does not exist or belongs to another
// tenant.
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned on stale version writes.
var ErrConflict = errors.New("store: version conflict")

// ErrQuota is returned when a tenant quota would be exceeded.
var ErrQuota = errors.New("store: quota exceeded")

// ErrDuplicate is returned on unique constraint violations.
var ErrDuplicate = errors.New("store: duplicate key")

// CommitValueParams atomically advances one item's environment value and
// appends a historical version. Refs replaces the raw reference edges of the
// item+env, so every commit re-declares the full outgoing set.
type CommitValueParams struct {
	TenantID        string
	ItemID          string
	Env             string
	Value           string
	Refs            []*domain.RawRef
	Operator        string
	ChangeType      string
	Note            string
	ExpectedVersion int64 // current version, or 0 if the env must be empty
	Retention       int   // max versions to keep for this item+env
}

// Store is the full persistence contract of the platform.
type Store interface {
	Ping(ctx context.Context) error

	CreateTenant(ctx context.Context, t *domain.Tenant) error
	GetTenant(ctx context.Context, id string) (*domain.Tenant, error)
	ListTenants(ctx context.Context) ([]*domain.Tenant, error)
	UpdateTenantQuota(ctx context.Context, id string, maxNs, maxItems, retention int) error

	CreateNamespace(ctx context.Context, n *domain.Namespace) error
	GetNamespace(ctx context.Context, tenantID, id string) (*domain.Namespace, error)
	ListNamespaces(ctx context.Context, tenantID string) ([]*domain.Namespace, error)
	CountNamespaces(ctx context.Context, tenantID string) (int, error)

	CreateGroup(ctx context.Context, g *domain.Group) error
	GetGroup(ctx context.Context, tenantID, namespaceID, id string) (*domain.Group, error)
	ListGroups(ctx context.Context, tenantID, namespaceID string) ([]*domain.Group, error)

	CreateItem(ctx context.Context, item *domain.Item) error
	GetItem(ctx context.Context, tenantID, id string) (*domain.Item, error)
	GetItemByKey(ctx context.Context, tenantID, namespaceID, groupID, key string) (*domain.Item, error)
	ListItems(ctx context.Context, tenantID, namespaceID, groupID string) ([]*domain.Item, error)
	// ListAllItems returns every item of a tenant, used to project the
	// tenant-wide reference dependency graph.
	ListAllItems(ctx context.Context, tenantID string) ([]*domain.Item, error)
	CountItems(ctx context.Context, tenantID, namespaceID, groupID string) (int, error)
	UpdateItemSchema(ctx context.Context, tenantID, id, schema string) error
	CommitValue(ctx context.Context, p CommitValueParams) (*domain.Version, error)

	ListVersions(ctx context.Context, tenantID, itemID, env string, limit int) ([]*domain.Version, error)
	GetVersion(ctx context.Context, tenantID, itemID, env string, version int64) (*domain.Version, error)

	CreateRelease(ctx context.Context, r *domain.Release) error
	GetRelease(ctx context.Context, tenantID, id string) (*domain.Release, error)
	UpdateRelease(ctx context.Context, r *domain.Release) error
	ListReleases(ctx context.Context, tenantID, namespaceID string, limit int) ([]*domain.Release, error)
	// ActiveReleases returns every release still in gray status for a
	// namespace. It backs the serving-time gate so non-selected instances
	// keep receiving the previous value.
	ActiveReleases(ctx context.Context, tenantID, namespaceID string) ([]*domain.Release, error)
	// ActiveReleasesAll returns every gray release of a tenant across all
	// namespaces, so cross-namespace references inherit gray visibility.
	ActiveReleasesAll(ctx context.Context, tenantID string) ([]*domain.Release, error)

	// ReplaceItemRefs atomically sets the outgoing reference edges of one
	// item+env (an empty slice removes them all).
	ReplaceItemRefs(ctx context.Context, tenantID, itemID, env string, refs []*domain.RawRef) error
	// ListItemRefs returns the outgoing edges of one item+env.
	ListItemRefs(ctx context.Context, tenantID, itemID, env string) ([]*domain.RawRef, error)
	// ListAllItemRefs returns every raw edge of a tenant, used to project the
	// concrete dependency graph.
	ListAllItemRefs(ctx context.Context, tenantID string) ([]*domain.RawRef, error)

	Close() error
}
