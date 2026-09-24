package ref

import (
	"strings"
	"testing"
)

func TestMaskJSONStructuralPlaceholder(t *testing.T) {
	got, err := Mask(`{"timeout": @{base}, "name": "x"}`, MaskJSON)
	if err != nil {
		t.Fatal(err)
	}
	// structural token became a quoted sentinel, embedded text untouched
	if UnmaskMarker(extractMarker(got)) == "" {
		t.Fatalf("no marker in masked JSON: %q", got)
	}
}

func TestMaskKeepsQuotedPlaceholder(t *testing.T) {
	raw := `{"url": "jdbc:@{host}/db"}`
	got, err := Mask(raw, MaskJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got != raw {
		t.Fatalf("quoted placeholder must stay literal:\nwant %q\n got %q", raw, got)
	}
}

func TestMaskYAMLQuotedAndStructural(t *testing.T) {
	raw := "a: '@{x}'\nb: @{x}\n"
	got, err := Mask(raw, MaskYAML)
	if err != nil {
		t.Fatal(err)
	}
	// quoted placeholder stays literal text; unquoted one is a structural
	// scalar position and becomes a sentinel.
	if !strings.Contains(got, "'@{x}'") {
		t.Fatalf("quoted YAML placeholder must stay literal, got %q", got)
	}
	if UnmaskMarker(extractMarker(got)) != "@{x}" {
		t.Fatalf("unquoted YAML placeholder must be masked structurally, got %q", got)
	}
}

func TestMaskYAMLDoubleQuoted(t *testing.T) {
	raw := "a: \"p-@{x}-q\"\n"
	got, err := Mask(raw, MaskYAML)
	if err != nil {
		t.Fatal(err)
	}
	if got != raw {
		t.Fatalf("double-quoted YAML placeholder must stay literal, got %q", got)
	}
}

func TestMaskTOMLUnquotedStructural(t *testing.T) {
	got, err := Mask(`port = @{p}`+"\n", MaskTOML)
	if err != nil {
		t.Fatal(err)
	}
	if UnmaskMarker(extractMarker(got)) == "" {
		t.Fatalf("unquoted TOML placeholder must be masked: %q", got)
	}
}

func TestMaskNonePassthrough(t *testing.T) {
	// Text formats are never parsed structurally; masking is a pass-through
	// and escaped delimiters are collapsed later by the resolver.
	raw := "a=@{b}\n"
	got, err := Mask(raw, MaskNone)
	if err != nil || got != raw {
		t.Fatalf("MaskNone must pass through verbatim: got %q err=%v", got, err)
	}
}

func TestMaskBypassForOldDocs(t *testing.T) {
	for _, k := range []MaskKind{MaskJSON, MaskYAML, MaskTOML} {
		raw := "nothing to see: $5 {ordinary}"
		got, err := Mask(raw, k)
		if err != nil || got != raw {
			t.Fatalf("reference-free doc altered for kind %d: %q", k, got)
		}
	}
}

func extractMarker(s string) string {
	i := indexStr(s, markerStart)
	if i < 0 {
		return ""
	}
	j := indexStrFrom(s, markerEnd, i)
	if j < 0 {
		return ""
	}
	return s[i : j+len(markerEnd)]
}

func indexStr(s, sub string) int { return indexStrFrom(s, sub, 0) }

func indexStrFrom(s, sub string, from int) int {
	for i := from; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
