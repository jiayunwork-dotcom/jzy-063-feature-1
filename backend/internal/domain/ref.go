package domain

// RefTarget is the location a placeholder points at. Empty NamespaceID /
// GroupID mean "resolve in the current resolution context" (a bare key).
// Empty Env means "same environment as the referencing value".
type RefTarget struct {
	NamespaceID string `json:"namespace_id,omitempty"`
	GroupID     string `json:"group_id,omitempty"`
	Key         string `json:"key"`
	Env         string `json:"env,omitempty"`
}

// Qualified reports whether the target pins its own namespace/group.
func (t RefTarget) Qualified() bool { return t.NamespaceID != "" }

// RefEdge is one persisted placeholder of one item in one environment. The
// graph of RefEdges is the long-lived "who references whom" dependency map;
// it is replaced atomically whenever a new value version is committed.
type RefEdge struct {
	TenantID string    `json:"tenant_id"`
	ItemID   string    `json:"item_id"`
	Env      string    `json:"env"`
	Seq      int       `json:"seq"`
	Raw      string    `json:"raw"` // placeholder text as authored, e.g. "@{ns/g/k?env=prod}"
	Target   RefTarget `json:"target"`
}

// RefNode is one vertex of the dependency graph as shown by the console.
type RefNode struct {
	ItemID      string `json:"item_id"`
	NamespaceID string `json:"namespace_id"`
	GroupID     string `json:"group_id"`
	Key         string `json:"key"`
	Layer       Layer  `json:"layer"`
	Format      Format `json:"format"`
	Env         string `json:"env,omitempty"`
}
