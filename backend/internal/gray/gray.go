// Package gray implements canary (gray-release) instance selection.
//
// Two strategies are supported:
//
//   - IP list:   deliver only to a fixed set of instance IPs;
//   - percentage: deliver to a deterministic subset selected by hashing the
//     release id and instance id into the [0,100) space. Hashing instead of
//     rand() makes the chosen set stable for the lifetime of the release and
//     makes the percentage independently testable.
package gray

import (
	"fmt"
	"hash/fnv"
	"sort"

	"configcenter/internal/domain"
)

// HashBucket maps (releaseID, instanceID) to a stable bucket in [0,99].
// Instances whose bucket is strictly smaller than the release percentage are
// selected, so percentage 0 selects nobody and 100 selects everybody.
func HashBucket(releaseID, instanceID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(releaseID))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write([]byte(instanceID))
	return int(h.Sum32() % 100)
}

// Selection is the resolved target set of a gray release.
type Selection struct {
	IDs     []string // stable, sorted unique instance ids
	ByIP    map[string]struct{}
	Percent int
}

func (s Selection) Contains(id string) bool {
	for _, x := range s.IDs {
		if x == id {
			return true
		}
	}
	return false
}

// Select resolves which of the given instances receive a release.
//
// instanceKey returns the (id, ip) pair used for identity and IP matching;
// pass a nil slice to express "no connected instances yet" - percentage
// selection over an empty pool is empty, IP matching still works later.
func Select(strategy domain.GrayStrategy, percent int, ipList []string, releaseID string, instances []Instance) (Selection, error) {
	sel := Selection{ByIP: map[string]struct{}{}}
	switch strategy {
	case domain.GrayIPList:
		allowed := make(map[string]struct{}, len(ipList))
		for _, ip := range ipList {
			allowed[ip] = struct{}{}
			sel.ByIP[ip] = struct{}{}
		}
		ids := map[string]struct{}{}
		for _, in := range instances {
			if _, ok := allowed[in.IP]; ok {
				ids[in.ID] = struct{}{}
			}
		}
		sel.IDs = sortedKeys(ids)
		return sel, nil
	case domain.GrayPercent:
		if percent < 0 || percent > 100 {
			return sel, fmt.Errorf("gray: percent must be in 0..100, got %d", percent)
		}
		sel.Percent = percent
		ids := map[string]struct{}{}
		for _, in := range instances {
			if HashBucket(releaseID, in.ID) < percent {
				ids[in.ID] = struct{}{}
			}
		}
		sel.IDs = sortedKeys(ids)
		return sel, nil
	default:
		return sel, fmt.Errorf("gray: unknown strategy %q", strategy)
	}
}

// ShouldDeliver answers whether a specific instance is entitled to the new
// version of an ongoing gray release. It works even for instances that
// connected after the release started, keeping the target set stable.
func ShouldDeliver(strategy domain.GrayStrategy, percent int, ipList []string, releaseID string, in Instance) bool {
	switch strategy {
	case domain.GrayIPList:
		for _, ip := range ipList {
			if ip == in.IP {
				return true
			}
		}
		return false
	case domain.GrayPercent:
		return HashBucket(releaseID, in.ID) < percent
	default:
		return false
	}
}

// Instance identifies one connected client.
type Instance struct {
	ID string // stable client instance id (defaults to remote addr)
	IP string // remote IP
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
