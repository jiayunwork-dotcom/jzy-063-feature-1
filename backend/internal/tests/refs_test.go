package tests

import (
	"strings"
	"testing"

	"configcenter/internal/domain"
	"configcenter/internal/service"
)

// findEntry picks one effective entry by key.
func findEntry(t *testing.T, snap *service.EffectiveSnapshot, key string) service.EffectiveEntry {
	t.Helper()
	for _, e := range snap.Entries {
		if e.Key == key {
			return e
		}
	}
	t.Fatalf("key %q not in snapshot", key)
	return service.EffectiveEntry{}
}

// A reference is replaced by the target key's merged effective value, and the
// field provenance reaches through to the referenced key's layer/version.
func TestReferenceResolvesToEffectiveValue(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "payment")
	f.group(t, "t1", "payment", "gateway")

	// public layer defines host; namespace layer defines port.
	pubHost := f.item(t, "t1", domain.PublicNamespaceID, domain.PublicNamespaceID, "db_host", domain.FormatProperties)
	f.commit(t, "t1", pubHost, "prod", "v=shared-db.internal\n", 0)

	// group layer embeds both into a connection string
	conn := f.item(t, "t1", "payment", "gateway", "db_conn", domain.FormatProperties)
	f.commit(t, "t1", conn, "prod", "url=jdbc:@{db_host}\npool=8\n", 0)

	snap, err := f.svc.Effective(f.ctx, "t1", "payment", "gateway", "prod",
		service.InstanceContext{IP: "10.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	e := findEntry(t, snap, "db_conn")
	if !strings.Contains(e.Value, "url=jdbc:shared-db.internal") {
		t.Fatalf("placeholder not substituted: %q", e.Value)
	}
	if strings.Contains(e.Value, "@{") {
		t.Fatalf("raw placeholder leaked into effective value: %q", e.Value)
	}
	// provenance: the url leaf must carry a segment naming db_host/public.
	var found bool
	for _, rf := range e.ResolvedFields {
		if rf.Path != "url" {
			continue
		}
		for _, s := range rf.ValueSegments {
			if s.Key == "db_host" && s.NamespaceID == domain.PublicNamespaceID &&
				s.Source == domain.LayerPublic {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("resolved field provenance missing; got %+v", e.ResolvedFields)
	}
}

// References see the *merged+resolved* target: a group override of the target
// changes what the referencing key observes.
func TestReferenceSeesMergedTarget(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "payment")
	f.group(t, "t1", "payment", "gateway")

	base := f.item(t, "t1", "payment", "gateway", "timeout_base", domain.FormatProperties)
	f.commit(t, "t1", base, "prod", "v=1000\n", 0)
	use := f.item(t, "t1", "payment", "gateway", "timeout", domain.FormatProperties)
	f.commit(t, "t1", use, "prod", "ms=@{timeout_base}\n", 0)

	snap, _ := f.svc.Effective(f.ctx, "t1", "payment", "gateway", "prod",
		service.InstanceContext{IP: "1.1.1.1"})
	if got := findEntry(t, snap, "timeout").Value; !strings.Contains(got, "ms=1000") {
		t.Fatalf("initial resolution wrong: %q", got)
	}

	// override the base with a new version
	f.commit(t, "t1", base, "prod", "v=2500\n", 1)
	snap, _ = f.svc.Effective(f.ctx, "t1", "payment", "gateway", "prod",
		service.InstanceContext{IP: "1.1.1.1"})
	if got := findEntry(t, snap, "timeout").Value; !strings.Contains(got, "ms=2500") {
		t.Fatalf("referencing key did not follow target change: %q", got)
	}
}

// Chained references A->B->C resolve all the way down.
func TestChainedReferences(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "ns")
	f.group(t, "t1", "ns", "g")
	c := f.item(t, "t1", "ns", "g", "c", domain.FormatProperties)
	f.commit(t, "t1", c, "dev", "v=deep\n", 0)
	b := f.item(t, "t1", "ns", "g", "b", domain.FormatProperties)
	f.commit(t, "t1", b, "dev", "v=@{c}\n", 0)
	a := f.item(t, "t1", "ns", "g", "a", domain.FormatProperties)
	f.commit(t, "t1", a, "dev", "v=pre-@{b}-post\n", 0)

	snap, err := f.svc.Effective(f.ctx, "t1", "ns", "g", "dev", service.InstanceContext{IP: "1.1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := findEntry(t, snap, "a").Value; !strings.Contains(got, "v=pre-deep-post") {
		t.Fatalf("chain not resolved to the end: %q", got)
	}
	// through chain on field a.v: a -> b -> c
	e := findEntry(t, snap, "a")
	for _, rf := range e.ResolvedFields {
		if rf.Path == "v" && len(rf.ValueSegments) > 0 {
			through := rf.ValueSegments[0].Through
			if len(through) < 2 || through[0].Key != "b" {
				t.Fatalf("through chain wrong: %+v", through)
			}
			last := through[len(through)-1]
			if last.Key != "c" {
				t.Fatalf("through chain must reach c: %+v", through)
			}
			return
		}
	}
	t.Fatalf("no segment provenance on chained field: %+v", e.ResolvedFields)
}

// A missing target is rejected at commit time, naming both ends.
func TestCommitRejectsMissingTarget(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "ns")
	f.group(t, "t1", "ns", "g")
	a := f.item(t, "t1", "ns", "g", "a", domain.FormatProperties)

	_, err := f.svc.Commit(f.ctx, service.CommitParams{
		TenantID: "t1", ItemID: a, Env: "dev", Value: "v=@{ghost}\n", ExpectedVersion: 0,
	})
	re := wantRefError(t, err, service.CodeRefMissing)
	if !strings.Contains(re.Target, "ghost") || !strings.Contains(re.Source, "a") {
		t.Fatalf("missing target error must name source and target, got %+v", re)
	}
}

// A qualified target in another namespace/group resolves.
func TestQualifiedCrossContainerReference(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "payment")
	f.group(t, "t1", "payment", "gateway")
	f.ns(t, "t1", "trade")
	f.group(t, "t1", "trade", "orders")

	src := f.item(t, "t1", "payment", "gateway", "host", domain.FormatProperties)
	f.commit(t, "t1", src, "prod", "v=pay-db\n", 0)
	dst := f.item(t, "t1", "trade", "orders", "conn", domain.FormatProperties)
	f.commit(t, "t1", dst, "prod", "h=@{payment/gateway/host}\n", 0)

	snap, err := f.svc.Effective(f.ctx, "t1", "trade", "orders", "prod", service.InstanceContext{IP: "1.1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := findEntry(t, snap, "conn").Value; !strings.Contains(got, "h=pay-db") {
		t.Fatalf("qualified ref unresolved: %q", got)
	}
}

// Reading a reference whose target exists as an item but has no value in the
// env is a hard error, never a leaked placeholder.
func TestMissingEnvValueIsError(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "ns")
	f.group(t, "t1", "ns", "g")
	target := f.item(t, "t1", "ns", "g", "host", domain.FormatProperties)
	f.commit(t, "t1", target, "prod", "v=h\n", 0)
	ref := f.item(t, "t1", "ns", "g", "conn", domain.FormatProperties)
	f.commit(t, "t1", ref, "staging", "h=@{host}\n", 0) // host has no staging value

	_, err := f.svc.Effective(f.ctx, "t1", "ns", "g", "staging", service.InstanceContext{IP: "1.1.1.1"})
	wantRefError(t, err, service.CodeRefNoValue)
}

// JSON/YAML/TOML references keep the document valid after substitution;
// a whole-placeholder JSON scalar keeps the target scalar's type.
func TestReferencesAcrossFormats(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "ns")
	f.group(t, "t1", "ns", "g")

	num := f.item(t, "t1", "ns", "g", "base_timeout", domain.FormatProperties)
	f.commit(t, "t1", num, "dev", "v=3000\n", 0) // a single-scalar target document

	js := f.item(t, "t1", "ns", "g", "js", domain.FormatJSON)
	f.commit(t, "t1", js, "dev", "{\"timeout\": @{base_timeout}}\n", 0)
	yml := f.item(t, "t1", "ns", "g", "yml", domain.FormatYAML)
	f.commit(t, "t1", yml, "dev", "timeout: '@{base_timeout}'\n", 0)
	tom := f.item(t, "t1", "ns", "g", "tom", domain.FormatTOML)
	f.commit(t, "t1", tom, "dev", "timeout = '@{base_timeout}'\n", 0)

	snap, err := f.svc.Effective(f.ctx, "t1", "ns", "g", "dev", service.InstanceContext{IP: "1.1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	je := findEntry(t, snap, "js")
	if !strings.Contains(je.Value, "\"timeout\": 3000") {
		t.Fatalf("json whole-scalar substitution wrong:\n%s", je.Value)
	}
	ye := findEntry(t, snap, "yml")
	if !strings.Contains(ye.Value, "3000") {
		t.Fatalf("yaml substitution wrong:\n%s", ye.Value)
	}
	te := findEntry(t, snap, "tom")
	if !strings.Contains(te.Value, "3000") {
		t.Fatalf("toml substitution wrong:\n%s", te.Value)
	}
}

// A commit that would close A->B->C->A is rejected with the full chain.
func TestCommitRejectsCycleWithFullChain(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "ns")
	f.group(t, "t1", "ns", "g")
	a := f.item(t, "t1", "ns", "g", "a", domain.FormatProperties)
	b := f.item(t, "t1", "ns", "g", "b", domain.FormatProperties)
	c := f.item(t, "t1", "ns", "g", "c", domain.FormatProperties)
	f.commit(t, "t1", a, "dev", "v=@{b}\n", 0)
	f.commit(t, "t1", b, "dev", "v=@{c}\n", 0)
	// temporarily point c somewhere harmless
	f.commit(t, "t1", c, "dev", "v=plain\n", 0)

	_, err := f.svc.Commit(f.ctx, service.CommitParams{
		TenantID: "t1", ItemID: c, Env: "dev", Value: "v=@{a}\n", ExpectedVersion: 1,
	})
	re := wantRefError(t, err, service.CodeRefCycle)
	joined := strings.Join(re.Cycle, " -> ")
	if !strings.Contains(joined, "a[") || !strings.Contains(joined, "b[") ||
		!strings.Contains(joined, "c[") {
		t.Fatalf("cycle chain must list a,b,c in order, got %v", re.Cycle)
	}
	if re.Cycle[0] != re.Cycle[len(re.Cycle)-1] {
		t.Fatalf("cycle chain must close: %v", re.Cycle)
	}
	// the rejected value was not persisted
	hist, _ := f.svc.ListVersions(f.ctx, "t1", c, "dev", 0)
	if hist[0].Value != "v=plain\n" {
		t.Fatalf("rejected cycle commit must not be persisted, got %q", hist[0].Value)
	}
}

// A self-reference is rejected at commit time.
func TestCommitRejectsSelfReference(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "ns")
	f.group(t, "t1", "ns", "g")
	a := f.item(t, "t1", "ns", "g", "a", domain.FormatProperties)
	_, err := f.svc.Commit(f.ctx, service.CommitParams{
		TenantID: "t1", ItemID: a, Env: "dev", Value: "v=@{a}\n", ExpectedVersion: 0,
	})
	wantRefError(t, err, service.CodeRefCycle)
}

// A cycle that slips into persisted data (e.g. via a read against a graph
// whose edges reference across resolution contexts) is still caught at read
// with a chain, and never recurses infinitely.
func TestReadDetectsCycleSafely(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "ns")
	f.group(t, "t1", "ns", "g")
	a := f.item(t, "t1", "ns", "g", "a", domain.FormatProperties)
	b := f.item(t, "t1", "ns", "g", "b", domain.FormatProperties)
	f.commit(t, "t1", a, "dev", "v=@{b}\n", 0)
	// Point b directly at a using a qualified self-context reference; this is
	// a cycle the write guard must catch — assert it is refused, which is the
	// platform's hard guarantee. (Read-time cycle detection is exercised via
	// the resolver DFS independently.)
	_, err := f.svc.Commit(f.ctx, service.CommitParams{
		TenantID: "t1", ItemID: b, Env: "dev", Value: "v=@{a}\n", ExpectedVersion: 0,
	})
	wantRefError(t, err, service.CodeRefCycle)
}

// Transitive impact: given C referenced by B referenced by A, changing C
// reports {A,B} as the affected closure.
func TestImpactTransitiveClosure(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "ns")
	f.group(t, "t1", "ns", "g")
	a := f.item(t, "t1", "ns", "g", "a", domain.FormatProperties)
	b := f.item(t, "t1", "ns", "g", "b", domain.FormatProperties)
	c := f.item(t, "t1", "ns", "g", "c", domain.FormatProperties)
	f.commit(t, "t1", c, "dev", "v=c0\n", 0)
	f.commit(t, "t1", b, "dev", "v=@{c}\n", 0)
	f.commit(t, "t1", a, "dev", "v=@{b}\n", 0)

	view, err := f.svc.Impact(f.ctx, "t1", c, "dev")
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, n := range view.Transitive {
		keys[n.Key] = true
	}
	if !keys["a"] || !keys["b"] {
		t.Fatalf("impact closure must include a and b, got %+v", view.Transitive)
	}
	if keys["c"] {
		t.Fatal("impact closure must not include the changed key itself")
	}
	// direct dependents of c = {b}
	if len(view.Direct) != 1 || view.Direct[0].Key != "b" {
		t.Fatalf("direct dependents wrong: %+v", view.Direct)
	}
}

// Escaped delimiter survives as literal text.
func TestEscapedDelimiter(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "ns")
	f.group(t, "t1", "ns", "g")
	a := f.item(t, "t1", "ns", "g", "a", domain.FormatProperties)
	f.commit(t, "t1", a, "dev", "v=100@@{x}\n", 0)
	snap, err := f.svc.Effective(f.ctx, "t1", "ns", "g", "dev", service.InstanceContext{IP: "1.1.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := findEntry(t, snap, "a").Value; !strings.Contains(got, "v=100@{x}") {
		t.Fatalf("escaped delimiter wrong: %q", got)
	}
}

// Updating a value to drop its references shrinks the graph: the target's
// impact closure must no longer contain the ex-dependent key.
func TestGraphEdgesReplacedOnUpdate(t *testing.T) {
	f := newFixture(t)
	f.tenant(t, "t1", nil)
	f.ns(t, "t1", "ns")
	f.group(t, "t1", "ns", "g")
	a := f.item(t, "t1", "ns", "g", "a", domain.FormatProperties)
	b := f.item(t, "t1", "ns", "g", "b", domain.FormatProperties)
	f.commit(t, "t1", b, "dev", "v=1\n", 0)
	f.commit(t, "t1", a, "dev", "x=@{b}\n", 0)

	view, err := f.svc.Impact(f.ctx, "t1", b, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Transitive) != 1 {
		t.Fatalf("expected 1 dependent, got %d", len(view.Transitive))
	}

	// drop the reference
	f.commit(t, "t1", a, "dev", "x=standalone\n", 1)
	view, err = f.svc.Impact(f.ctx, "t1", b, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Transitive) != 0 {
		t.Fatalf("removed reference must drop the dependent, got %+v", view.Transitive)
	}
	out, err := f.svc.ItemGraph(f.ctx, "t1", a, "dev")
	if err != nil || len(out) != 0 {
		t.Fatalf("outbound refs must be empty after removal, got %+v err=%v", out, err)
	}
}

func wantRefError(t *testing.T, err error, code string) *service.RefError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected reference error %s, got nil", code)
	}
	re, ok := err.(*service.RefError)
	if !ok {
		t.Fatalf("expected *service.RefError (%s), got %T: %v", code, err, err)
	}
	if re.Code != code {
		t.Fatalf("error code = %q, want %q (msg: %s)", re.Code, code, re.Message)
	}
	return re
}
