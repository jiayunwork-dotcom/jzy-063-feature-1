// Package merge implements the three-layer configuration merge kernel.
//
// The effective value of a configuration key is the superposition of:
//
//	public    - shared by every service
//	namespace - shared inside a business line
//	group     - specific to one service
//
// A lower (stronger) layer overrides an upper (weaker) one. JSON and YAML
// documents are deep-merged object by object; Properties and TOML documents
// are overridden at the top-level key. Every surviving leaf key keeps a
// reference to the layer (and version) it came from.
package merge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"configcenter/internal/domain"
	refmask "configcenter/internal/ref"
)

// LayerInput is one layer's contribution to a configuration key.
type LayerInput struct {
	Layer   domain.Layer
	Format  domain.Format
	Value   string
	Version int64
}

// KeyResult attributes one leaf key of the merged document to its source.
type KeyResult struct {
	Path          string
	Value         any
	Source        domain.Layer
	SourceVersion int64
}

// Result is the merged, serialized document with per-key provenance.
type Result struct {
	Format domain.Format
	Root   map[string]any
	Keys   []KeyResult // sorted by path
	Render string
}

// origin records where one leaf came from.
type origin struct {
	layer   domain.Layer
	version int64
}

// Merge combines the given layers. Layers may be passed in any order; they
// are sorted by override priority internally. The output format follows the
// strongest layer that contributes.
func Merge(target domain.Format, layers []LayerInput) (*Result, error) {
	if !target.Valid() {
		return nil, fmt.Errorf("merge: unknown format %q", target)
	}
	sorted := make([]LayerInput, 0, len(layers))
	for _, l := range layers {
		if strings.TrimSpace(l.Value) == "" {
			continue
		}
		sorted = append(sorted, l)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		return domain.LayerPriority[sorted[i].Layer] < domain.LayerPriority[sorted[j].Layer]
	})

	root := map[string]any{}
	sources := map[string]origin{}
	for _, l := range sorted {
		parsed, err := decode(l.Format, l.Value)
		if err != nil {
			return nil, fmt.Errorf("merge: cannot parse %s layer: %w", l.Layer, err)
		}
		switch target {
		case domain.FormatJSON, domain.FormatYAML:
			deepMerge(root, parsed, origin{layer: l.Layer, version: l.Version}, "", sources)
		default:
			keyOverride(root, parsed, origin{layer: l.Layer, version: l.Version}, sources)
		}
	}

	keys := make([]KeyResult, 0, len(sources))
	for path, o := range sources {
		keys = append(keys, KeyResult{
			Path:          path,
			Value:         leafAt(root, path),
			Source:        o.layer,
			SourceVersion: o.version,
		})
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Path < keys[j].Path })

	rendered, err := encode(target, root)
	if err != nil {
		return nil, fmt.Errorf("merge: cannot render %s: %w", target, err)
	}
	return &Result{Format: target, Root: root, Keys: keys, Render: rendered}, nil
}

// deepMerge walks weak -> strong maps. Nested maps are merged field by field;
// any other value is replaced wholesale, but only the leaves that the
// stronger layer actually defines change their source.
func deepMerge(dst, src map[string]any, o origin, prefix string, sources map[string]origin) {
	// Iterate keys deterministically for predictable behavior.
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		path := joinPath(prefix, k)
		sv := src[k]
		if srcMap, ok := sv.(map[string]any); ok {
			if dv, exists := dst[k]; exists {
				if dstMap, ok := dv.(map[string]any); ok {
					// Both sides are objects: descend instead of replacing,
					// so upper-layer fields not covered here stay untouched.
					deepMerge(dstMap, srcMap, o, path, sources)
					continue
				}
			}
			// Insert a copy so later merges cannot alias src.
			cp := cloneMap(srcMap)
			dst[k] = cp
			markLeaves(cp, o, path, sources)
			continue
		}
		dst[k] = sv
		sources[path] = o
	}
}

// keyOverride implements key-level override for Properties/TOML: every
// top-level key is replaced as a whole and gets the new source.
func keyOverride(dst, src map[string]any, o origin, sources map[string]origin) {
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		dst[k] = src[k]
		sources[k] = o
	}
}

func markLeaves(m map[string]any, o origin, prefix string, sources map[string]origin) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		path := joinPath(prefix, k)
		if sub, ok := m[k].(map[string]any); ok {
			markLeaves(sub, o, path, sources)
		} else {
			sources[path] = o
		}
	}
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func leafAt(root map[string]any, path string) any {
	parts := strings.Split(path, ".")
	var cur any = root
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if sub, ok := v.(map[string]any); ok {
			out[k] = cloneMap(sub)
		} else {
			out[k] = v
		}
	}
	return out
}

func decode(f domain.Format, raw string) (map[string]any, error) {
	return Decode(f, raw)
}

// Decode parses a document of the given format into a generic object. It is
// exported so the reference resolver can substitute placeholders and re-check
// structural validity using the exact same parser as the merge kernel.
//
// Structural placeholders (a "@{...}" token in an unquoted scalar position,
// which would not parse before dereference) are masked into sentinel strings
// first; the resolver turns them back into real values after merging.
func Decode(f domain.Format, raw string) (map[string]any, error) {
	masked, err := refmask.Mask(raw, refmask.MaskKindFor(string(f)))
	if err != nil {
		return nil, err
	}
	switch f {
	case domain.FormatJSON:
		var m map[string]any
		if err := json.Unmarshal([]byte(masked), &m); err != nil {
			return nil, err
		}
		return m, nil
	case domain.FormatYAML:
		var m map[string]any
		if err := yaml.Unmarshal([]byte(masked), &m); err != nil {
			return nil, err
		}
		return m, nil
	case domain.FormatTOML:
		var m map[string]any
		if _, err := toml.Decode(masked, &m); err != nil {
			return nil, err
		}
		return m, nil
	case domain.FormatProperties:
		return decodeProperties(raw), nil
	default:
		return nil, fmt.Errorf("unknown format %q", f)
	}
}

func decodeProperties(raw string) map[string]any {
	out := map[string]any{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
			continue
		}
		for i := 0; i < len(line); i++ {
			switch line[i] {
			case '=', ':':
				out[strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+1:])
				goto next
			}
		}
		out[strings.TrimSpace(line)] = ""
	next:
	}
	return out
}

func encode(f domain.Format, root map[string]any) (string, error) {
	return Encode(f, root)
}

// Encode serializes a generic object in the requested format. It is exported
// for the reference resolver, which renders documents after substitution.
func Encode(f domain.Format, root map[string]any) (string, error) {
	switch f {
	case domain.FormatJSON:
		b, err := json.MarshalIndent(root, "", "  ")
		if err != nil {
			return "", err
		}
		return string(b) + "\n", nil
	case domain.FormatYAML:
		b, err := yaml.Marshal(root)
		if err != nil {
			return "", err
		}
		return string(b), nil
	case domain.FormatTOML:
		var buf bytes.Buffer
		enc := toml.NewEncoder(&buf)
		if err := enc.Encode(root); err != nil {
			return "", err
		}
		return buf.String(), nil
	case domain.FormatProperties:
		return encodeProperties(root), nil
	default:
		return "", fmt.Errorf("unknown format %q", f)
	}
}

func encodeProperties(root map[string]any) string {
	keys := make([]string, 0, len(root))
	for k := range root {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	for _, k := range keys {
		buf.WriteString(k)
		buf.WriteByte('=')
		buf.WriteString(formatScalar(root[k]))
		buf.WriteByte('\n')
	}
	return buf.String()
}

func formatScalar(v any) string {
	return FormatScalar(v)
}

// FormatScalar renders one scalar value in the canonical text used by the
// properties encoder. Exported for embedded placeholder substitution.
func FormatScalar(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case float64:
		// TOML/YAML integers decode to int64; float64 is a genuine decimal.
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}
