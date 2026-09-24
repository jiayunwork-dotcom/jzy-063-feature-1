package merge

import (
	"sort"
	"strconv"
)

// WalkStringLeaves visits every string leaf of a decoded tree, descending
// through maps and arrays. fn receives the dotted/[i] path and the current
// value; if it returns ok=true the leaf is replaced in place with the
// returned string. Traversal of maps is key-sorted for deterministic
// provenance.
func WalkStringLeaves(root any, fn func(path, val string) (repl string, ok bool)) {
	var walk func(v any, path string, parent any, key string, idx int)
	walk = func(v any, path string, parent any, key string, idx int) {
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
				walk(x[k], p, x, k, -1)
			}
		case []any:
			for i := range x {
				walk(x[i], arrayPath(path, i), x, "", i)
			}
		case string:
			if repl, ok := fn(path, x); ok {
				switch p := parent.(type) {
				case map[string]any:
					p[key] = repl
				case []any:
					p[idx] = repl
				}
			}
		}
	}
	walk(root, "", nil, "", -1)
}

// StringLeafAt returns the string value at a path produced by
// WalkStringLeaves and reports whether the leaf exists and is a string.
// Array elements use "[i]" segments.
func StringLeafAt(root any, path string) (string, bool) {
	v := leafAtPath(root, path)
	s, ok := v.(string)
	return s, ok
}

func arrayPath(prefix string, i int) string {
	return prefix + "[" + strconv.Itoa(i) + "]"
}

func leafAtPath(root any, path string) any {
	cur := root
	for _, seg := range splitFieldPath(path) {
		switch node := cur.(type) {
		case map[string]any:
			cur = node[seg.name]
		case []any:
			if seg.index < 0 || seg.index >= len(node) {
				return nil
			}
			cur = node[seg.index]
		default:
			return nil
		}
	}
	return cur
}

type pathSeg struct {
	name  string
	index int
}

func splitFieldPath(path string) []pathSeg {
	var segs []pathSeg
	var name []byte
	flush := func() {
		if len(name) > 0 {
			segs = append(segs, pathSeg{name: string(name), index: -1})
			name = name[:0]
		}
	}
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case '.':
			flush()
		case '[':
			flush()
			j := i + 1
			n := 0
			for j < len(path) && path[j] >= '0' && path[j] <= '9' {
				n = n*10 + int(path[j]-'0')
				j++
			}
			segs = append(segs, pathSeg{index: n})
			i = j // skip past ']' (j points at it; loop's i++ moves over)
		default:
			name = append(name, path[i])
		}
	}
	flush()
	return segs
}

// CloneTree deep-copies a decoded tree so substitution cannot mutate the
// shared merge result.
func CloneTree(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = CloneTree(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = CloneTree(x[i])
		}
		return out
	default:
		return v
	}
}
