package merge

import (
	"strings"
	"testing"

	"configcenter/internal/domain"
)

// Backward compatibility: documents without any placeholder must decode and
// render byte-identically to before the reference layer existed (masking is a
// no-op and the resolver leaves the merge render untouched).
func TestReferenceFreeDocumentsUnchanged(t *testing.T) {
	cases := []struct {
		format domain.Format
		value  string
	}{
		{domain.FormatJSON, "{\n  \"db\": {\"host\": \"h\", \"port\": 5432},\n  \"flag\": true\n}\n"},
		{domain.FormatYAML, "db:\n  host: h\n  port: 5432\n"},
		{domain.FormatProperties, "host=h\nport=5432\n"},
		{domain.FormatTOML, "host = \"h\"\nport = 5432\n"},
		// ordinary braces, dollars, currency and a lone @ must be untouched
		{domain.FormatProperties, "msg=price is $5 {usd} email@x.com\n"},
	}
	for _, c := range cases {
		root, err := Decode(c.format, c.value)
		if err != nil {
			t.Fatalf("%s decode: %v", c.format, err)
		}
		out, err := Encode(c.format, root)
		if err != nil {
			t.Fatalf("%s encode: %v", c.format, err)
		}
		res, err := Merge(c.format, []LayerInput{
			{Layer: domain.LayerGroup, Format: c.format, Value: c.value, Version: 1},
		})
		if err != nil {
			t.Fatalf("%s merge: %v", c.format, err)
		}
		if strings.Contains(res.Render, "") {
			t.Fatalf("%s render leaked a marker: %q", c.format, res.Render)
		}
		if strings.Contains(c.value, "$5") && !strings.Contains(out, "$5") {
			t.Fatalf("currency text altered: %q", out)
		}
		if out != res.Render {
			t.Fatalf("%s decode/encode and merge render differ:\n%q\n%q", c.format, out, res.Render)
		}
	}
}
