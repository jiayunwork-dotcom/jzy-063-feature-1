package tests

import (
	"context"
	"testing"

	"configcenter/internal/domain"
	"configcenter/internal/push"
	"configcenter/internal/service"
	"configcenter/internal/store"
)

type fixture struct {
	ctx context.Context
	st  store.Store
	bus *push.MemoryBus
	hub *push.Hub
	svc *service.Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st := store.NewMemory()
	bus := push.NewMemoryBus()
	hub := push.NewHub(push.NewMemTracker())
	svc := service.New(st, bus, hub)
	return &fixture{ctx: context.Background(), st: st, bus: bus, hub: hub, svc: svc}
}

func (f *fixture) tenant(t *testing.T, id string, q *service.Quota) {
	t.Helper()
	if _, err := f.svc.CreateTenant(f.ctx, id, id, q); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
}

func (f *fixture) ns(t *testing.T, tenant, id string) {
	t.Helper()
	if _, err := f.svc.CreateNamespace(f.ctx, tenant, id, id); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
}

func (f *fixture) group(t *testing.T, tenant, ns, id string) {
	t.Helper()
	if _, err := f.svc.CreateGroup(f.ctx, tenant, ns, id, id); err != nil {
		t.Fatalf("create group: %v", err)
	}
}

func (f *fixture) item(t *testing.T, tenant, ns, grp, key string, fmtv domain.Format) string {
	t.Helper()
	it, err := f.svc.CreateItem(f.ctx, service.CreateItemParams{
		TenantID: tenant, NamespaceID: ns, GroupID: grp, Key: key, Format: fmtv,
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	return it.ID
}

func (f *fixture) commit(t *testing.T, tenant, itemID, env, val string, expected int64) *service.CommitResult {
	t.Helper()
	res, err := f.svc.Commit(f.ctx, service.CommitParams{
		TenantID: tenant, ItemID: itemID, Env: env, Value: val,
		Operator: "tester", ExpectedVersion: expected,
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return res
}
