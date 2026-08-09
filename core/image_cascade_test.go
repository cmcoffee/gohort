// Five pictures handed to a three-input backend used to be a refusal. Now it
// is a chain: edit the first three, then blend that result with the remaining
// two. Every stage after the first spends one input slot carrying the running
// result forward, which is what makes the arithmetic max-1 rather than max.
package core

import (
	"reflect"
	"testing"
)

func TestPlanImageCascade(t *testing.T) {
	for _, tc := range []struct {
		name    string
		n, max  int
		want    []int
		wantWhy string
	}{
		{name: "the reported case", n: 5, max: 3, want: []int{3, 2},
			wantWhy: "three, then the result plus the last two"},
		{name: "fits in one call", n: 3, max: 3, want: []int{3}},
		{name: "under the limit", n: 2, max: 3, want: []int{2}},
		{name: "exactly one over", n: 4, max: 3, want: []int{3, 1}},
		{name: "long chain on a 2-input backend", n: 5, max: 2, want: []int{2, 1, 1, 1}},
		{name: "big backend", n: 10, max: 6, want: []int{6, 4}},
		{name: "single-slot backend cannot carry a result", n: 2, max: 1, want: nil,
			wantWhy: "no slot left for the previous stage's output"},
		{name: "single image on a single-slot backend still works", n: 1, max: 1, want: []int{1}},
		{name: "past the stage cap", n: 99, max: 2, want: nil},
		{name: "nothing to do", n: 0, max: 3, want: nil},
	} {
		got := planImageCascade(tc.n, tc.max)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: planImageCascade(%d, %d) = %v, want %v %s",
				tc.name, tc.n, tc.max, got, tc.want, tc.wantWhy)
		}
	}
}

// Every image the caller passed must be consumed exactly once — a plan that
// quietly drops one produces a composite missing a picture, which is the
// failure the refusal message has always been about.
func TestEveryStagePlanConsumesEveryImage(t *testing.T) {
	for max := 2; max <= 6; max++ {
		for n := 1; n <= cascadeCapacity(max); n++ {
			stages := planImageCascade(n, max)
			if stages == nil {
				t.Errorf("max=%d n=%d: no plan, but n is within cascade capacity %d", max, n, cascadeCapacity(max))
				continue
			}
			total := 0
			for i, c := range stages {
				total += c
				// Stage 1 fills every slot; later stages leave one for the
				// carried result.
				limit := max
				if i > 0 {
					limit = max - 1
				}
				if c < 1 || c > limit {
					t.Errorf("max=%d n=%d: stage %d takes %d new image(s), limit %d", max, n, i+1, c, limit)
				}
			}
			if total != n {
				t.Errorf("max=%d n=%d: stages %v consume %d images, want %d", max, n, stages, total, n)
			}
		}
	}
}

// The capacity the preflight and the tool description promise has to be a
// capacity the planner will actually accept — one over must be the refusal.
func TestCascadeCapacityMatchesThePlanner(t *testing.T) {
	for max := 1; max <= 6; max++ {
		cap := cascadeCapacity(max)
		if planImageCascade(cap, max) == nil {
			t.Errorf("max=%d: capacity %d is promised but has no plan", max, cap)
		}
		if planImageCascade(cap+1, max) != nil {
			t.Errorf("max=%d: %d is one past capacity %d and should have no plan", max, cap+1, cap)
		}
	}
}

// A one-slot backend has no cascade, and must not be advertised as having one.
func TestASingleSlotBackendPromisesNoCascade(t *testing.T) {
	if got := cascadeCapacity(1); got != 1 {
		t.Errorf("cascadeCapacity(1) = %d, want 1", got)
	}
}
