package merge

import (
	"testing"

	"configcenter/internal/domain"
)

// Core invariant: changing an upper-layer nested field updates only the
// sub-paths the lower layer did not cover; covered paths stay on the lower
// value and every leaf is attributed to the layer it actually came from.
func TestDeepMergeNestedOverrideAndProvenance(t *testing.T) {
	public := LayerInput{
		Layer: domain.LayerPublic, Format: domain.FormatJSON, Version: 1,
		Value: `{"db":{"host":"shared-db","port":5432,"ssl":true},"region":"cn"}`,
	}
	ns := LayerInput{
		Layer: domain.LayerNamespace, Format: domain.FormatJSON, Version: 3,
		Value: `{"db":{"port":6543}}`,
	}
	group := LayerInput{
		Layer: domain.LayerGroup, Format: domain.FormatJSON, Version: 7,
		Value: `{"db":{"host":"dedicated-db"}}`,
	}
	res, err := Merge(domain.FormatJSON, []LayerInput{group, public, ns}) // out of order on purpose
	if err != nil {
		t.Fatal(err)
	}
	db := res.Root["db"].(map[string]any)
	if db["host"] != "dedicated-db" {
		t.Fatalf("host should be group value, got %v", db["host"])
	}
	if db["port"].(float64) != 6543 {
		t.Fatalf("port should be namespace value, got %v", db["port"])
	}
	if db["ssl"] != true {
		t.Fatalf("ssl should survive from public layer, got %v", db["ssl"])
	}
	if res.Root["region"] != "cn" {
		t.Fatalf("region should survive from public layer, got %v", res.Root["region"])
	}

	src := map[string]domain.Layer{}
	ver := map[string]int64{}
	for _, k := range res.Keys {
		src[k.Path] = k.Source
		ver[k.Path] = k.SourceVersion
	}
	want := map[string]domain.Layer{
		"db.host": domain.LayerGroup,
		"db.port": domain.LayerNamespace,
		"db.ssl":  domain.LayerPublic,
		"region":  domain.LayerPublic,
	}
	for path, layer := range want {
		if src[path] != layer {
			t.Fatalf("provenance for %s = %s, want %s", path, src[path], layer)
		}
	}
	if ver["db.host"] != 7 || ver["db.port"] != 3 || ver["db.ssl"] != 1 {
		t.Fatalf("source versions wrong: %+v", ver)
	}
}

// Mutating the upper layer's uncovered nested field must flow through while
// covered fields remain fixed.
func TestUpperLayerChangeFlowsToUncoveredPaths(t *testing.T) {
	publicV1 := LayerInput{Layer: domain.LayerPublic, Format: domain.FormatJSON, Version: 1,
		Value: `{"log":{"level":"info","format":"text"}}`}
	group := LayerInput{Layer: domain.LayerGroup, Format: domain.FormatJSON, Version: 2,
		Value: `{"log":{"level":"debug"}}`}

	r1, err := Merge(domain.FormatJSON, []LayerInput{publicV1, group})
	if err != nil {
		t.Fatal(err)
	}
	log1 := r1.Root["log"].(map[string]any)
	if log1["level"] != "debug" || log1["format"] != "text" {
		t.Fatalf("unexpected initial merge: %+v", log1)
	}

	publicV2 := publicV1
	publicV2.Version = 3
	publicV2.Value = `{"log":{"level":"info","format":"json"}}`
	r2, err := Merge(domain.FormatJSON, []LayerInput{publicV2, group})
	if err != nil {
		t.Fatal(err)
	}
	log2 := r2.Root["log"].(map[string]any)
	if log2["level"] != "debug" {
		t.Fatal("covered level must not change when upper layer changes")
	}
	if log2["format"] != "json" {
		t.Fatal("uncovered format must follow the upper layer update")
	}
}

// Whole-object replacement is wrong: the lower object must not be replaced by
// the upper object's shape when it only defines one field.
func TestLowerObjectIsNotWholeReplaced(t *testing.T) {
	public := LayerInput{Layer: domain.LayerPublic, Format: domain.FormatJSON, Version: 1,
		Value: `{"feature":{"a":true,"b":true}}`}
	group := LayerInput{Layer: domain.LayerGroup, Format: domain.FormatJSON, Version: 1,
		Value: `{"feature":{"a":false}}`}
	res, err := Merge(domain.FormatJSON, []LayerInput{public, group})
	if err != nil {
		t.Fatal(err)
	}
	feat := res.Root["feature"].(map[string]any)
	if feat["a"] != false || feat["b"] != true {
		t.Fatalf("deep merge broken: %+v", feat)
	}
}

func TestKeyLevelOverrideForPropertiesAndTOML(t *testing.T) {
	public := LayerInput{Layer: domain.LayerPublic, Format: domain.FormatProperties, Version: 1,
		Value: "a=1\nb=2\n"}
	group := LayerInput{Layer: domain.LayerGroup, Format: domain.FormatProperties, Version: 9,
		Value: "a=100\n"}
	res, err := Merge(domain.FormatProperties, []LayerInput{public, group})
	if err != nil {
		t.Fatal(err)
	}
	if res.Root["a"] != "100" || res.Root["b"] != "2" {
		t.Fatalf("properties key override broken: %+v", res.Root)
	}
	byKey := map[string]domain.Layer{}
	for _, k := range res.Keys {
		byKey[k.Path] = k.Source
	}
	if byKey["a"] != domain.LayerGroup || byKey["b"] != domain.LayerPublic {
		t.Fatalf("properties provenance wrong: %+v", byKey)
	}

	// TOML: nested sections exist but override is at the top-level key,
	// meaning the group's whole [server] table wins over the public one.
	tomlPublic := LayerInput{Layer: domain.LayerPublic, Format: domain.FormatTOML, Version: 1,
		Value: "[server]\nhost = \"h1\"\nport = 1\n"}
	tomlGroup := LayerInput{Layer: domain.LayerGroup, Format: domain.FormatTOML, Version: 2,
		Value: "[server]\nhost = \"h2\"\n"}
	tres, err := Merge(domain.FormatTOML, []LayerInput{tomlPublic, tomlGroup})
	if err != nil {
		t.Fatal(err)
	}
	server := tres.Root["server"].(map[string]any)
	if _, hasPort := server["port"]; hasPort {
		t.Fatalf("TOML must override at key level, port leaked: %+v", server)
	}
	if server["host"] != "h2" {
		t.Fatalf("TOML group table should win: %+v", server)
	}
}

func TestYAMLDeepMerge(t *testing.T) {
	public := LayerInput{Layer: domain.LayerPublic, Format: domain.FormatYAML, Version: 1,
		Value: "cache:\n  ttl: 30\n  mode: redis\n"}
	ns := LayerInput{Layer: domain.LayerNamespace, Format: domain.FormatYAML, Version: 4,
		Value: "cache:\n  ttl: 60\n"}
	res, err := Merge(domain.FormatYAML, []LayerInput{public, ns})
	if err != nil {
		t.Fatal(err)
	}
	cache := res.Root["cache"].(map[string]any)
	if cache["ttl"].(int) != 60 || cache["mode"] != "redis" {
		t.Fatalf("yaml deep merge broken: %+v", cache)
	}
}
