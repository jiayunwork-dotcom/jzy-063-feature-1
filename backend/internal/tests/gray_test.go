package tests

import (
	"fmt"
	"testing"
	"time"

	"configcenter/internal/domain"
	"configcenter/internal/push"
	"configcenter/internal/service"
)

// Wait briefly for a long-poll connection to be woken. Because Dispatch closes
// the signal channel before this runs, an already-woken conn returns at once.
func expectWoken(t *testing.T, hub *push.Hub, c *push.Conn, want bool) {
	t.Helper()
	got := hub.Wait(c, 100*time.Millisecond)
	if got != want {
		t.Fatalf("instance %s woken=%v, want %v", c.InstanceID, got, want)
	}
}

// Gray by IP list: only listed instances receive the new version, both in the
// computed effective config and in the push dispatch.
func TestGrayIPListHitsOnlySelected(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "shop")
	f.group(t, "t1", "shop", "svc")
	id := f.item(t, "t1", "shop", "svc", "feature", domain.FormatProperties)
	f.commit(t, "t1", id, "prod", "flag=off\n", 0) // v1

	// three live instances
	c1 := f.hub.Register(push.ConnMeta{InstanceID: "i1", TenantID: "t1", NamespaceID: "shop", GroupID: "svc", Env: "prod", IP: "10.0.0.1"})
	c2 := f.hub.Register(push.ConnMeta{InstanceID: "i2", TenantID: "t1", NamespaceID: "shop", GroupID: "svc", Env: "prod", IP: "10.0.0.2"})
	c3 := f.hub.Register(push.ConnMeta{InstanceID: "i3", TenantID: "t1", NamespaceID: "shop", GroupID: "svc", Env: "prod", IP: "10.0.0.3"})
	defer f.hub.Unregister(c3)

	// commit v2 as a gray release for 10.0.0.1 only
	res, err := f.svc.Commit(f.ctx, service.CommitParams{
		TenantID: "t1", ItemID: id, Env: "prod", Value: "flag=on\n",
		Operator: "tester", ExpectedVersion: 1,
		Gray: &service.GrayRequest{Strategy: domain.GrayIPList, IPs: []string{"10.0.0.1"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	ev := domain.PushEvent{
		Type: domain.EventChange, TenantID: "t1", NamespaceID: "shop",
		GroupID: "svc", ItemID: id, Key: "feature", Env: "prod",
		Version: 2, ReleaseID: res.Release.ID,
	}
	n := f.hub.Dispatch(ev, f.svc.NewGate())
	if n != 1 {
		t.Fatalf("gray dispatch must wake exactly 1 instance, woke %d", n)
	}
	expectWoken(t, f.hub, c1, true)
	expectWoken(t, f.hub, c2, false)
	expectWoken(t, f.hub, c3, false)

	// effective config reflects the same gate at pull time.
	eff := func(ip string) string {
		snap, err := f.svc.Effective(f.ctx, "t1", "shop", "svc", "prod",
			service.InstanceContext{IP: ip})
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.Entries) != 1 {
			t.Fatalf("expected one entry for %s", ip)
		}
		return snap.Entries[0].Value
	}
	if got := eff("10.0.0.1"); got != "flag=on\n" {
		t.Fatalf("gray instance should get new value, got %q", got)
	}
	if got := eff("10.0.0.2"); got != "flag=off\n" {
		t.Fatalf("non-gray instance must keep old value, got %q", got)
	}
	if got := eff("10.0.0.3"); got != "flag=off\n" {
		t.Fatalf("non-gray instance must keep old value, got %q", got)
	}

	// gray panel counters: 1 delivered out of 3 total.
	view, err := f.svc.GetRelease(f.ctx, "t1", res.Release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.DeliveredCount != 1 || view.TotalInstances != 3 {
		t.Fatalf("release counters = %d/%d, want 1/3", view.DeliveredCount, view.TotalInstances)
	}
}

// After promotion every instance gets the new version.
func TestPromoteDeliversToAll(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "shop")
	f.group(t, "t1", "shop", "svc")
	id := f.item(t, "t1", "shop", "svc", "feature", domain.FormatProperties)
	f.commit(t, "t1", id, "prod", "flag=off\n", 0)

	res, err := f.svc.Commit(f.ctx, service.CommitParams{
		TenantID: "t1", ItemID: id, Env: "prod", Value: "flag=on\n",
		ExpectedVersion: 1,
		Gray:            &service.GrayRequest{Strategy: domain.GrayIPList, IPs: []string{"10.0.0.1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Promote(f.ctx, "t1", res.Release.ID, "tester"); err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.9"} {
		snap, err := f.svc.Effective(f.ctx, "t1", "shop", "svc", "prod",
			service.InstanceContext{IP: ip})
		if err != nil {
			t.Fatal(err)
		}
		if snap.Entries[0].Value != "flag=on\n" {
			t.Fatalf("after promote %s should get new value, got %q", ip, snap.Entries[0].Value)
		}
	}
}

// Gray rollback restores the previous value as a new version and only wakes
// instances that had actually received the gray value.
func TestGrayRollbackOnlyRecallsDelivered(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "shop")
	f.group(t, "t1", "shop", "svc")
	id := f.item(t, "t1", "shop", "svc", "feature", domain.FormatProperties)
	f.commit(t, "t1", id, "prod", "flag=off\n", 0) // v1

	c1 := f.hub.Register(push.ConnMeta{InstanceID: "i1", TenantID: "t1", NamespaceID: "shop", GroupID: "svc", Env: "prod", IP: "10.0.0.1"})
	c2 := f.hub.Register(push.ConnMeta{InstanceID: "i2", TenantID: "t1", NamespaceID: "shop", GroupID: "svc", Env: "prod", IP: "10.0.0.2"})

	res, err := f.svc.Commit(f.ctx, service.CommitParams{
		TenantID: "t1", ItemID: id, Env: "prod", Value: "flag=on\n",
		ExpectedVersion: 1,
		Gray:            &service.GrayRequest{Strategy: domain.GrayIPList, IPs: []string{"10.0.0.1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ev := domain.PushEvent{
		Type: domain.EventChange, TenantID: "t1", NamespaceID: "shop",
		GroupID: "svc", ItemID: id, Env: "prod",
		Version: 2, ReleaseID: res.Release.ID,
	}
	f.hub.Dispatch(ev, f.svc.NewGate()) // only c1 marked delivered

	_, v, err := f.svc.GrayRollback(f.ctx, "t1", res.Release.ID, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if v.Version != 3 || v.Value != "flag=off\n" {
		t.Fatalf("rollback should re-commit old value as v3, got v%d %q", v.Version, v.Value)
	}
	if v.ChangeType != "gray_rollback" {
		t.Fatalf("change type = %q", v.ChangeType)
	}
	rbEvent := domain.PushEvent{
		Type: domain.EventRollback, TenantID: "t1", NamespaceID: "shop",
		GroupID: "svc", ItemID: id, Env: "prod",
		Version: 3, ReleaseID: res.Release.ID,
	}
	n := f.hub.Dispatch(rbEvent, f.svc.NewGate())
	if n != 1 {
		t.Fatalf("rollback push must recall only the delivered instance, woke %d", n)
	}
	expectWoken(t, f.hub, c1, true)
	expectWoken(t, f.hub, c2, false)

	// everyone computes the safe old value afterwards.
	for _, ip := range []string{"10.0.0.1", "10.0.0.2"} {
		snap, err := f.svc.Effective(f.ctx, "t1", "shop", "svc", "prod",
			service.InstanceContext{IP: ip})
		if err != nil {
			t.Fatal(err)
		}
		if snap.Entries[0].Value != "flag=off\n" {
			t.Fatalf("after gray rollback %s should see old value, got %q", ip, snap.Entries[0].Value)
		}
	}
}

// Percentage gray: the fraction of instances actually served the new value
// must sit in the expected band and the rest must keep the old one.
func TestGrayPercentRatioAndIsolation(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "shop")
	f.group(t, "t1", "shop", "svc")
	id := f.item(t, "t1", "shop", "svc", "feature", domain.FormatProperties)
	f.commit(t, "t1", id, "prod", "flag=off\n", 0)

	const pool = 1000
	_, err := f.svc.Commit(f.ctx, service.CommitParams{
		TenantID: "t1", ItemID: id, Env: "prod", Value: "flag=on\n",
		ExpectedVersion: 1,
		Gray:            &service.GrayRequest{Strategy: domain.GrayPercent, Percent: 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	newCount := 0
	for i := 0; i < pool; i++ {
		instID := fmt.Sprintf("node-%05d", i)
		snap, err := f.svc.Effective(f.ctx, "t1", "shop", "svc", "prod",
			service.InstanceContext{InstanceID: instID, IP: "10.2.0." + itoa(int64(i%250))})
		if err != nil {
			t.Fatal(err)
		}
		if snap.Entries[0].Value == "flag=on\n" {
			newCount++
		} else if snap.Entries[0].Value != "flag=off\n" {
			t.Fatalf("unexpected value %q", snap.Entries[0].Value)
		}
	}
	ratio := float64(newCount) / float64(pool)
	if ratio < 0.26 || ratio > 0.34 {
		t.Fatalf("30%% gray served %.1f%% of instances, want ~30%%", ratio*100)
	}

	// stability: asking twice gives the same answer for every instance.
	for i := 0; i < 50; i++ {
		instID := fmt.Sprintf("stable-%d", i)
		a, _ := f.svc.Effective(f.ctx, "t1", "shop", "svc", "prod",
			service.InstanceContext{InstanceID: instID, IP: "1.1.1.1"})
		b, _ := f.svc.Effective(f.ctx, "t1", "shop", "svc", "prod",
			service.InstanceContext{InstanceID: instID, IP: "1.1.1.1"})
		if a.Entries[0].Value != b.Entries[0].Value {
			t.Fatalf("gray membership unstable for %s", instID)
		}
	}
}
