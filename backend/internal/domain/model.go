// Package domain defines the core data model of the configuration platform.
package domain

import "time"

// Format is the serialization format of a configuration value.
type Format string

const (
	FormatJSON       Format = "json"
	FormatYAML       Format = "yaml"
	FormatProperties Format = "properties"
	FormatTOML       Format = "toml"
)

// SupportedFormats lists every accepted configuration format.
var SupportedFormats = []Format{FormatJSON, FormatYAML, FormatProperties, FormatTOML}

func (f Format) Valid() bool {
	for _, x := range SupportedFormats {
		if x == f {
			return true
		}
	}
	return false
}

// Layer is one of the three merge layers. Lower numbers are higher priority
// during merge (group overrides namespace overrides public).
type Layer string

const (
	LayerPublic    Layer = "public"
	LayerNamespace Layer = "namespace"
	LayerGroup     Layer = "group"
)

// LayerPriority maps a layer to its override priority. The group layer is
// strongest; the public layer is weakest.
var LayerPriority = map[Layer]int{
	LayerPublic:    0,
	LayerNamespace: 1,
	LayerGroup:     2,
}

// Reserved identifiers for the shared layers.
const (
	// PublicNamespaceID is the virtual namespace that hosts the shared
	// ("common") layer readable by every group of the tenant.
	PublicNamespaceID = "_public"
	// DefaultGroupID is the group holding namespace-layer ("namespace-wide")
	// values inside any normal namespace.
	DefaultGroupID = "_defaults"
)

// IsSystemID reports whether an id is reserved for the shared layers.
func IsSystemID(s string) bool {
	return s == PublicNamespaceID || s == DefaultGroupID
}

// ReleaseStatus tracks the lifecycle of a gray (canary) release.
type ReleaseStatus string

const (
	ReleaseGray       ReleaseStatus = "gray"        // only selected instances got it
	ReleasePromoted   ReleaseStatus = "promoted"    // manually pushed to everyone
	ReleaseRolledBack ReleaseStatus = "rolled_back" // withdrawn during observation
)

// GrayStrategy decides which instances receive a gray release.
type GrayStrategy string

const (
	// GrayIPList delivers to instances whose IP is explicitly listed.
	GrayIPList GrayStrategy = "ip"
	// GrayPercent delivers deterministically to a given percentage of
	// instances (hash-routed so the set is stable).
	GrayPercent GrayStrategy = "percent"
)

// Tenant is an isolated customer. Quotas are enforced per tenant.
type Tenant struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	MaxNamespaces    int       `json:"max_namespaces"`
	MaxItemsPerGroup int       `json:"max_items_per_group"`
	VersionRetention int       `json:"version_retention"`
	CreatedAt        time.Time `json:"created_at"`
}

// Namespace corresponds to one business line.
type Namespace struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Group corresponds to one service module inside a namespace.
type Group struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	NamespaceID string    `json:"namespace_id"`
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"created_at"`
}

// EnvValue is the value of one item in one environment.
type EnvValue struct {
	Value     string    `json:"value"`
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// Item is a single configuration entry, uniquely located by
// namespace + group + key (+ tenant). Each environment has its own value.
type Item struct {
	ID          string               `json:"id"`
	TenantID    string               `json:"tenant_id"`
	NamespaceID string               `json:"namespace_id"`
	GroupID     string               `json:"group_id"`
	Layer       Layer                `json:"layer"`
	Key         string               `json:"key"`
	Format      Format               `json:"format"`
	Schema      string               `json:"schema,omitempty"`
	Values      map[string]*EnvValue `json:"values"` // env label -> value
	CreatedAt   time.Time            `json:"created_at"`
	UpdatedAt   time.Time            `json:"updated_at"`
}

// Version records one historical value of an item in one environment.
type Version struct {
	ID         int64     `json:"id"`
	ItemID     string    `json:"item_id"`
	TenantID   string    `json:"tenant_id"`
	Env        string    `json:"env"`
	Version    int64     `json:"version"`
	Value      string    `json:"value"`
	Operator   string    `json:"operator"`
	ChangeType string    `json:"change_type"` // create | update | rollback | gray_rollback
	Note       string    `json:"note,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// DiffOp is the kind of a line diff entry.
type DiffOp string

const (
	DiffEqual  DiffOp = "equal"
	DiffAdd    DiffOp = "add"
	DiffDelete DiffOp = "delete"
)

// DiffLine is one row of a line-level diff between two versions.
type DiffLine struct {
	Op    DiffOp `json:"op"`
	Left  string `json:"left,omitempty"`
	Right string `json:"right,omitempty"`
}

// Release is a gray/full push of one item's new value.
type Release struct {
	ID          string        `json:"id"`
	TenantID    string        `json:"tenant_id"`
	NamespaceID string        `json:"namespace_id"`
	GroupID     string        `json:"group_id"`
	ItemID      string        `json:"item_id"`
	Env         string        `json:"env"`
	Version     int64         `json:"version"` // version being delivered
	PrevVersion int64         `json:"prev_version"`
	Strategy    GrayStrategy  `json:"strategy"`
	IPs         []string      `json:"ips,omitempty"`
	Percent     int           `json:"percent,omitempty"`
	Status      ReleaseStatus `json:"status"`
	Operator    string        `json:"operator"`
	StartedAt   time.Time     `json:"started_at"`
	PromotedAt  *time.Time    `json:"promoted_at,omitempty"`
	EndedAt     *time.Time    `json:"ended_at,omitempty"`
}

// ReleaseView adds live delivery counters for the gray panel.
type ReleaseView struct {
	Release
	DeliveredCount int `json:"delivered_count"` // selected instances that got it
	TotalInstances int `json:"total_instances"` // currently connected instances
}

// PushEventKind distinguishes ordinary changes from gray lifecycle events.
type PushEventKind string

const (
	EventChange   PushEventKind = "change"        // a new value (possibly gray)
	EventRollback PushEventKind = "gray_rollback" // a gray release withdrawn
	EventPromote  PushEventKind = "promote"       // gray release promoted to all
)

// PushEvent is a configuration change broadcast through Redis to every server
// node and from there to waiting long-polls and websocket clients.
type PushEvent struct {
	Type        PushEventKind `json:"type,omitempty"`
	TenantID    string        `json:"tenant_id"`
	NamespaceID string        `json:"namespace_id"`
	GroupID     string        `json:"group_id"`
	ItemID      string        `json:"item_id"`
	Key         string        `json:"key,omitempty"`
	Env         string        `json:"env"`
	Version     int64         `json:"version"`
	ReleaseID   string        `json:"release_id,omitempty"`
}

// EffectiveItem is one key of a merged configuration with its source layer.
type EffectiveItem struct {
	Key       string `json:"key"`
	Format    Format `json:"format"`
	Value     string `json:"value"`
	Source    Layer  `json:"source"`
	SourceVer int64  `json:"source_version"`
}

// NamespaceStats feeds the connection / recent-push dashboard.
type NamespaceStats struct {
	NamespaceID string     `json:"namespace_id"`
	Connections int        `json:"connections"`
	LastPushAt  *time.Time `json:"last_push_at,omitempty"`
	LastPushEnv string     `json:"last_push_env,omitempty"`
	LastVersion int64      `json:"last_version,omitempty"`
}
