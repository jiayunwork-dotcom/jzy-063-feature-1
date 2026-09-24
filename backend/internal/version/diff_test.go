package version

import (
	"testing"

	"configcenter/internal/domain"
)

func TestDiffDetectsAddDeleteChange(t *testing.T) {
	old := "a=1\nb=2\nc=3\n"
	new := "a=1\nb=20\nc=3\nd=4\n"
	// remove 'a' case too via second document to exercise pure delete
	old2 := "keep=1\nremove=2\n"
	new2 := "keep=1\n"

	rows := Diff(old, new)
	var adds, deletes, changes int
	for _, r := range rows {
		switch r.Op {
		case domain.DiffAdd:
			adds++
			if r.Right != "d=4" {
				t.Fatalf("unexpected added line %q", r.Right)
			}
		case DiffChange:
			changes++
			if r.Left != "b=2" || r.Right != "b=20" {
				t.Fatalf("change paired wrong: %q -> %q", r.Left, r.Right)
			}
		case domain.DiffDelete:
			deletes++
		}
	}
	if adds != 1 || changes != 1 {
		t.Fatalf("expected 1 add 1 change, got add=%d change=%d delete=%d", adds, changes, deletes)
	}

	rows2 := Diff(old2, new2)
	var del int
	for _, r := range rows2 {
		if r.Op == domain.DiffDelete && r.Left == "remove=2" {
			del++
		}
	}
	if del != 1 {
		t.Fatalf("expected one pure delete, got %+v", rows2)
	}
}

func TestDiffNoChanges(t *testing.T) {
	rows := Diff("x\n y\nz\n", "x\n y\nz\n")
	if HasChanges(rows) {
		t.Fatalf("identical documents reported changes: %+v", rows)
	}
	if Summarize(rows).Equal != 3 {
		t.Fatalf("expected 3 equal rows, got %+v", Summarize(rows))
	}
}

func TestDiffEmpty(t *testing.T) {
	rows := Diff("", "a=1\n")
	if len(rows) != 1 || rows[0].Op != domain.DiffAdd {
		t.Fatalf("expected single add, got %+v", rows)
	}
	rows = Diff("a=1\n", "")
	if len(rows) != 1 || rows[0].Op != domain.DiffDelete {
		t.Fatalf("expected single delete, got %+v", rows)
	}
}

func TestDiffSummaryCounts(t *testing.T) {
	rows := Diff("a\nb\nc\n", "a\nB\nc\nd\n")
	s := Summarize(rows)
	if s.Add != 1 || s.Change != 1 || s.Equal != 2 {
		t.Fatalf("unexpected summary %+v", s)
	}
}
