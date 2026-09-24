// Package version provides line-level diffing between historical
// configuration versions.
package version

import (
	"strings"

	"configcenter/internal/domain"
)

// Diff returns a line-level diff of two raw configuration texts.
//
// The classic LCS table identifies unchanged lines; a delete immediately
// followed by an add on adjacent lines is collapsed into a change so callers
// can distinguish modification from pure insertion/deletion.
func Diff(oldText, newText string) []domain.DiffLine {
	left := splitLines(oldText)
	right := splitLines(newText)

	n, m := len(left), len(right)
	// lcs[i][j] = length of LCS of left[i:] and right[j:]
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if left[i] == right[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	type rawOp struct {
		op    domain.DiffOp
		left  string
		right string
	}
	var raws []rawOp
	i, j := 0, 0
	for i < n && j < m {
		if left[i] == right[j] {
			raws = append(raws, rawOp{domain.DiffEqual, left[i], right[j]})
			i++
			j++
		} else if lcs[i+1][j] >= lcs[i][j+1] {
			raws = append(raws, rawOp{domain.DiffDelete, left[i], ""})
			i++
		} else {
			raws = append(raws, rawOp{domain.DiffAdd, "", right[j]})
			j++
		}
	}
	for ; i < n; i++ {
		raws = append(raws, rawOp{domain.DiffDelete, left[i], ""})
	}
	for ; j < m; j++ {
		raws = append(raws, rawOp{domain.DiffAdd, "", right[j]})
	}

	// Collapse adjacent delete/add blocks of equal length into changes by
	// pairing them row for row; unequal tails stay add/delete.
	out := make([]domain.DiffLine, 0, len(raws))
	for k := 0; k < len(raws); {
		if raws[k].op == domain.DiffDelete {
			// gather delete run
			dStart := k
			for k < len(raws) && raws[k].op == domain.DiffDelete {
				k++
			}
			dEnd := k
			// gather following add run
			aStart := k
			for k < len(raws) && raws[k].op == domain.DiffAdd {
				k++
			}
			aEnd := k
			dCount, aCount := dEnd-dStart, aEnd-aStart
			pairs := dCount
			if aCount < pairs {
				pairs = aCount
			}
			for p := 0; p < pairs; p++ {
				out = append(out, domain.DiffLine{
					Op:    DiffChange,
					Left:  raws[dStart+p].left,
					Right: raws[aStart+p].right,
				})
			}
			for p := pairs; p < dCount; p++ {
				out = append(out, domain.DiffLine{Op: domain.DiffDelete, Left: raws[dStart+p].left})
			}
			for p := pairs; p < aCount; p++ {
				out = append(out, domain.DiffLine{Op: domain.DiffAdd, Right: raws[aStart+p].right})
			}
			continue
		}
		out = append(out, domain.DiffLine{Op: raws[k].op, Left: raws[k].left, Right: raws[k].right})
		k++
	}
	return out
}

// DiffChange marks a modified line (present in both versions but different).
const DiffChange domain.DiffOp = "change"

// HasChanges reports whether the diff contains anything besides equal rows.
func HasChanges(rows []domain.DiffLine) bool {
	for _, r := range rows {
		if r.Op != domain.DiffEqual {
			return true
		}
	}
	return false
}

// Summary counts adds/deletes/changes.
type Summary struct {
	Add    int `json:"add"`
	Delete int `json:"delete"`
	Change int `json:"change"`
	Equal  int `json:"equal"`
}

func Summarize(rows []domain.DiffLine) Summary {
	var s Summary
	for _, r := range rows {
		switch r.Op {
		case domain.DiffAdd:
			s.Add++
		case domain.DiffDelete:
			s.Delete++
		case DiffChange:
			s.Change++
		default:
			s.Equal++
		}
	}
	return s
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
