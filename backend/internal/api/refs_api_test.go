package api

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// createItemFromAPI provisions one properties item and returns its id.
func createItemFromAPI(t *testing.T, h *harness, hdr map[string]string, ns, g, key string) string {
	t.Helper()
	code, body := doJSON(t, "POST", h.srv.URL+"/api/namespaces/"+ns+"/groups/"+g+"/items", hdr,
		`{"key":"`+key+`","format":"properties"}`)
	if code != 201 {
		t.Fatalf("create item %s: %d %v", key, code, body)
	}
	return body["id"].(string)
}

func setupRefWorld(t *testing.T, h *harness) map[string]string {
	hdr := map[string]string{"X-Tenant-ID": "t1"}
	if code, _ := doJSON(t, "POST", h.srv.URL+"/api/tenants", nil, `{"id":"t1","name":"T1"}`); code != 201 {
		t.Fatalf("tenant %d", code)
	}
	doJSON(t, "POST", h.srv.URL+"/api/namespaces", hdr, `{"id":"ns","name":"NS"}`)
	doJSON(t, "POST", h.srv.URL+"/api/namespaces/ns/groups", hdr, `{"id":"g","name":"G"}`)
	ids := map[string]string{}
	ids["c"] = createItemFromAPI(t, h, hdr, "ns", "g", "c")
	ids["b"] = createItemFromAPI(t, h, hdr, "ns", "g", "b")
	ids["a"] = createItemFromAPI(t, h, hdr, "ns", "g", "a")
	return ids
}

// A missing target is rejected with a structured 422 carrying the code.
func TestAPICommitRejectsMissingTarget(t *testing.T) {
	h := newHarness(t)
	ids := setupRefWorld(t, h)
	hdr := map[string]string{"X-Tenant-ID": "t1"}
	code, body := doJSON(t, "POST", h.srv.URL+"/api/items/"+ids["a"]+"/values", hdr,
		`{"env":"dev","value":"v=@{ghost}\n"}`)
	if code != 422 {
		t.Fatalf("want 422, got %d body=%v", code, body)
	}
	if body["code"] != "ref_missing_target" {
		t.Fatalf("want ref_missing_target, got %v", body)
	}
	if !strings.Contains(body["target"].(string), "ghost") {
		t.Fatalf("error must name the missing target: %v", body)
	}
}

// A closing cycle is rejected at commit with the full chain.
func TestAPICommitRejectsCycle(t *testing.T) {
	h := newHarness(t)
	ids := setupRefWorld(t, h)
	hdr := map[string]string{"X-Tenant-ID": "t1"}
	commit := func(id, val string, expected int) {
		t.Helper()
		escaped := strings.ReplaceAll(val, "\n", `\n`)
		body := `{"env":"dev","value":"` + escaped + `"`
		if expected >= 0 {
			body += `,"expected_version":` + itoaTest(expected)
		}
		body += `}`
		if code, rb := doJSON(t, "POST", h.srv.URL+"/api/items/"+id+"/values", hdr, body); code != 201 {
			t.Fatalf("setup commit %s: %d %v", val, code, rb)
		}
	}
	commit(ids["a"], "v=@{b}\n", 0)
	commit(ids["b"], "v=@{c}\n", 0)
	commit(ids["c"], "v=plain\n", 0)

	code, body := doJSON(t, "POST", h.srv.URL+"/api/items/"+ids["c"]+"/values", hdr,
		`{"env":"dev","value":"v=@{a}\n","expected_version":1}`)
	if code != 422 || body["code"] != "ref_cycle" {
		t.Fatalf("want 422 ref_cycle, got %d %v", code, body)
	}
	cycle, _ := body["cycle"].([]any)
	if len(cycle) < 3 {
		t.Fatalf("cycle chain too short: %v", cycle)
	}
	first, _ := cycle[0].(string)
	last, _ := cycle[len(cycle)-1].(string)
	if first != last {
		t.Fatalf("cycle chain must close: %v", cycle)
	}
}

// The impact endpoint reports the transitive closure before an edit.
func TestAPIImpactPreview(t *testing.T) {
	h := newHarness(t)
	ids := setupRefWorld(t, h)
	hdr := map[string]string{"X-Tenant-ID": "t1"}
	commit := func(id, val string) {
		escaped := strings.ReplaceAll(val, "\n", `\n`)
		if code, rb := doJSON(t, "POST", h.srv.URL+"/api/items/"+id+"/values", hdr,
			`{"env":"dev","value":"`+escaped+`"}`); code != 201 {
			t.Fatalf("commit %s: %d %v", val, code, rb)
		}
	}
	commit(ids["c"], "v=c\n")
	commit(ids["b"], "v=@{c}\n")
	commit(ids["a"], "v=@{b}\n")

	code, body := doJSON(t, "GET", h.srv.URL+"/api/items/"+ids["c"]+"/impact?env=dev", hdr, "")
	if code != 200 {
		t.Fatalf("impact: %d %v", code, body)
	}
	trans, _ := body["transitive"].([]any)
	keys := map[string]bool{}
	for _, x := range trans {
		if m, ok := x.(map[string]any); ok {
			keys[m["key"].(string)] = true
		}
	}
	if !keys["a"] || !keys["b"] {
		t.Fatalf("impact closure must contain a and b: %v", trans)
	}

	// outbound references endpoint
	code, body = doJSON(t, "GET", h.srv.URL+"/api/items/"+ids["a"]+"/refs?env=dev", hdr, "")
	if code != 200 {
		t.Fatalf("refs: %d", code)
	}
	refs, _ := body["references"].([]any)
	if len(refs) != 1 {
		t.Fatalf("a should reference exactly one key, got %v", refs)
	}
}

// Effective resolution dereferences and validates end to end over HTTP.
func TestAPIEffectiveResolvesReference(t *testing.T) {
	h := newHarness(t)
	ids := setupRefWorld(t, h)
	hdr := map[string]string{"X-Tenant-ID": "t1"}
	commit := func(id, val string) {
		escaped := strings.ReplaceAll(val, "\n", `\n`)
		if code, rb := doJSON(t, "POST", h.srv.URL+"/api/items/"+id+"/values", hdr,
			`{"env":"dev","value":"`+escaped+`"}`); code != 201 {
			t.Fatalf("commit %s: %d %v", val, code, rb)
		}
	}
	commit(ids["c"], "v=final\n")
	commit(ids["b"], "m=@{c}\n")
	commit(ids["a"], "z=@{b}\n")

	resp := getTenant(t, h.srv.URL+"/api/effective?namespace=ns&group=g&env=dev")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	entries, _ := body["entries"].([]any)
	got := map[string]string{}
	for _, e := range entries {
		m := e.(map[string]any)
		got[m["key"].(string)] = m["value"].(string)
	}
	if !strings.Contains(got["a"], "z=final") {
		t.Fatalf("chained reference not resolved over API: %q", got["a"])
	}
}

func itoaTest(n int) string {
	if n < 0 {
		return "0"
	}
	digits := ""
	if n == 0 {
		return "0"
	}
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
