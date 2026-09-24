package validator

import (
	"strings"
	"testing"

	"configcenter/internal/domain"
)

func TestValidateJSON(t *testing.T) {
	if got := Validate(domain.FormatJSON, `{"a":1}`, ""); !got.Valid {
		t.Fatalf("valid json rejected: %+v", got.Errors)
	}
	got := Validate(domain.FormatJSON, `{"a": }`, "")
	if got.Valid {
		t.Fatal("invalid json accepted")
	}
	if got.Errors[0].Line < 1 || !strings.Contains(got.Errors[0].Message, "invalid JSON") {
		t.Fatalf("expected line-annotated JSON error, got %+v", got.Errors[0])
	}
	// root must be an object
	if got := Validate(domain.FormatJSON, `[1,2,3]`, ""); got.Valid {
		t.Fatal("array root accepted")
	}
}

func TestValidateYAML(t *testing.T) {
	if got := Validate(domain.FormatYAML, "a:\n  b: 1\n", ""); !got.Valid {
		t.Fatalf("valid yaml rejected: %+v", got.Errors)
	}
	// tab indentation is a YAML syntax error
	got := Validate(domain.FormatYAML, "a:\n\tb: 1\n", "")
	if got.Valid {
		t.Fatal("invalid yaml accepted")
	}
	if !strings.Contains(strings.ToLower(got.Errors[0].Message), "invalid yaml") {
		t.Fatalf("unexpected message: %+v", got.Errors[0])
	}
}

func TestValidateProperties(t *testing.T) {
	ok := `
# comment line
server.host = 127.0.0.1
server.port: 8080
feature.flag true
multiline=hello\nworld
`
	if got := Validate(domain.FormatProperties, ok, ""); !got.Valid {
		t.Fatalf("valid properties rejected: %+v", got.Errors)
	}
	got := Validate(domain.FormatProperties, "a=1\n=orphan\n", "")
	if got.Valid {
		t.Fatal("invalid properties accepted")
	}
	found := false
	for _, e := range got.Errors {
		if e.Rule == "empty_key" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an empty_key error, got %+v", got.Errors)
	}
	// duplicate key must be rejected
	if got := Validate(domain.FormatProperties, "a=1\na=2\n", ""); got.Valid {
		t.Fatal("duplicate key accepted")
	}
}

func TestValidateTOML(t *testing.T) {
	ok := `title = "demo"
[server]
port = 8080
`
	if got := Validate(domain.FormatTOML, ok, ""); !got.Valid {
		t.Fatalf("valid toml rejected: %+v", got.Errors)
	}
	got := Validate(domain.FormatTOML, `title = "demo"
[server
port = 8080`, "")
	if got.Valid {
		t.Fatal("invalid toml accepted")
	}
	if got.Errors[0].Line < 2 {
		t.Fatalf("expected line number on toml error, got %+v", got.Errors[0])
	}
}

const personSchema = `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "minLength": 2},
    "age": {"type": "integer", "minimum": 0},
    "tags": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["name"]
}`

func TestSchemaValid(t *testing.T) {
	if got := Validate(domain.FormatJSON, `{"name":"ab","age":3}`, personSchema); !got.Valid {
		t.Fatalf("schema-valid doc rejected: %+v", got.Errors)
	}
}

func TestSchemaReportsFieldAndConstraint(t *testing.T) {
	got := Validate(domain.FormatJSON, `{"name":"x","age":-1}`, personSchema)
	if got.Valid {
		t.Fatal("schema-invalid doc accepted")
	}
	rules := map[string]bool{}
	for _, e := range got.Errors {
		rules[e.Rule] = true
		if e.Field == "" && e.Rule != "" {
			t.Fatalf("error missing field path: %+v", e)
		}
	}
	if !rules["number_gte"] {
		t.Fatalf("expected number_gte violation, got %+v", got.Errors)
	}
	if !rules["string_gte"] {
		t.Fatalf("expected minLength violation, got %+v", got.Errors)
	}
}

func TestSchemaMissingRequired(t *testing.T) {
	got := Validate(domain.FormatJSON, `{"age":10}`, personSchema)
	if got.Valid {
		t.Fatal("accepted doc without required name")
	}
	found := false
	for _, e := range got.Errors {
		if strings.Contains(e.Message, "name") && strings.Contains(e.Message, "required") {
			found = true
		}
	}
	if !found {
		t.Fatalf("required field error must name the field, got %+v", got.Errors)
	}
}

// Schema validation also applies to YAML after decoding.
func TestSchemaAppliesToYAML(t *testing.T) {
	got := Validate(domain.FormatYAML, "name: x\nage: -5\n", personSchema)
	if got.Valid {
		t.Fatal("schema-invalid yaml accepted")
	}
	if len(got.Errors) < 2 {
		t.Fatalf("expected both constraint violations, got %+v", got.Errors)
	}
}
