package ref

import (
	"encoding/hex"
	"strings"
)

// PUA sentinels frame a masked whole-value placeholder. Private Use Area
// code points are valid inside JSON/YAML/TOML double-quoted strings and do
// not occur in real configuration text.
const (
	markerStart = "" // U+E800
	markerEnd   = "" // U+E801
)

// MaskKind selects quote-tracking rules for masking.
type MaskKind int

const (
	MaskNone MaskKind = iota // properties / unknown: text is never parsed
	MaskJSON                 // JSON: " with backslash escapes
	MaskYAML                 // YAML: " and ' (with '' doubling), triple strings
	MaskTOML                 // TOML: " and ' with escapes, triple strings
)

// MaskKindFor maps a format name ("json", ...) to its masking rules.
func MaskKindFor(format string) MaskKind {
	switch format {
	case "json":
		return MaskJSON
	case "yaml":
		return MaskYAML
	case "toml":
		return MaskTOML
	default:
		return MaskNone
	}
}

// HasMarker reports whether a rendered document still carries a sentinel.
func HasMarker(s string) bool { return strings.Contains(s, markerStart) }

// quoteState tracks whether a byte position of the raw document lies inside a
// quoted string. It is format-aware (escapes, YAML ” doubling, triple
// quotes) but deliberately simple: block scalars and comments with quotes are
// out of scope, and masking only needs an exact answer for "@{" positions.
type quoteState struct {
	kind     MaskKind
	inString bool
	quote    byte
	triple   bool
}

func (qs *quoteState) feed(lit string, upto int) {
	for i := 0; i < upto; i++ {
		ch := lit[i]
		if !qs.inString {
			if qs.kind != MaskJSON && strings.HasPrefix(lit[i:], `"""`) {
				qs.inString, qs.triple, qs.quote = true, true, '"'
				i += 2
				continue
			}
			if qs.kind != MaskJSON && strings.HasPrefix(lit[i:], `'''`) {
				qs.inString, qs.triple, qs.quote = true, true, '\''
				i += 2
				continue
			}
			switch ch {
			case '"':
				if qs.kind == MaskJSON || qs.kind == MaskYAML || qs.kind == MaskTOML {
					qs.inString, qs.quote = true, '"'
				}
			case '\'':
				if qs.kind == MaskYAML || qs.kind == MaskTOML {
					qs.inString, qs.quote = true, '\''
				}
			}
			continue
		}
		// inside a string
		switch qs.quote {
		case '"':
			if ch == '\\' && (qs.kind == MaskJSON || qs.kind == MaskTOML) && i+1 < len(lit) {
				i++ // skip escaped char
				continue
			}
			if ch == '\n' && !qs.triple {
				qs.inString = false // unterminated at line end
				continue
			}
			if qs.triple {
				if strings.HasPrefix(lit[i:], `"""`) {
					i += 2
					qs.inString, qs.triple = false, false
				}
			} else if ch == '"' {
				qs.inString = false
			}
		case '\'':
			if qs.triple {
				if strings.HasPrefix(lit[i:], `'''`) {
					i += 2
					qs.inString, qs.triple = false, false
				}
			} else if qs.kind == MaskYAML && ch == '\'' && i+1 < len(lit) && lit[i+1] == '\'' {
				i++ // doubled quote: still inside
			} else {
				qs.inString = false // matching single quote closes
			}
		}
	}
}

// at reports whether the raw byte at offset pos (a "@{" placeholder start) is
// inside a quoted string. It is unused by Mask (which feeds incrementally) but
// documents the semantics.
func (qs *quoteState) at(lit string, pos int) bool {
	saved := *qs
	defer func() { *qs = saved }()
	qs.feed(lit, pos)
	return qs.inString
}

// Mask rewrites a raw structured document so it parses in its own format even
// when a placeholder occupies a structural (unquoted) scalar position:
//
//   - placeholders inside quoted strings stay untouched (they resolve as
//     embedded text later);
//   - placeholders outside strings become a double-quoted sentinel carrying
//     the original token bytes;
//   - the escaped delimiter "@@{" collapses to a literal "@{".
//
// MaskNone is a pass-through: text formats (properties) are never parsed as
// structures, so masking (and escape collapsing) happens later, in the single
// tokenization pass of the resolver.
func Mask(raw string, kind MaskKind) (string, error) {
	if kind == MaskNone {
		return raw, nil
	}
	// Old, reference-free documents bypass scanning entirely.
	if !strings.Contains(raw, "@") {
		return raw, nil
	}
	toks, err := Scan(raw)
	if err != nil {
		return "", err
	}
	qs := &quoteState{kind: kind}
	pos := 0
	var b strings.Builder
	b.Grow(len(raw) + 16)
	for _, t := range toks {
		if t.Placeholder {
			// quote state reflects everything strictly before this token.
			if qs.inString {
				b.WriteString(t.Raw)
			} else {
				b.WriteString(maskToken(t.Raw))
			}
			// tokens contain no quotes relevant to state tracking.
			pos += len(t.Raw)
			continue
		}
		qs.feed(t.Literal, len(t.Literal))
		// collapse escaped delimiters emitted by Scan as literal "@{"
		b.WriteString(t.Literal)
		pos += len(t.Literal)
	}
	_ = pos
	return b.String(), nil
}

func maskToken(rawToken string) string {
	return `"` + markerStart + hex.EncodeToString([]byte(rawToken)) + markerEnd + `"`
}

// UnmaskMarker decodes a masked whole-value sentinel back to its original
// placeholder text. It returns "" when s is not a marker.
func UnmaskMarker(s string) string {
	if !strings.HasPrefix(s, markerStart) || !strings.HasSuffix(s, markerEnd) {
		return ""
	}
	h := s[len(markerStart) : len(s)-len(markerEnd)]
	raw, err := hex.DecodeString(h)
	if err != nil {
		return ""
	}
	return string(raw)
}
