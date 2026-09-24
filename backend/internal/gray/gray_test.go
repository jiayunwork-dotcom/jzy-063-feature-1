package gray

import (
	"fmt"
	"testing"

	"configcenter/internal/domain"
)

func TestIPListSelectsOnlyListedInstances(t *testing.T) {
	instances := []Instance{
		{ID: "i-1", IP: "10.0.0.1"},
		{ID: "i-2", IP: "10.0.0.2"},
		{ID: "i-3", IP: "10.0.0.3"},
	}
	sel, err := Select(domain.GrayIPList, 0, []string{"10.0.0.1", "10.0.0.3"}, "rel-1", instances)
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.IDs) != 2 {
		t.Fatalf("expected 2 selected, got %v", sel.IDs)
	}
	got := map[string]bool{}
	for _, id := range sel.IDs {
		got[id] = true
	}
	if !got["i-1"] || !got["i-3"] || got["i-2"] {
		t.Fatalf("ip selection wrong: %v", got)
	}
	// A not-selected instance must never be delivered; a listed one must.
	if ShouldDeliver(domain.GrayIPList, 0, []string{"10.0.0.1"}, "rel-1", instances[1]) {
		t.Fatal("instance outside ip list received gray value")
	}
	if !ShouldDeliver(domain.GrayIPList, 0, []string{"10.0.0.2"}, "rel-1", instances[1]) {
		t.Fatal("listed instance denied gray value")
	}
}

func TestPercentageIsDeterministic(t *testing.T) {
	in := Instance{ID: "stable-instance", IP: "1.1.1.1"}
	first := make([]bool, 0, 5)
	for i := 0; i < 5; i++ {
		first = append(first, ShouldDeliver(domain.GrayPercent, 30, nil, "rel-fixed", in))
	}
	for i := 1; i < len(first); i++ {
		if first[i] != first[0] {
			t.Fatal("percentage selection must be stable across calls for the same release")
		}
	}
}

func TestPercentageBounds(t *testing.T) {
	instances := make([]Instance, 1000)
	for i := range instances {
		instances[i] = Instance{ID: fmt.Sprintf("inst-%d", i), IP: fmt.Sprintf("10.1.%d.%d", i/256, i%256)}
	}
	zero, _ := Select(domain.GrayPercent, 0, nil, "r0", instances)
	if len(zero.IDs) != 0 {
		t.Fatalf("percent=0 must select nobody, got %d", len(zero.IDs))
	}
	hundred, _ := Select(domain.GrayPercent, 100, nil, "r100", instances)
	if len(hundred.IDs) != 1000 {
		t.Fatalf("percent=100 must select everybody, got %d", len(hundred.IDs))
	}
	if _, err := Select(domain.GrayPercent, 101, nil, "rx", instances); err == nil {
		t.Fatal("percent>100 must be rejected")
	}
}

// The selected share over a large pool must land within the expected band
// around the requested percentage.
func TestPercentageRatioWithinBand(t *testing.T) {
	const pool = 2000
	instances := make([]Instance, pool)
	for i := range instances {
		instances[i] = Instance{ID: fmt.Sprintf("node-%05d", i)}
	}
	for _, p := range []int{10, 25, 50, 75} {
		sel, err := Select(domain.GrayPercent, p, nil, fmt.Sprintf("rel-pct-%d", p), instances)
		if err != nil {
			t.Fatal(err)
		}
		ratio := float64(len(sel.IDs)) / float64(pool)
		// allow a generous 4-point band so the test is not flaky while still
		// pinning the distribution to the requested percentage.
		lo := float64(p)/100 - 0.04
		hi := float64(p)/100 + 0.04
		if ratio < lo || ratio > hi {
			t.Fatalf("percent=%d selected ratio %.3f outside [%.3f,%.3f]", p, ratio, lo, hi)
		}
	}
}

// Changing the release id rebalances the set; the same instance can move in
// or out, but stability holds per release.
func TestPercentageDiffersAcrossReleases(t *testing.T) {
	in := Instance{ID: "in-x"}
	delivered := map[bool]int{}
	for r := 0; r < 100; r++ {
		delivered[ShouldDeliver(domain.GrayPercent, 50, nil, fmt.Sprintf("rel-%d", r), in)]++
	}
	if delivered[true] == 0 || delivered[false] == 0 {
		t.Fatalf("hashing should spread one instance across releases: %+v", delivered)
	}
}
