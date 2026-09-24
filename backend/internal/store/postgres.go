package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"

	"configcenter/internal/domain"
)

// Postgres is the production Store backed by PostgreSQL 16.
type Postgres struct {
	db *sql.DB
}

// NewPostgres opens the connection pool and verifies it.
func NewPostgres(dsn string) (*Postgres, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return &Postgres{db: db}, nil
}

func (p *Postgres) DB() *sql.DB { return p.db }

func (p *Postgres) Ping(ctx context.Context) error {
	return p.db.PingContext(ctx)
}

func (p *Postgres) Close() error { return p.db.Close() }

func isUniqueViolation(err error) bool {
	var pe *pq.Error
	if errors.As(err, &pe) {
		return pe.Code == "23505"
	}
	return false
}

func (p *Postgres) CreateTenant(ctx context.Context, t *domain.Tenant) error {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now()
	}
	err := p.db.QueryRowContext(ctx,
		`INSERT INTO tenants (id, name, max_namespaces, max_items_per_group, version_retention, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6) RETURNING created_at`,
		t.ID, t.Name, t.MaxNamespaces, t.MaxItemsPerGroup, t.VersionRetention, t.CreatedAt).
		Scan(&t.CreatedAt)
	if isUniqueViolation(err) {
		return ErrDuplicate
	}
	return err
}

func (p *Postgres) GetTenant(ctx context.Context, id string) (*domain.Tenant, error) {
	t := &domain.Tenant{}
	err := p.db.QueryRowContext(ctx,
		`SELECT id, name, max_namespaces, max_items_per_group, version_retention, created_at
		 FROM tenants WHERE id=$1`, id).
		Scan(&t.ID, &t.Name, &t.MaxNamespaces, &t.MaxItemsPerGroup, &t.VersionRetention, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func (p *Postgres) ListTenants(ctx context.Context) ([]*domain.Tenant, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, name, max_namespaces, max_items_per_group, version_retention, created_at
		 FROM tenants ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Tenant
	for rows.Next() {
		t := &domain.Tenant{}
		if err := rows.Scan(&t.ID, &t.Name, &t.MaxNamespaces, &t.MaxItemsPerGroup,
			&t.VersionRetention, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (p *Postgres) UpdateTenantQuota(ctx context.Context, id string, maxNs, maxItems, retention int) error {
	res, err := p.db.ExecContext(ctx,
		`UPDATE tenants SET max_namespaces=$2, max_items_per_group=$3, version_retention=$4 WHERE id=$1`,
		id, maxNs, maxItems, retention)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) CreateNamespace(ctx context.Context, n *domain.Namespace) error {
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now()
	}
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO namespaces (id, tenant_id, name, created_at) VALUES ($1,$2,$3,$4)`,
		n.ID, n.TenantID, n.Name, n.CreatedAt)
	if isUniqueViolation(err) {
		return ErrDuplicate
	}
	return err
}

func (p *Postgres) GetNamespace(ctx context.Context, tenantID, id string) (*domain.Namespace, error) {
	n := &domain.Namespace{}
	err := p.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, created_at FROM namespaces WHERE tenant_id=$1 AND id=$2`,
		tenantID, id).
		Scan(&n.ID, &n.TenantID, &n.Name, &n.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return n, err
}

func (p *Postgres) ListNamespaces(ctx context.Context, tenantID string) ([]*domain.Namespace, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, tenant_id, name, created_at FROM namespaces WHERE tenant_id=$1 ORDER BY id`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Namespace
	for rows.Next() {
		n := &domain.Namespace{}
		if err := rows.Scan(&n.ID, &n.TenantID, &n.Name, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (p *Postgres) CountNamespaces(ctx context.Context, tenantID string) (int, error) {
	var c int
	err := p.db.QueryRowContext(ctx,
		`SELECT count(*) FROM namespaces WHERE tenant_id=$1`, tenantID).Scan(&c)
	return c, err
}

func (p *Postgres) CreateGroup(ctx context.Context, g *domain.Group) error {
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now()
	}
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO groups (id, tenant_id, namespace_id, name, created_at) VALUES ($1,$2,$3,$4,$5)`,
		g.ID, g.TenantID, g.NamespaceID, g.Name, g.CreatedAt)
	if isUniqueViolation(err) {
		return ErrDuplicate
	}
	return err
}

func (p *Postgres) GetGroup(ctx context.Context, tenantID, namespaceID, id string) (*domain.Group, error) {
	g := &domain.Group{}
	err := p.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, namespace_id, name, created_at FROM groups
		 WHERE tenant_id=$1 AND namespace_id=$2 AND id=$3`,
		tenantID, namespaceID, id).
		Scan(&g.ID, &g.TenantID, &g.NamespaceID, &g.Name, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return g, err
}

func (p *Postgres) ListGroups(ctx context.Context, tenantID, namespaceID string) ([]*domain.Group, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, tenant_id, namespace_id, name, created_at FROM groups
		 WHERE tenant_id=$1 AND namespace_id=$2 ORDER BY id`, tenantID, namespaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Group
	for rows.Next() {
		g := &domain.Group{}
		if err := rows.Scan(&g.ID, &g.TenantID, &g.NamespaceID, &g.Name, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (p *Postgres) CreateItem(ctx context.Context, item *domain.Item) error {
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.CreatedAt
	}
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO items (id, tenant_id, namespace_id, group_id, layer, key, format, schema, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		item.ID, item.TenantID, item.NamespaceID, item.GroupID,
		string(item.Layer), item.Key, string(item.Format), item.Schema,
		item.CreatedAt, item.UpdatedAt)
	if isUniqueViolation(err) {
		return ErrDuplicate
	}
	return err
}

const itemColumns = `id, tenant_id, namespace_id, group_id, layer, key, format, schema, created_at, updated_at`

func scanItem(row interface {
	Scan(...any) error
}) (*domain.Item, error) {
	item := &domain.Item{Values: map[string]*domain.EnvValue{}}
	var layer, format string
	if err := row.Scan(&item.ID, &item.TenantID, &item.NamespaceID, &item.GroupID,
		&layer, &item.Key, &format, &item.Schema, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return nil, err
	}
	item.Layer = domain.Layer(layer)
	item.Format = domain.Format(format)
	return item, nil
}

func (p *Postgres) GetItem(ctx context.Context, tenantID, id string) (*domain.Item, error) {
	row := p.db.QueryRowContext(ctx,
		"SELECT "+itemColumns+" FROM items WHERE tenant_id=$1 AND id=$2", tenantID, id)
	item, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := p.fillValues(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

func (p *Postgres) GetItemByKey(ctx context.Context, tenantID, namespaceID, groupID, key string) (*domain.Item, error) {
	row := p.db.QueryRowContext(ctx,
		"SELECT "+itemColumns+
			" FROM items WHERE tenant_id=$1 AND namespace_id=$2 AND group_id=$3 AND key=$4",
		tenantID, namespaceID, groupID, key)
	item, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := p.fillValues(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

func (p *Postgres) fillValues(ctx context.Context, item *domain.Item) error {
	rows, err := p.db.QueryContext(ctx,
		`SELECT env, version, value, updated_by, updated_at FROM item_values WHERE item_id=$1`, item.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var env string
		ev := &domain.EnvValue{}
		if err := rows.Scan(&env, &ev.Version, &ev.Value, &ev.UpdatedBy, &ev.UpdatedAt); err != nil {
			return err
		}
		item.Values[env] = ev
	}
	return rows.Err()
}

func (p *Postgres) ListItems(ctx context.Context, tenantID, namespaceID, groupID string) ([]*domain.Item, error) {
	rows, err := p.db.QueryContext(ctx,
		"SELECT "+itemColumns+" FROM items WHERE tenant_id=$1 AND namespace_id=$2 AND group_id=$3 ORDER BY key",
		tenantID, namespaceID, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, item := range out {
		if err := p.fillValues(ctx, item); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (p *Postgres) CountItems(ctx context.Context, tenantID, namespaceID, groupID string) (int, error) {
	var c int
	err := p.db.QueryRowContext(ctx,
		`SELECT count(*) FROM items WHERE tenant_id=$1 AND namespace_id=$2 AND group_id=$3`,
		tenantID, namespaceID, groupID).Scan(&c)
	return c, err
}

func (p *Postgres) UpdateItemSchema(ctx context.Context, tenantID, id, schema string) error {
	res, err := p.db.ExecContext(ctx,
		`UPDATE items SET schema=$3, updated_at=now() WHERE tenant_id=$1 AND id=$2`,
		tenantID, id, schema)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// CommitValue runs the version increment + retention prune atomically.
func (p *Postgres) CommitValue(ctx context.Context, in CommitValueParams) (*domain.Version, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var current int64
	err = tx.QueryRowContext(ctx,
		`SELECT version FROM item_values WHERE item_id=$1 AND env=$2 FOR UPDATE`,
		in.ItemID, in.Env).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		current = 0
	} else if err != nil {
		return nil, err
	}
	if in.ExpectedVersion != current {
		return nil, ErrConflict
	}

	var tenantCheck string
	if err := tx.QueryRowContext(ctx,
		`SELECT tenant_id FROM items WHERE id=$1 AND tenant_id=$2`,
		in.ItemID, in.TenantID).Scan(&tenantCheck); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	newVersion := current + 1
	if current == 0 {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO item_values (item_id, tenant_id, env, version, value, updated_by, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6, now())`,
			in.ItemID, in.TenantID, in.Env, newVersion, in.Value, in.Operator)
	} else {
		_, err = tx.ExecContext(ctx,
			`UPDATE item_values SET version=$4, value=$5, updated_by=$6, updated_at=now()
			 WHERE item_id=$1 AND env=$2 AND version=$3`,
			in.ItemID, in.Env, current, newVersion, in.Value, in.Operator)
	}
	if err != nil {
		return nil, err
	}

	v := &domain.Version{
		ItemID:     in.ItemID,
		TenantID:   in.TenantID,
		Env:        in.Env,
		Version:    newVersion,
		Value:      in.Value,
		Operator:   in.Operator,
		ChangeType: in.ChangeType,
		Note:       in.Note,
		CreatedAt:  time.Now(),
	}
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO versions (item_id, tenant_id, env, version, value, operator, change_type, note, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
		v.ItemID, v.TenantID, v.Env, v.Version, v.Value, v.Operator, v.ChangeType, v.Note, v.CreatedAt).
		Scan(&v.ID); err != nil {
		return nil, err
	}

	if in.Retention > 0 {
		// Keep exactly the newest Retention rows; delete anything older.
		_, _ = tx.ExecContext(ctx,
			`DELETE FROM versions WHERE item_id=$1 AND env=$2 AND id NOT IN (
				SELECT id FROM versions WHERE item_id=$1 AND env=$2
				ORDER BY version DESC LIMIT $3
			)`,
			in.ItemID, in.Env, in.Retention)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return v, nil
}

func (p *Postgres) ListVersions(ctx context.Context, tenantID, itemID, env string, limit int) ([]*domain.Version, error) {
	q := `SELECT id, item_id, tenant_id, env, version, value, operator, change_type, note, created_at
	      FROM versions WHERE tenant_id=$1 AND item_id=$2 AND env=$3 ORDER BY version DESC`
	args := []any{tenantID, itemID, env}
	if limit > 0 {
		q += " LIMIT $4"
		args = append(args, limit)
	}
	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Version
	for rows.Next() {
		v := &domain.Version{}
		if err := rows.Scan(&v.ID, &v.ItemID, &v.TenantID, &v.Env, &v.Version,
			&v.Value, &v.Operator, &v.ChangeType, &v.Note, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (p *Postgres) GetVersion(ctx context.Context, tenantID, itemID, env string, ver int64) (*domain.Version, error) {
	v := &domain.Version{}
	err := p.db.QueryRowContext(ctx,
		`SELECT id, item_id, tenant_id, env, version, value, operator, change_type, note, created_at
		 FROM versions WHERE tenant_id=$1 AND item_id=$2 AND env=$3 AND version=$4`,
		tenantID, itemID, env, ver).
		Scan(&v.ID, &v.ItemID, &v.TenantID, &v.Env, &v.Version,
			&v.Value, &v.Operator, &v.ChangeType, &v.Note, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return v, err
}

func (p *Postgres) CreateRelease(ctx context.Context, r *domain.Release) error {
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now()
	}
	ips, _ := json.Marshal(r.IPs)
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO releases (id, tenant_id, namespace_id, group_id, item_id, env, version, prev_version,
			strategy, ips, percent, status, operator, started_at, promoted_at, ended_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		r.ID, r.TenantID, r.NamespaceID, r.GroupID, r.ItemID, r.Env, r.Version, r.PrevVersion,
		string(r.Strategy), ips, r.Percent, string(r.Status), r.Operator, r.StartedAt,
		nilTime(r.PromotedAt), nilTime(r.EndedAt))
	if isUniqueViolation(err) {
		return ErrDuplicate
	}
	return err
}

func nilTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

func scanRelease(row interface {
	Scan(...any) error
}) (*domain.Release, error) {
	r := &domain.Release{}
	var strategy, status string
	var ips []byte
	var promotedAt, endedAt sql.NullTime
	if err := row.Scan(&r.ID, &r.TenantID, &r.NamespaceID, &r.GroupID, &r.ItemID,
		&r.Env, &r.Version, &r.PrevVersion, &strategy, &ips, &r.Percent,
		&status, &r.Operator, &r.StartedAt, &promotedAt, &endedAt); err != nil {
		return nil, err
	}
	r.Strategy = domain.GrayStrategy(strategy)
	r.Status = domain.ReleaseStatus(status)
	_ = json.Unmarshal(ips, &r.IPs)
	if promotedAt.Valid {
		r.PromotedAt = &promotedAt.Time
	}
	if endedAt.Valid {
		r.EndedAt = &endedAt.Time
	}
	return r, nil
}

const releaseColumns = `id, tenant_id, namespace_id, group_id, item_id, env, version, prev_version,
	strategy, ips, percent, status, operator, started_at, promoted_at, ended_at`

func (p *Postgres) GetRelease(ctx context.Context, tenantID, id string) (*domain.Release, error) {
	row := p.db.QueryRowContext(ctx,
		"SELECT "+releaseColumns+" FROM releases WHERE tenant_id=$1 AND id=$2", tenantID, id)
	r, err := scanRelease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

func (p *Postgres) UpdateRelease(ctx context.Context, r *domain.Release) error {
	ips, _ := json.Marshal(r.IPs)
	res, err := p.db.ExecContext(ctx,
		`UPDATE releases SET group_id=$3, item_id=$4, env=$5, version=$6, prev_version=$7,
			strategy=$8, ips=$9, percent=$10, status=$11, promoted_at=$12, ended_at=$13
		 WHERE id=$1 AND tenant_id=$2`,
		r.ID, r.TenantID, r.GroupID, r.ItemID, r.Env, r.Version, r.PrevVersion,
		string(r.Strategy), ips, r.Percent, string(r.Status),
		nilTime(r.PromotedAt), nilTime(r.EndedAt))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) ListReleases(ctx context.Context, tenantID, namespaceID string, limit int) ([]*domain.Release, error) {
	q := "SELECT " + releaseColumns + " FROM releases WHERE tenant_id=$1 AND namespace_id=$2 ORDER BY started_at DESC"
	args := []any{tenantID, namespaceID}
	if limit > 0 {
		q += " LIMIT $3"
		args = append(args, limit)
	}
	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Release
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, fmt.Errorf("scan release: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *Postgres) ActiveReleases(ctx context.Context, tenantID, namespaceID string) ([]*domain.Release, error) {
	rows, err := p.db.QueryContext(ctx,
		"SELECT "+releaseColumns+
			" FROM releases WHERE tenant_id=$1 AND namespace_id=$2 AND status='gray' ORDER BY started_at",
		tenantID, namespaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Release
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
