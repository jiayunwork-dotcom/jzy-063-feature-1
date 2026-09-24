// Package ref implements configuration reference syntax: placeholders inside
// string values that point at another configuration key's final effective
// value.
//
// A placeholder has the form
//
//	$ref{key}                       bare key — resolved in the current context
//	$ref{namespace/group/key}       cross-namespace, current environment
//	$ref{namespace/group/env/key}   cross-namespace and cross-environment
//
// It may sit anywhere inside a string; a single string may carry several
// placeholders; the whole string may be one placeholder.
//
// The delimiter is intentionally the uncommon "$ref{": ordinary braces and
// currency symbols ("{x}", "${x}", "$100") are never touched. To embed a
// literal delimiter, escape it with a doubled dollar sign:
//
//	$$ref{key}   ->   $ref{key}   (literal text, not a reference)
//
// References never carry a tenant component: dereferencing is tenant-scoped
// by the caller, so cross-tenant references are structurally impossible.
package ref

import (
	"fmt"
	"sort"
	"strings"
)

// Prefix opens a reference placeholder; Escape renders a literal Prefix.
const (
	Prefix = "$ref{"
	Escape = "$$ref{"
	Suffix = "}"
)

// Target is a parsed reference target.
//
// Bare targets carry only a key and are resolved in the caller's context
// (the three layers of the group being computed). Qualified targets pin a
// namespace/group, and optionally an environment (empty Env inherits the
// current one).
type Target struct {
	Namespace string `json:"namespace,omitempty"`
	Group     string `json:"group,omitempty"`
	Env       string `json:"env,omitempty"`
	Key       string `json:"key"`
	Bare      bool `json:"bare,omitempty"`
}

// Qualified reports whether the target pins a namespace/group location.
func (t Target) Qualified() bool { return !t.Bare }

// Spec is the canonical textual form of the target (without delimiters).
func (t Target) Spec() string {
	switch {
	case t.Bare:
		return t.Key
	case t.Env != "":
		return t.Namespace + "/" + t.Group + "/" + t.Env + "/" + t.Key
	default:
		return t.Namespace + "/" + t.Group + "/" + t.Key
	}
}

// String renders the placeholder form, used in error messages.
func (t Target) String() string { return Prefix + t.Spec() + Suffix }

// ParseTarget parses the content between Prefix and Suffix.
//
// Accepted shapes: "key", "ns/group/key", "ns/group/env/key". A two-segment
// shape is rejected so "ns/key" can never be silently misread.
func ParseTarget(spec string) (Target, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Target{}, fmt.Errorf("empty reference target")
	}
	parts := strings.Split(spec, "/")
	for _, p := range parts {
		if p == "" {
			return Target{}, fmt.Errorf("reference target %q has an empty segment", spec)
		}
	}
	switch len(parts) {
	case 1:
		return Target{Key: parts[0], Bare: true}, nil
	case 3:
		return Target{Namespace: parts[0], Group: parts[1], Key: parts[2]}, nil
	case 4:
		return Target{Namespace: parts[0], Group: parts[1], Env: parts[2], Key: parts[3]}, nil
	default:
		return Target{}, fmt.Errorf("reference target %q must be key, ns/group/key or ns/group/env/key", spec)
	}
}

// SegmentKind classifies a scanned piece of text.
type SegmentKind int

const (
	SegLiteral   SegmentKind = iota // plain text
	SegReference                    // a dereferenced placeholder
	SegEscaped                      // an escaped literal delimiter
)

// Segment is one piece of a scanned string.
type Segment struct {
	Kind   SegmentKind
	Text   string // literal text (SegLiteral / SegEscaped, already unescaped)
	Target Target // SegReference only
}

// SyntaxError names a malformed placeholder and why it is malformed.
type SyntaxError struct {
	Raw     string // the offending placeholder text as written
	Offset  int    // byte offset inside the scanned string
	Message string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("malformed reference at byte %d: %s (%q)", e.Offset, e.Message, e.Raw)
}

// HasPlaceholder reports whether s contains an unescaped placeholder. It is a
// cheap first check used to skip all reference work for ordinary configs.
func HasPlaceholder(s string) bool {
	for {
		idx := strings.Index(s, Prefix)
		if idx < 0 {
			return false
		}
		if idx > 0 && s[idx-1] == '$' {
			// escaped; keep looking past it
			s = s[idx+len(Prefix):]
			continue
		}
		return true
	}
}

// Scan splits one raw string value into literal / reference / escaped
// segments. Text without any placeholder yields a single literal segment.
func Scan(raw string) ([]Segment, error) {
	var segs []Segment
	var lit strings.Builder
	i := 0
	flush := func() {
		if lit.Len() > 0 {
			segs = append(segs, Segment{Kind: SegLiteral, Text: lit.String()})
			lit.Reset()
		}
	}
	for i < len(raw) {
		if strings.HasPrefix(raw[i:], Escape) {
			// Escaped delimiter: emit the literal "$ref{" and skip past the
			// extra '$'. Everything up to the next '}' stays verbatim.
			rest := raw[i+len(Escape):]
			end := strings.Index(rest, Suffix)
			if end < 0 {
				lit.WriteString(Prefix)
				lit.WriteString(rest)
				i = len(raw)
				break
			}
			flush()
			segs = append(segs, Segment{Kind: SegEscaped, Text: Prefix + rest[:end+1]})
			i += len(Escape) + end + 1
			continue
		}
		if strings.HasPrefix(raw[i:], Prefix) {
			rest := raw[i+len(Prefix):]
			end := strings.Index(rest, Suffix)
			if end < 0 {
				return nil, &SyntaxError{
					Raw: raw[i:], Offset: i,
					Message: "reference is missing its closing '}'",
				}
			}
			inner := rest[:end]
			target, err := ParseTarget(inner)
			if err != nil {
				return nil, &SyntaxError{
					Raw: raw[i : i+len(Prefix)+end+1], Offset: i, Message: err.Error(),
				}
			}
			flush()
			segs = append(segs, Segment{Kind: SegReference, Target: target})
			i += len(Prefix) + end + 1
			continue
		}
		lit.WriteByte(raw[i])
		i++
	}
	flush()
	return segs, nil
}

// Occurrence records one placeholder inside a document.
type Occurrence struct {
	Path   string // dotted field path of the string leaf containing it
	Index  int    // segment index inside that leaf's scan (0-based)
	Target Target
}

// DocumentError points at a malformed placeholder inside a parsed document.
type DocumentError struct {
	Path string
	Err  *SyntaxError
}

func (e *DocumentError) Error() string {
	return fmt.Sprintf("%s: %s", e.Path, e.Err.Error())
}

// Extract scans every string leaf of a parsed configuration document and
// returns the references it contains in deterministic order (path, then
// segment index). Map keys are not string values and are never scanned, so a
// "$ref{" sitting in a JSON/YAML/TOML key stays ordinary text.
//
// The document is the generic map produced by the merge decoders; arrays are
// descended into (paths use "[i]"). If any placeholder is malformed the
// first error is returned together with the references found before it.
func Extract(root map[string]any) (refs []Occurrence, errs []*DocumentError) {
	walkValue(root, "", func(path string, v any) {
		s, ok := v.(string)
		if !ok || !HasPlaceholder(s) {
			return
		}
		segs, err := Scan(s)
		if err != nil {
			if se, ok := err.(*SyntaxError); ok {
				errs = append(errs, &DocumentError{Path: path, Err: se})
				return
			}
		}
		for i, seg := range segs {
			if seg.Kind == SegReference {
				refs = append(refs, Occurrence{Path: path, Index: i, Target: seg.Target})
			}
		}
	})
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].Path != refs[j].Path {
			return refs[i].Path < refs[j].Path
		}
		return refs[i].Index < refs[j].Index
	})
	return refs, errs
}

// DocumentHasPlaceholder reports whether any string leaf of the document
// contains an unescaped placeholder — the cheap "does this value need the
// reference layer at all" check.
func DocumentHasPlaceholder(root map[string]any) bool {
	found := false
	walkValue(root, "", func(_ string, v any) {
		if !found {
			if s, ok := v.(string); ok && HasPlaceholder(s) {
				found = true
			}
		}
	})
	return found
}

func walkValue(v any, path string, visit func(path string, v any)) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := k
			if path != "" {
				p = path + "." + k
			}
			walkValue(x[k], p, visit)
		}
	case []any:
		for i, item := range x {
			walkValue(item, fmt.Sprintf("%s[%d]", path, i), visit)
		}
	default:
		visit(path, v)
	}
}

