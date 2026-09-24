-- PostgreSQL 16 schema for the configuration governance platform.

CREATE TABLE IF NOT EXISTS tenants (
    id                 TEXT PRIMARY KEY,
    name               TEXT NOT NULL,
    max_namespaces     INTEGER NOT NULL DEFAULT 10,
    max_items_per_group INTEGER NOT NULL DEFAULT 100,
    version_retention  INTEGER NOT NULL DEFAULT 100,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS namespaces (
    id         TEXT NOT NULL,
    tenant_id  TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS groups (
    id           TEXT NOT NULL,
    tenant_id    TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    namespace_id TEXT NOT NULL,
    name         TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, namespace_id, id),
    CONSTRAINT groups_ns_fk FOREIGN KEY (tenant_id, namespace_id)
        REFERENCES namespaces(tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS items (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    namespace_id TEXT NOT NULL,
    group_id     TEXT NOT NULL,
    layer        TEXT NOT NULL CHECK (layer IN ('public', 'namespace', 'group')),
    key          TEXT NOT NULL,
    format       TEXT NOT NULL CHECK (format IN ('json', 'yaml', 'properties', 'toml')),
    schema       TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, namespace_id, group_id, key)
);

CREATE INDEX IF NOT EXISTS idx_items_group ON items(tenant_id, namespace_id, group_id);

-- One row per environment value; values without an env row do not exist yet.
CREATE TABLE IF NOT EXISTS item_values (
    item_id    TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    tenant_id  TEXT NOT NULL,
    env        TEXT NOT NULL,
    version    BIGINT NOT NULL,
    value      TEXT NOT NULL,
    updated_by TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (item_id, env)
);

CREATE SEQUENCE IF NOT EXISTS versions_id_seq;

CREATE TABLE IF NOT EXISTS versions (
    id          BIGINT PRIMARY KEY DEFAULT nextval('versions_id_seq'),
    item_id     TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    tenant_id   TEXT NOT NULL,
    env         TEXT NOT NULL,
    version     BIGINT NOT NULL,
    value       TEXT NOT NULL,
    operator    TEXT NOT NULL DEFAULT '',
    change_type TEXT NOT NULL,
    note        TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (item_id, env, version)
);

CREATE INDEX IF NOT EXISTS idx_versions_item ON versions(tenant_id, item_id, env, version);

CREATE TABLE IF NOT EXISTS releases (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    namespace_id TEXT NOT NULL,
    group_id     TEXT NOT NULL,
    item_id      TEXT NOT NULL,
    env          TEXT NOT NULL,
    version      BIGINT NOT NULL,
    prev_version BIGINT NOT NULL,
    strategy     TEXT NOT NULL CHECK (strategy IN ('ip', 'percent')),
    ips          JSONB NOT NULL DEFAULT '[]',
    percent      INTEGER NOT NULL DEFAULT 0,
    status       TEXT NOT NULL CHECK (status IN ('gray', 'promoted', 'rolled_back')),
    operator     TEXT NOT NULL DEFAULT '',
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    promoted_at  TIMESTAMPTZ,
    ended_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_releases_ns ON releases(tenant_id, namespace_id, started_at DESC);

-- Raw reference edges: one row per placeholder declared by one item+env.
-- Replaced wholesale on every commit, so the graph always matches the latest
-- value. The concrete target is projected at read time against the current
-- namespace/group/key locations.
CREATE TABLE IF NOT EXISTS item_refs (
    id          BIGINT PRIMARY KEY DEFAULT nextval('versions_id_seq'),
    tenant_id   TEXT NOT NULL,
    item_id     TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    env         TEXT NOT NULL,
    target_key  TEXT NOT NULL,
    target_bare BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_item_refs_src ON item_refs(tenant_id, item_id, env);
CREATE INDEX IF NOT EXISTS idx_item_refs_tenant ON item_refs(tenant_id);
