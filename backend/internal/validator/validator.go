// Package validator performs syntax validation for the four supported
// configuration formats and, optionally, JSON Schema validation. Every
// rejection carries a concrete location/reason rather than a generic failure.
package validator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	gojsonschema "github.com/xeipuuv/gojsonschema"
	"gopkg.in/yaml.v3"

	"configcenter/internal/domain"
)

// Error describes a single validation problem.
type Error struct {
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Field   string `json:"field,omitempty"`
	Rule    string `json:"rule,omitempty"`
	Message string `json:"message"`
}

// Outcome is the result of validating one configuration value.
type Outcome struct {
	Valid  bool     `json:"valid"`
	Errors []*Error `json:"errors,omitempty"`
}

func fail(errs ...*Error) *Outcome { return &Outcome{Valid: false, Errors: errs} }
func ok() *Outcome                 { return &Outcome{Valid: true} }

// Validate checks raw content against its format's grammar and, when schema is
// non-empty, against that JSON Schema. Content must parse to an object root.
func Validate(format domain.Format, content, schema string) *Outcome {
	parsed, errs := parseRoot(format, content)
	if len(errs) > 0 {
		return fail(errs...)
	}
	if strings.TrimSpace(schema) != "" {
		if schemaErrs := validateSchema(format, parsed, schema); len(schemaErrs) > 0 {
			return fail(schemaErrs...)
		}
	}
	return ok()
}

// ValidateSchemaText checks that a schema document is itself a valid JSON
// Schema that the platform can apply.
func ValidateSchemaText(schema string) *Outcome {
	if strings.TrimSpace(schema) == "" {
		return ok()
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(schema), &root); err != nil {
		line, col := positionAt(schema, errOffset(err))
		return fail(&Error{Line: line, Column: col, Rule: "schema_syntax",
			Message: fmt.Sprintf("schema is not valid JSON: %s", err.Error())})
	}
	loader := gojsonschema.NewSchemaLoader()
	loader.Validate = true // we only want to compile-check here
	if _, err := loader.Compile(gojsonschema.NewStringLoader(schema)); err != nil {
		return fail(&Error{Rule: "schema", Message: fmt.Sprintf("invalid JSON Schema: %s", err.Error())})
	}
	return ok()
}

// parseRoot parses content into a generic map and returns every syntax error.
func parseRoot(format domain.Format, content string) (map[string]any, []*Error) {
	if strings.TrimSpace(content) == "" {
		return nil, []*Error{{Line: 1, Rule: "empty", Message: "configuration content is empty"}}
	}
	switch format {
	case domain.FormatJSON:
		return parseJSON(content)
	case domain.FormatYAML:
		return parseYAML(content)
	case domain.FormatProperties:
		m, errs := parseProperties(content)
		return m, errs
	case domain.FormatTOML:
		return parseTOML(content)
	default:
		return nil, []*Error{{Rule: "format", Message: fmt.Sprintf("unsupported format %q", format)}}
	}
}

func parseJSON(content string) (map[string]any, []*Error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		line, col := positionAt(content, errOffset(err))
		return nil, []*Error{{Line: line, Column: col, Rule: "syntax",
			Message: fmt.Sprintf("invalid JSON: %s", jsonErrText(err))}}
	}
	if m == nil {
		return nil, []*Error{{Line: 1, Rule: "root", Message: "configuration root must be a JSON object, got null"}}
	}
	return m, nil
}

func parseYAML(content string) (map[string]any, []*Error) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(content), &node); err != nil {
		return nil, []*Error{{Line: yamlLine(err), Rule: "syntax",
			Message: fmt.Sprintf("invalid YAML: %s", err.Error())}}
	}
	var v any
	if err := node.Decode(&v); err != nil {
		return nil, []*Error{{Line: yamlLine(err), Rule: "syntax",
			Message: fmt.Sprintf("invalid YAML: %s", err.Error())}}
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, []*Error{{Line: 1, Rule: "root", Message: "configuration root must be a YAML mapping"}}
	}
	return m, nil
}

func parseTOML(content string) (map[string]any, []*Error) {
	var m map[string]any
	if _, err := toml.Decode(content, &m); err != nil {
		line := tomlLine(err)
		return nil, []*Error{{Line: line, Rule: "syntax",
			Message: fmt.Sprintf("invalid TOML: %s", err.Error())}}
	}
	return m, nil
}

// parseProperties implements a Java-properties style grammar:
// key=value / key:value / whitespace separator, # and ! comment lines,
// blank lines, backslash line continuation. Blank keys are rejected.
func parseProperties(content string) (map[string]any, []*Error) {
	// join continuation lines first, remembering the original start line.
	type logical struct {
		text      string
		startLine int
	}
	var logicals []logical
	rawLines := strings.Split(content, "\n")
	for i := 0; i < len(rawLines); i++ {
		start := i + 1
		line := rawLines[i]
		for strings.HasSuffix(strings.TrimRight(line, "\r"), "\\") && i+1 < len(rawLines) {
			line = strings.TrimRight(line, "\r")
			line = line[:len(line)-1] + strings.TrimLeft(rawLines[i+1], " \t")
			i++
		}
		logicals = append(logicals, logical{text: strings.TrimRight(line, "\r"), startLine: start})
	}

	out := make(map[string]any)
	var errs []*Error
	seen := map[string]bool{}
	for _, lg := range logicals {
		line := strings.TrimLeft(lg.text, " \t")
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		key, val, found := splitProperty(line)
		if !found {
			errs = append(errs, &Error{Line: lg.startLine, Rule: "syntax",
				Message: fmt.Sprintf("invalid property line %q: expected key=value", line)})
			continue
		}
		key = decodeEscapes(strings.TrimSpace(key))
		if key == "" {
			errs = append(errs, &Error{Line: lg.startLine, Rule: "empty_key", Message: "property key must not be blank"})
			continue
		}
		_ = val
		if seen[key] {
			errs = append(errs, &Error{Line: lg.startLine, Rule: "duplicate_key",
				Message: fmt.Sprintf("duplicate property key %q", key)})
			continue
		}
		seen[key] = true
		out[key] = decodeEscapes(val)
	}
	return out, errs
}

// splitProperty splits on the first unescaped '=', ':' or run of whitespace.
func splitProperty(line string) (key, val string, found bool) {
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' {
			i++ // skip escaped char
			continue
		}
		switch runes[i] {
		case '=', ':':
			return string(runes[:i]), strings.TrimLeft(string(runes[i+1:]), " \t"), true
		case ' ', '\t':
			rest := strings.TrimLeft(string(runes[i+1:]), " \t")
			if rest == "" {
				return string(runes[:i]), "", true
			}
			if rest[0] == '=' || rest[0] == ':' {
				rest = rest[1:]
			}
			return string(runes[:i]), strings.TrimLeft(rest, " \t"), true
		}
	}
	// A lone key with no separator is allowed (empty value).
	return line, "", true
}

func decodeEscapes(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		if r[i] != '\\' || i+1 >= len(r) {
			b.WriteRune(r[i])
			continue
		}
		i++
		switch r[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'u':
			if i+4 < len(r) {
				var code rune
				if _, err := fmt.Sscanf(string(r[i+1:i+5]), "%x", &code); err == nil {
					b.WriteRune(code)
					i += 4
					continue
				}
			}
			b.WriteRune('u')
		default:
			b.WriteRune(r[i])
		}
	}
	return b.String()
}

// validateSchema serializes the parsed value to JSON and runs the schema.
func validateSchema(format domain.Format, parsed map[string]any, schema string) []*Error {
	docJSON, err := json.Marshal(parsed)
	if err != nil {
		return []*Error{{Rule: "schema", Message: fmt.Sprintf("cannot encode value for schema check: %s", err)}}
	}
	schemaLoader := gojsonschema.NewStringLoader(schema)
	docLoader := gojsonschema.NewBytesLoader(docJSON)
	result, err := gojsonschema.Validate(schemaLoader, docLoader)
	if err != nil {
		// Most likely an invalid schema; report it as a schema problem.
		return []*Error{{Rule: "schema", Message: fmt.Sprintf("failed to apply JSON Schema: %s", err)}}
	}
	if result.Valid() {
		return nil
	}
	var errs []*Error
	for _, e := range result.Errors() {
		field := strings.TrimPrefix(e.Field(), "(root)")
		field = strings.TrimPrefix(field, ".")
		errs = append(errs, &Error{
			Field:   field,
			Rule:    e.Type(),
			Message: fmt.Sprintf("%s: %s", fieldOrRoot(field), e.Description()),
		})
	}
	return errs
}

func fieldOrRoot(f string) string {
	if f == "" {
		return "(root)"
	}
	return f
}

// --- helpers for extracting line/column from parser errors ---

var lineRe = regexp.MustCompile(`line (\d+)`)

func yamlLine(err error) int {
	if m := lineRe.FindStringSubmatch(err.Error()); m != nil {
		return atoi(m[1])
	}
	return 1
}

func tomlLine(err error) int {
	if m := lineRe.FindStringSubmatch(err.Error()); m != nil {
		return atoi(m[1])
	}
	return 1
}

func errOffset(err error) int64 {
	if se, ok := err.(*json.SyntaxError); ok {
		return se.Offset
	}
	if te, ok := err.(*json.UnmarshalTypeError); ok {
		return te.Offset
	}
	return 0
}

func jsonErrText(err error) string {
	if se, ok := err.(*json.SyntaxError); ok {
		// Offset is already in the message; keep the tail only.
		msg := se.Error()
		if i := strings.Index(msg, " "); i >= 0 {
			return msg[i+1:]
		}
	}
	return err.Error()
}

// positionAt converts a byte offset into 1-based line and column.
func positionAt(content string, offset int64) (int, int) {
	if offset <= 0 {
		return 1, 1
	}
	off := int(offset)
	if off > len(content) {
		off = len(content)
	}
	line, col := 1, 1
	for i := 0; i < off; i++ {
		if content[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// EnsureBytesRoundtrip is used by tests/tooling to canonicalize values.
func EnsureBytesRoundtrip(format domain.Format, in string) (string, error) {
	m, errs := parseRoot(format, in)
	if len(errs) > 0 {
		var buf bytes.Buffer
		for _, e := range errs {
			buf.WriteString(e.Message)
			buf.WriteByte('\n')
		}
		return "", fmt.Errorf("%s", strings.TrimSpace(buf.String()))
	}
	b, err := json.Marshal(m)
	return string(b), err
}
