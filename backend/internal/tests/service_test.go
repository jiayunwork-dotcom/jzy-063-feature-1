package tests

import (
	"errors"
	"testing"

	"configcenter/internal/domain"
	"configcenter/internal/service"
	"configcenter/internal/store"
)

// Version numbers are strictly monotonic per item+env.
func TestVersionNumbersMonotonic(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "ns")
	f.group(t, "t1", "ns", "g")
	id := f.item(t, "t1", "ns", "g", "k", domain.FormatJSON)

	prev := int64(0)
	for i := 0; i < 5; i++ {
		res := f.commit(t, "t1", id, "dev",
			`{"v":`+itoa(int64(i))+`}`, prev)
		if res.Version.Version != prev+1 {
			t.Fatalf("version jumped to %d, want %d", res.Version.Version, prev+1)
		}
		prev = res.Version.Version
	}
	// A stale expected_version must be rejected (optimistic concurrency).
	_, err := f.svc.Commit(f.ctx, service.CommitParams{
		TenantID: "t1", ItemID: id, Env: "dev", Value: `{"v":99}`, ExpectedVersion: 1,
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("expected conflict on stale version, got %v", err)
	}
}

// Rollback creates a new version carrying an old value and never removes the
// intermediate history.
func TestRollbackCreatesNewVersion(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "ns")
	f.group(t, "t1", "ns", "g")
	id := f.item(t, "t1", "ns", "g", "k", domain.FormatProperties)

	f.commit(t, "t1", id, "prod", "k=v1\n", 0) // v1
	f.commit(t, "t1", id, "prod", "k=v2\n", 1) // v2
	f.commit(t, "t1", id, "prod", "k=v3\n", 2) // v3

	res, err := f.svc.Rollback(f.ctx, "t1", id, "prod", 1, "tester")
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if res.Version.Version != 4 {
		t.Fatalf("rollback must append version 4, got %d", res.Version.Version)
	}
	if res.Version.Value != "k=v1\n" {
		t.Fatalf("rollback value wrong: %q", res.Version.Value)
	}
	if res.Version.ChangeType != "rollback" {
		t.Fatalf("rollback must be marked, got %q", res.Version.ChangeType)
	}

	hist, err := f.svc.ListVersions(f.ctx, "t1", id, "prod", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 4 {
		t.Fatalf("intermediate history must survive, got %d versions", len(hist))
	}
	// v2 remains readable.
	v2, err := f.st.GetVersion(f.ctx, "t1", id, "prod", 2)
	if err != nil || v2.Value != "k=v2\n" {
		t.Fatal("version 2 should still exist after rollback")
	}
	// Diff v2 -> v4 shows the rollback delta.
	rows, _, _, err := f.svc.Diff(f.ctx, "t1", id, "prod", 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	for _, r := range rows {
		if r.Op == "change" && r.Left == "k=v2" && r.Right == "k=v1" {
			changed = true
		}
	}
	if !changed {
		t.Fatalf("diff v2..v4 must show the rolled back line, got %+v", rows)
	}
}

// Invalid content is refused before persistence and names the problem.
func TestCommitRejectsInvalidContent(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "ns")
	f.group(t, "t1", "ns", "g")
	id := f.item(t, "t1", "ns", "g", "k", domain.FormatJSON)

	_, err := f.svc.Commit(f.ctx, service.CommitParams{
		TenantID: "t1", ItemID: id, Env: "dev", Value: `{not json`, ExpectedVersion: 0,
	})
	var vf *service.ValidationFailure
	if !errors.As(err, &vf) {
		t.Fatalf("expected ValidationFailure, got %v", err)
	}
	if len(vf.Errors) == 0 {
		t.Fatal("validation failure must explain the error")
	}
	// nothing was written
	if hist, _ := f.svc.ListVersions(f.ctx, "t1", id, "dev", 0); len(hist) != 0 {
		t.Fatal("invalid content must not be persisted")
	}
}

// Namespace quota is enforced and clearly rejected.
func TestNamespaceQuotaEnforced(t *testing.T) {
	f := newFixture(t)
	q := service.Quota{MaxNamespaces: 2, MaxItemsPerGroup: 10, VersionRetention: 5}
	f.tenant(t, "qt", &q)
	f.ns(t, "qt", "a")
	f.ns(t, "qt", "b")
	_, err := f.svc.CreateNamespace(f.ctx, "qt", "c", "c")
	if !errors.Is(err, store.ErrQuota) {
		t.Fatalf("expected quota error on third namespace, got %v", err)
	}
}

// Item quota per group is enforced.
func TestItemQuotaEnforced(t *testing.T) {
	f := newFixture(t)
	q := service.Quota{MaxNamespaces: 5, MaxItemsPerGroup: 2, VersionRetention: 5}
	f.tenant(t, "qt", &q)
	f.ns(t, "qt", "ns")
	f.group(t, "qt", "ns", "g")
	f.item(t, "qt", "ns", "g", "k1", domain.FormatProperties)
	f.item(t, "qt", "ns", "g", "k2", domain.FormatProperties)
	_, err := f.svc.CreateItem(f.ctx, service.CreateItemParams{
		TenantID: "qt", NamespaceID: "ns", GroupID: "g", Key: "k3", Format: domain.FormatProperties,
	})
	if !errors.Is(err, store.ErrQuota) {
		t.Fatalf("expected item quota error, got %v", err)
	}
}

// Retention caps how many versions are kept.
func TestVersionRetention(t *testing.T) {
	f := newFixture(t)
	q := service.Quota{MaxNamespaces: 5, MaxItemsPerGroup: 10, VersionRetention: 3}
	f.tenant(t, "rt", &q)
	f.ns(t, "rt", "ns")
	f.group(t, "rt", "ns", "g")
	id := f.item(t, "rt", "ns", "g", "k", domain.FormatProperties)
	for i, v := range []string{"a=1\n", "a=2\n", "a=3\n", "a=4\n", "a=5\n"} {
		f.commit(t, "rt", id, "dev", v, int64(i))
	}
	hist, _ := f.svc.ListVersions(f.ctx, "rt", id, "dev", 0)
	if len(hist) != 3 {
		t.Fatalf("retention=3 should keep 3 versions, got %d", len(hist))
	}
	if hist[len(hist)-1].Version != 3 {
		t.Fatalf("oldest surviving version should be 3, got %d", hist[len(hist)-1].Version)
	}
}

// Tenants cannot see each other's data.
func TestTenantIsolation(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "alpha", nil)
	f.tenant(t, "beta", nil)
	f.ns(t, "alpha", "ns")
	f.group(t, "alpha", "ns", "g")
	id := f.item(t, "alpha", "ns", "g", "secret", domain.FormatProperties)
	f.commit(t, "alpha", id, "dev", "secret=alpha-value\n", 0)

	// beta must not read alpha's item directly.
	if _, err := f.svc.GetItem(f.ctx, "beta", id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("beta could read alpha item: %v", err)
	}
	// Same namespace id in beta is a different, empty namespace.
	if items, err := f.svc.ListItems(f.ctx, "beta", "ns", "g"); err != nil || len(items) != 0 {
		// group doesn't exist for beta: treat not-found and empty equivalently
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	// beta cannot read alpha's version history.
	hist, _ := f.svc.ListVersions(f.ctx, "beta", id, "dev", 0)
	if len(hist) != 0 {
		t.Fatal("beta could read alpha version history")
	}
	// beta's effective snapshot never contains alpha's key.
	f.ns(t, "beta", "ns")
	f.group(t, "beta", "ns", "g")
	snap, err := f.svc.Effective(f.ctx, "beta", "ns", "g", "dev", service.InstanceContext{IP: "1.1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range snap.Entries {
		if e.Key == "secret" {
			t.Fatal("alpha secret leaked into beta effective config")
		}
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [24]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
