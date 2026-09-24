// Package ref implements the reference (placeholder) syntax of the platform.
//
// A placeholder is written as @{...} inside any configuration value:
//
//	@{key}                    - same resolution context (group context where
//	                            the merged key is served)
//	@{ns/group/key}           - a key in another namespace/group
//	@{key?env=prod}           - same context, value of another environment
//	@{ns/group/key?env=prod}  - both
//
// Plain braces, "@" characters and currency symbols never trigger parsing;
// a literal "@{" is written "@@{". Malformed "@{" sequences (unterminated or
// with a body that is not a valid target spec) are hard syntax errors, so a
// typo can never silently survive into an effective value.
//
// Resolution semantics themselves live elsewhere (the service resolver);
// this package is a pure, fully unit-testable lexer/parser.
package ref

import (
	"fmt"
	"strings"
)

// SyntaxError describes one malformed placeholder in a raw value.
type SyntaxError struct {
	Pos     int    // byte offset of the offending '@'
	Raw     string // the offending opening token, or the raw text near it
	Message string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("reference syntax error at %d: %s (near %q)", e.Pos, e.Message, e.Raw)
}

// Token is one piece of a raw value: literal text or a placeholder.
type Token struct {
	Literal     string // present when Placeholder is false
	Raw         string // placeholder text as authored (with the @{...} delimiters)
	Target      string // target spec body (key or ns/group/key, without ?env=)
	Env         string // explicit env, empty when inheriting
	Start       int    // start byte offset in the source
	Placeholder bool
}

const (
	open  = "@{"
	esc   = "@@{"
	close = "}"
)

// Scan splits a raw configuration value into text/placeholder tokens. The
// token stream always round-trips: concatenating Token.Literal/Raw yields the
// original text.
func Scan(raw string) ([]Token, error) {
	var toks []Token
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			toks = append(toks, Token{Literal: lit.String(), Placeholder: false})
			lit.Reset()
		}
	}
	i := 0
	for i < len(raw) {
		switch {
		case strings.HasPrefix(raw[i:], esc):
			// Escaped delimiter: collapse to a literal "@{".
			lit.WriteString(open)
			i += len(esc)
		case strings.HasPrefix(raw[i:], open):
			end := strings.IndexByte(raw[i+len(open):], '}')
			if end < 0 {
				return nil, &SyntaxError{Pos: i, Raw: open, Message: `unterminated placeholder, missing "}"`}
			}
			end += i + len(open)
			body := raw[i+len(open) : end]
			target, env, err := parseBody(body)
			if err != nil {
				return nil, &SyntaxError{Pos: i, Raw: raw[i : end+1], Message: err.Error()}
			}
			flush()
			toks = append(toks, Token{
				Raw: raw[i : end+1], Target: target, Env: env,
				Start: i, Placeholder: true,
			})
			i = end + 1
		default:
			lit.WriteByte(raw[i])
			i++
		}
	}
	flush()
	return toks, nil
}

// HasPlaceholder reports whether the value contains at least one placeholder.
// It returns false on syntax errors: callers that need the distinction use
// Scan directly.
func HasPlaceholder(raw string) bool {
	toks, err := Scan(raw)
	if err != nil {
		return false
	}
	for _, t := range toks {
		if t.Placeholder {
			return true
		}
	}
	return false
}

// parseBody accepts:
//
//	key
//	ns/group/key
//
// optionally followed by "?env=<env>". A bare "@{}" is illegal, group ids may
// not be partially specified (either key alone or the full ns/group/key), and
// whitespace is never trimmed inside the braces so "@{ key }" is rejected
// rather than silently accepted.
func parseBody(body string) (target string, env string, err error) {
	if body == "" {
		return "", "", fmt.Errorf("empty placeholder target")
	}
	if q := strings.IndexByte(body, '?'); q >= 0 {
		target = body[:q]
		qv := body[q+1:]
		const prefix = "env="
		if !strings.HasPrefix(qv, prefix) || qv[len(prefix):] == "" ||
			strings.ContainsAny(qv[len(prefix):], " \t&") {
			return "", "", fmt.Errorf("invalid placeholder query %q (want env=<env>)", qv)
		}
		env = qv[len(prefix):]
	} else {
		target = body
	}
	if target == "" {
		return "", "", fmt.Errorf("empty placeholder target")
	}
	parts := strings.Split(target, "/")
	switch len(parts) {
	case 1:
		if !validCoord(parts[0]) {
			return "", "", fmt.Errorf("invalid key in placeholder: %q", parts[0])
		}
	case 3:
		for i, p := range parts {
			if !validCoord(p) {
				name := []string{"namespace", "group", "key"}[i]
				return "", "", fmt.Errorf("invalid %s in placeholder: %q", name, p)
			}
		}
	default:
		return "", "", fmt.Errorf("invalid target %q: want key or namespace/group/key", target)
	}
	if env != "" && !validCoord(env) {
		return "", "", fmt.Errorf("invalid env in placeholder: %q", env)
	}
	return target, env, nil
}

// validCoord accepts the identifier alphabet used by keys, coordinates and
// environment labels; delimiters, whitespace and "@" never qualify, so a
// typo like "@{a b}" is rejected rather than silently treated as text.
func validCoord(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

// RenderTokens rebuilds a value from tokens, substituting placeholders with
// the provided resolved values (indexed by token order). It is used both for
// normal resolution and for unit tests.
func RenderTokens(toks []Token, resolved map[int]string) string {
	var b strings.Builder
	ph := 0
	for _, t := range toks {
		if !t.Placeholder {
			b.WriteString(t.Literal)
			continue
		}
		b.WriteString(resolved[ph])
		ph++
	}
	return b.String()
}

// Placeholders returns only the placeholder tokens, preserving order.
func Placeholders(toks []Token) []Token {
	out := make([]Token, 0)
	for _, t := range toks {
		if t.Placeholder {
			out = append(out, t)
		}
	}
	return out
}
