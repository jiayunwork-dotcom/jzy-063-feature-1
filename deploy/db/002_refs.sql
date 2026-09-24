-- Reference dependency graph edges (added with the reference layer).
-- Safe to apply on databases created before the feature existed.

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
