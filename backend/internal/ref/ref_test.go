package ref

import (
	"strings"
	"testing"
)

func TestScanBareKey(t *testing.T) {
	toks, err := Scan("jdbc:@{db_host}/app")
	if err != nil {
		t.Fatal(err)
	}
	if len(toks) != 3 {
		t.Fatalf("want 3 tokens, got %d", len(toks))
	}
	if toks[0].Literal != "jdbc:" || toks[2].Literal != "/app" {
		t.Fatalf("literal split wrong: %q %q", toks[0].Literal, toks[2].Literal)
	}
	ph := toks[1]
	if !ph.Placeholder || ph.Target != "db_host" {
		t.Fatalf("placeholder parsed wrong: %+v", ph)
	}
}

func TestScanFullyQualified(t *testing.T) {
	toks, err := Scan("x=@{payment/gateway/host?env=prod};")
	if err != nil {
		t.Fatal(err)
	}
	var ph *Token
	for i := range toks {
		if toks[i].Placeholder {
			ph = &toks[i]
		}
	}
	if ph == nil {
		t.Fatal("missing placeholder")
	}
	if ph.Target != "payment/gateway/host" || ph.Env != "prod" {
		t.Fatalf("target/env wrong: %q env=%q", ph.Target, ph.Env)
	}
	if strings.Count(strings.Join([]string{toks[0].Literal, toks[len(toks)-1].Literal}, ""), ";") != 1 {
		t.Fatal("surrounding text lost")
	}
}

func TestScanMultipleAndRoundTrip(t *testing.T) {
	raw := "@{a}-@{b}/@{c}"
	toks, err := Scan(raw)
	if err != nil {
		t.Fatal(err)
	}
	ph := 0
	var rebuilt strings.Builder
	for _, tk := range toks {
		if tk.Placeholder {
			ph++
			rebuilt.WriteString(tk.Raw)
		} else {
			rebuilt.WriteString(tk.Literal)
		}
	}
	if ph != 3 {
		t.Fatalf("want 3 placeholders, got %d", ph)
	}
	if rebuilt.String() != raw {
		t.Fatalf("round trip mismatch: %q", rebuilt.String())
	}
}

func TestEscapeLiteralDelimiter(t *testing.T) {
	toks, err := Scan("100@@{x} dollars")
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range toks {
		if tk.Placeholder {
			t.Fatalf("escaped delimiter must not parse as placeholder: %+v", tk)
		}
	}
	if got := RenderTokens(toks, nil); got != "100@{x} dollars" {
		t.Fatalf("escape collapsed wrong: %q", got)
	}
}

func TestOrdinaryTextUntouched(t *testing.T) {
	for _, raw := range []string{
		"plain text",
		"{braces} and ${dollar}",
		"$",
		"@ alone",
		"{ @ }",
		"email@example.com",
		"template ${foo} stays",
		"",
	} {
		toks, err := Scan(raw)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", raw, err)
		}
		for _, tk := range toks {
			if tk.Placeholder {
				t.Fatalf("ordinary text %q produced a placeholder %+v", raw, tk)
			}
		}
		if got := RenderTokens(toks, nil); got != raw {
			t.Fatalf("ordinary text altered: %q -> %q", raw, got)
		}
	}
}

func TestMalformedPlaceholdersRejected(t *testing.T) {
	bad := []string{
		"@{unterminated",
		"@{}",
		"@{a/b}",       // partial path
		"@{a/b/c/d}",   // too many segments
		"@{ns//key}",   // empty segment
		"@{ key }",     // whitespace
		"@{k?bogus=x}", // bad query
		"@{k?env=}",    // empty env
	}
	for _, raw := range bad {
		if _, err := Scan(raw); err == nil {
			t.Fatalf("expected syntax error for %q", raw)
		}
	}
}

func TestHasPlaceholderFastPath(t *testing.T) {
	if HasPlaceholder("anything {ordinary}") {
		t.Fatal("ordinary text reported as placeholder")
	}
	if !HasPlaceholder("v=@{x}") {
		t.Fatal("placeholder not detected")
	}
}
