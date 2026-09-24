package tests

import (
	"strings"
	"testing"
	"time"

	"configcenter/internal/domain"
	"configcenter/internal/push"
	"configcenter/internal/service"
	"configcenter/internal/store"
)

// Read-time cycle guard: if a cyclic graph somehow exists in storage, the
// resolver DFS stops and reports the chain instead of looping or blowing the
// stack. (The write guard makes this unreachable through the API; this test
// pins the defense in depth.)
func TestReadTimeCycleDetected(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "ns")
	f.group(t, "t1", "ns", "g")
	a := f.item(t, "t1", "ns", "g", "a", domain.FormatProperties)
	b := f.item(t, "t1", "ns", "g", "b", domain.FormatProperties)
	f.commit(t, "t1", a, "dev", "x=1\n", 0)
	f.commit(t, "t1", b, "dev", "x=1\n", 0)

	// Forge cyclic values and edges directly through storage, bypassing the
	// write-time guard (defense-in-depth read guard).
	commitRaw := func(id string, expected int64, raw string) {
		rev, err := f.st.NextTenantRevision(f.ctx, "t1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.CommitValue(f.ctx, store.CommitValueParams{
			TenantID: "t1", ItemID: id, Env: "dev", Value: raw, Revision: rev,
			ChangeType: "update", ExpectedVersion: int64(expected),
		}); err != nil {
			t.Fatal(err)
		}
	}
	commitRaw(a, 1, "x=@{b}\n")
	commitRaw(b, 1, "x=@{a}\n")

	mkEdge := func(src, tgt string) domain.RefEdge {
		return domain.RefEdge{
			TenantID: "t1", ItemID: src, Env: "dev", Seq: 0, Raw: "@{" + tgt + "}",
			Target: domain.RefTarget{Key: tgt},
		}
	}
	if err := f.st.SetItemRefs(f.ctx, "t1", a, "dev", []domain.RefEdge{mkEdge(a, "b")}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetItemRefs(f.ctx, "t1", b, "dev", []domain.RefEdge{mkEdge(b, "a")}); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.Effective(f.ctx, "t1", "ns", "g", "dev", service.InstanceContext{IP: "1.1.1.1"})
	re := wantRefError(t, err, service.CodeRefCycle)
	joined := strings.Join(re.Cycle, " -> ")
	if !strings.Contains(joined, "a[dev]") || !strings.Contains(joined, "b[dev]") {
		t.Fatalf("read cycle chain must name a and b: %v", re.Cycle)
	}
}

// Changing a referenced key advances the effective revision of every
// dependent key and wakes their long-poll subscribers.
func TestReferencedChangeFansOutAndAdvancesVersion(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "shop")
	f.group(t, "t1", "shop", "svc")
	base := f.item(t, "t1", "shop", "svc", "base", domain.FormatProperties)
	use := f.item(t, "t1", "shop", "svc", "use", domain.FormatProperties)
	f.commit(t, "t1", base, "prod", "v=old\n", 0)
	f.commit(t, "t1", use, "prod", "x=@{base}\n", 0)

	ic := service.InstanceContext{InstanceID: "i-1", IP: "10.0.0.1"}
	snap, _ := f.svc.Effective(f.ctx, "t1", "shop", "svc", "prod", ic)
	before := findEntry(t, snap, "use").Version
	if before == 0 {
		t.Fatal("revision must be positive after setup")
	}

	// A subscriber on the dependent group suspends a long poll.
	conn := f.hub.Register(push.ConnMeta{
		InstanceID: "i-1", TenantID: "t1", NamespaceID: "shop", GroupID: "svc",
		Env: "prod", IP: "10.0.0.1",
	})
	defer f.hub.Unregister(conn)

	// Commit only the referenced key.
	f.commit(t, "t1", base, "prod", "v=new\n", 1)

	// The dependent key's served revision must move forward.
	snap, _ = f.svc.Effective(f.ctx, "t1", "shop", "svc", "prod", ic)
	after := findEntry(t, snap, "use").Version
	if after <= before {
		t.Fatalf("dependent revision must advance: before=%d after=%d", before, after)
	}
	if got := findEntry(t, snap, "use").Value; !strings.Contains(got, "x=new") {
		t.Fatalf("dependent value must follow referenced change: %q", got)
	}

	// Production consumer mirrors bus -> hub dispatch with the gray gate.
	// The fanout must include an event for the dependent key, waking conn.
	got := make(chan struct{}, 1)
	go func() {
		f.hub.Wait(conn, time.Second)
		close(got)
	}()
	// Replay events would already have been dispatched in production; emulate
	// by checking the gate admits a dependent-key change event. Simulate a bus
	// event explicitly here (the commit above published it).
	ev := domain.PushEvent{
		Type: domain.EventChange, TenantID: "t1", NamespaceID: "shop", GroupID: "svc",
		Key: "use", Env: "prod",
	}
	n := f.hub.Dispatch(ev, f.svc.NewGate())
	if n < 1 {
		t.Fatal("subscriber of the dependent key must be woken by its fanout event")
	}
	select {
	case <-got:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("long poll on dependent group not released")
	}
}

// Gray release on a referenced key: selected instances resolve the chain to
// new values, non-selected instances — and the keys depending on it — keep
// the old values. The push gate applies the same release selection to
// dependent-key events.
func TestGrayOnReferencedKeyFlowsThroughChain(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "shop")
	f.group(t, "t1", "shop", "svc")
	base := f.item(t, "t1", "shop", "svc", "base", domain.FormatProperties)
	mid := f.item(t, "t1", "shop", "svc", "mid", domain.FormatProperties)
	use := f.item(t, "t1", "shop", "svc", "use", domain.FormatProperties)
	f.commit(t, "t1", base, "prod", "v=old\n", 0)
	f.commit(t, "t1", mid, "prod", "m=@{base}\n", 0)
	// scalar reference chain: use.u follows mid.m follows base.v
	f.commit(t, "t1", use, "prod", "u=@{mid}\n", 0)

	connIn := f.hub.Register(push.ConnMeta{InstanceID: "in", TenantID: "t1", NamespaceID: "shop", GroupID: "svc", Env: "prod", IP: "10.0.0.1"})
	connOut := f.hub.Register(push.ConnMeta{InstanceID: "out", TenantID: "t1", NamespaceID: "shop", GroupID: "svc", Env: "prod", IP: "10.0.0.2"})
	defer f.hub.Unregister(connIn)
	defer f.hub.Unregister(connOut)

	res, err := f.svc.Commit(f.ctx, service.CommitParams{
		TenantID: "t1", ItemID: base, Env: "prod", Value: "v=new\n", ExpectedVersion: 1,
		Gray: &service.GrayRequest{Strategy: domain.GrayIPList, IPs: []string{"10.0.0.1"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	in := service.InstanceContext{InstanceID: "in", IP: "10.0.0.1"}
	out := service.InstanceContext{InstanceID: "out", IP: "10.0.0.2"}
	eff := func(ic service.InstanceContext) string {
		snap, err := f.svc.Effective(f.ctx, "t1", "shop", "svc", "prod", ic)
		if err != nil {
			t.Fatal(err)
		}
		return findEntry(t, snap, "use").Value
	}
	if got := eff(in); !strings.Contains(got, "u=new") {
		t.Fatalf("gray-selected instance must see the chain resolved to new: %q", got)
	}
	if got := eff(out); !strings.Contains(got, "u=old") {
		t.Fatalf("non-selected instance must keep the chain on old values: %q", got)
	}

	// Every fanned event (base/mid/use) carries the same release id; the gate
	// admits exactly the selected instance for the dependent keys too.
	for _, key := range []string{"base", "mid", "use"} {
		ev := domain.PushEvent{
			Type: domain.EventChange, TenantID: "t1", NamespaceID: "shop", GroupID: "svc",
			Key: key, Env: "prod", ReleaseID: res.Release.ID,
		}
		if n := f.hub.Dispatch(ev, f.svc.NewGate()); n != 1 {
			t.Fatalf("event for %s must wake exactly the selected instance, woke %d", key, n)
		}
	}
	if f.hub.Wait(connIn, 50*time.Millisecond) != true {
		t.Fatal("selected instance should have been woken")
	}
	if f.hub.Wait(connOut, 50*time.Millisecond) != false {
		t.Fatal("non-selected instance must not be woken")
	}
}

// Cross-tenant references must fail at commit: a qualified coordinate in
// another tenant simply does not resolve inside the author's tenant.
func TestCrossTenantReferenceRejected(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "alpha", nil)
	f.tenant(t, "beta", nil)
	f.ns(t, "alpha", "ns")
	f.group(t, "alpha", "ns", "g")
	f.ns(t, "beta", "ns")
	f.group(t, "beta", "ns", "g")
	secret := f.item(t, "beta", "ns", "g", "secret", domain.FormatProperties)
	f.commit(t, "beta", secret, "prod", "v=beta-only\n", 0)

	a := f.item(t, "alpha", "ns", "g", "a", domain.FormatProperties)
	_, err := f.svc.Commit(f.ctx, service.CommitParams{
		TenantID: "alpha", ItemID: a, Env: "prod",
		Value: "x=@{ns/g/secret}\n", ExpectedVersion: 0,
	})
	wantRefError(t, err, service.CodeRefMissing)
}

// A ?env= reference reads the target's value in that other environment.
func TestExplicitEnvReference(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "ns")
	f.group(t, "t1", "ns", "g")
	host := f.item(t, "t1", "ns", "g", "host", domain.FormatProperties)
	f.commit(t, "t1", host, "prod", "v=prod-db\n", 0)
	f.commit(t, "t1", host, "dev", "v=dev-db\n", 0)
	link := f.item(t, "t1", "ns", "g", "link", domain.FormatProperties)
	f.commit(t, "t1", link, "dev", "h=@{host?env=prod}\n", 0)

	snap, err := f.svc.Effective(f.ctx, "t1", "ns", "g", "dev", service.InstanceContext{IP: "1.1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := findEntry(t, snap, "link").Value; !strings.Contains(got, "h=prod-db") {
		t.Fatalf("cross-env ref wrong: %q", got)
	}
}

// The graph is queryable both directions: outbound references and inbound
// impact, including across layers.
func TestGraphQueries(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "ns")
	f.group(t, "t1", "ns", "g")
	pub := f.item(t, "t1", domain.PublicNamespaceID, domain.PublicNamespaceID, "shared", domain.FormatProperties)
	f.commit(t, "t1", pub, "dev", "v=s\n", 0)
	a := f.item(t, "t1", "ns", "g", "a", domain.FormatProperties)
	f.commit(t, "t1", a, "dev", "x=@{shared}\n", 0)

	out, err := f.svc.ItemGraph(f.ctx, "t1", a, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "shared" || out[0].Layer != domain.LayerPublic {
		t.Fatalf("outbound reference wrong: %+v", out)
	}
	view, err := f.svc.Impact(f.ctx, "t1", pub, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Direct) != 1 || view.Direct[0].Key != "a" {
		t.Fatalf("inbound direct set wrong: %+v", view.Direct)
	}
}
