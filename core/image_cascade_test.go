// Five pictures handed to a three-input backend is a chain: edit the first
// three, then blend that result with the remaining two. Every render after the
// first spends one input slot carrying the running result forward.
//
// Two constraints the first cut of this got wrong, both pinned below:
//
// Every render must be EXACTLY full. A compose graph writes one mapped node
// per image and errors on a partial fill, because an unwritten node renders
// against whatever placeholder the workflow was saved with. So a short final
// stage is not a smaller render, it is a failed one — after the earlier
// renders have already been paid for.
//
// And on a two-slot backend the chain is pathological: every render folds in
// exactly one new image, so the first picture is re-encoded once per remaining
// image while the last is untouched. Those backends merge as a TREE instead.
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
		{name: "two-slot backend takes any count", n: 5, max: 2, want: []int{2, 1, 1, 1}},
		{name: "seven images on a three-slot backend", n: 7, max: 3, want: []int{3, 2, 2}},
		{name: "one over leaves a short render", n: 4, max: 3, want: nil,
			wantWhy: "the second pass would carry one result plus one image into three slots"},
		{name: "ten into six is not divisible", n: 10, max: 6, want: nil},
		{name: "single-slot backend cannot carry a result", n: 2, max: 1, want: nil,
			wantWhy: "no slot left for the previous render's output"},
		{name: "single image on a single-slot backend still works", n: 1, max: 1, want: []int{1}},
		{name: "past the render cap", n: 99, max: 2, want: nil},
		{name: "nothing to do", n: 0, max: 3, want: nil},
	} {
		got := planImageCascade(tc.n, tc.max)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: planImageCascade(%d, %d) = %v, want %v %s",
				tc.name, tc.n, tc.max, got, tc.want, tc.wantWhy)
		}
	}
}

// A short render fails outright once it reaches the backend, so a plan that
// produces one is worse than no plan: it burns the earlier renders first.
func TestEveryRenderIsExactlyFull(t *testing.T) {
	for max := 2; max <= 6; max++ {
		for n := 1; n <= cascadeCapacity(max); n++ {
			stages := planImageCascade(n, max)
			if stages == nil {
				continue // refused; TestOnlyDivisibleCountsPlan covers which
			}
			total := 0
			for i, c := range stages {
				total += c
				filled := c
				if i > 0 {
					filled++ // the carried result occupies one slot
				}
				if filled != max && !(len(stages) == 1 && c <= max) {
					t.Errorf("max=%d n=%d: render %d gets %d of %d slots — a partial fill is a failed render",
						max, n, i+1, filled, max)
				}
			}
			if total != n {
				t.Errorf("max=%d n=%d: stages %v consume %d images, want %d", max, n, stages, total, n)
			}
		}
	}
}

// Exactly the counts whose arithmetic works, and no others. The refusal
// message quotes this list, so it has to be the truth.
func TestOnlyDivisibleCountsPlan(t *testing.T) {
	for max := 2; max <= 6; max++ {
		ok := map[int]bool{}
		for _, c := range cascadeImageCounts(max) {
			ok[c] = true
		}
		for n := 1; n <= cascadeCapacity(max); n++ {
			planned := planImageCascade(n, max) != nil
			want := ok[n] || n <= max
			if planned != want {
				t.Errorf("max=%d n=%d: planned=%v, want %v (workable counts %v)",
					max, n, planned, want, cascadeImageCounts(max))
			}
		}
	}
}

func TestCascadeCapacityMatchesThePlanner(t *testing.T) {
	for max := 2; max <= 6; max++ {
		capacity := cascadeCapacity(max)
		if planImageCascade(capacity, max) == nil {
			t.Errorf("max=%d: capacity %d is promised but has no plan", max, capacity)
		}
		for n := capacity + 1; n <= capacity+max; n++ {
			if planImageCascade(n, max) != nil {
				t.Errorf("max=%d: %d is past capacity %d and should have no plan", max, n, capacity)
			}
		}
	}
}

func TestASingleSlotBackendPromisesNoCascade(t *testing.T) {
	if got := cascadeCapacity(1); got != 1 {
		t.Errorf("cascadeCapacity(1) = %d, want 1", got)
	}
}

// --- tree merge ---------------------------------------------------------------

// sourceDepth returns how many renders each source image passes through. This
// is the number the tree exists to flatten: a source re-encoded four times has
// drifted four times.
func sourceDepth(steps []cascadeStep, n int) []int {
	stepDepth := make([]int, len(steps))
	depth := make([]int, n)
	for i, st := range steps {
		worst := 0
		for _, ref := range st.In {
			d := 0
			if ref.Step >= 0 {
				d = stepDepth[ref.Step]
			}
			if d > worst {
				worst = d
			}
		}
		stepDepth[i] = worst + 1
		for _, ref := range st.In {
			if ref.Source >= 0 {
				depth[ref.Source] = stepDepth[i]
			}
		}
	}
	// A source folded in early keeps accumulating depth through its ancestors.
	for i, st := range steps {
		for _, ref := range st.In {
			if ref.Step >= 0 {
				for src := 0; src < n; src++ {
					if reachesStep(steps, ref.Step, src) && stepDepth[i] > depth[src] {
						depth[src] = stepDepth[i]
					}
				}
			}
		}
	}
	return depth
}

func reachesStep(steps []cascadeStep, step, src int) bool {
	for _, ref := range steps[step].In {
		if ref.Source == src {
			return true
		}
		if ref.Step >= 0 && reachesStep(steps, ref.Step, src) {
			return true
		}
	}
	return false
}

func maxOf(xs []int) int {
	m := 0
	for _, x := range xs {
		if x > m {
			m = x
		}
	}
	return m
}

// The whole point: same number of renders, shallower and more even.
func TestTreeIsShallowerThanTheChain(t *testing.T) {
	const n, max = 5, 2
	chain := chainSteps(planImageCascade(n, max))
	tree := planCascadeTree(n, max)

	if len(tree) != len(chain) {
		t.Fatalf("render count should be fixed by the arithmetic: tree %d, chain %d", len(tree), len(chain))
	}
	chainDepth, treeDepth := sourceDepth(chain, n), sourceDepth(tree, n)
	if maxOf(treeDepth) >= maxOf(chainDepth) {
		t.Errorf("tree must reduce the worst-case depth: chain %v (max %d), tree %v (max %d)",
			chainDepth, maxOf(chainDepth), treeDepth, maxOf(treeDepth))
	}
	// The first picture is the one the chain punishes hardest.
	if treeDepth[0] >= chainDepth[0] {
		t.Errorf("the first image should be re-encoded less: chain %d, tree %d", chainDepth[0], treeDepth[0])
	}
}

// Whatever the shape, every source is used exactly once — dropping one
// produces a composite missing a picture, the failure the refusal exists for.
func TestEverySourceIsConsumedExactlyOnce(t *testing.T) {
	for max := 2; max <= 4; max++ {
		for n := 1; n <= cascadeCapacity(max); n++ {
			steps := cascadeSteps(n, max)
			if steps == nil {
				continue
			}
			seen := make([]int, n)
			for _, st := range steps {
				if len(st.In) != max && !(len(steps) == 1 && len(st.In) <= max) {
					t.Errorf("max=%d n=%d: a render takes %d of %d slots", max, n, len(st.In), max)
				}
				for _, ref := range st.In {
					if ref.Source >= 0 {
						seen[ref.Source]++
					}
				}
			}
			for i, c := range seen {
				if c != 1 {
					t.Errorf("max=%d n=%d: source %d used %d times, want once", max, n, i, c)
				}
			}
		}
	}
}

// Only low-slot backends merge as a tree. Above that the chain is already
// shallow, and its left-to-right accumulation is the ordering that was asked
// for.
func TestOnlyLowSlotBackendsUseTheTree(t *testing.T) {
	twoSlot := cascadeSteps(5, 2)
	if reflect.DeepEqual(twoSlot, chainSteps(planImageCascade(5, 2))) {
		t.Error("a two-slot backend should merge as a tree, not a chain")
	}
	threeSlot := cascadeSteps(5, 3)
	if !reflect.DeepEqual(threeSlot, chainSteps(planImageCascade(5, 3))) {
		t.Error("a three-slot backend should keep the in-order chain")
	}
}

// Tree grouping stays in reading order — (1,2) then (3,4) — so it remains an
// ordered combine even though it is no longer an ordered accumulation.
func TestTreeGroupsInOrder(t *testing.T) {
	steps := planCascadeTree(4, 2)
	if len(steps) != 3 {
		t.Fatalf("four images on a two-slot backend is three renders, got %d", len(steps))
	}
	want := [][2]int{{0, 1}, {2, 3}}
	for i, w := range want {
		in := steps[i].In
		if len(in) != 2 || in[0].Source != w[0] || in[1].Source != w[1] {
			t.Errorf("render %d = %+v, want sources %v", i+1, in, w)
		}
	}
	// The last render combines the two results, in order.
	last := steps[2].In
	if len(last) != 2 || last[0].Step != 0 || last[1].Step != 1 {
		t.Errorf("final render = %+v, want the two results in order", last)
	}
}

func TestCascadeImageCounts(t *testing.T) {
	if got, want := cascadeImageCounts(3), []int{3, 5, 7, 9, 11, 13}; !reflect.DeepEqual(got, want) {
		t.Errorf("cascadeImageCounts(3) = %v, want %v", got, want)
	}
	if got, want := cascadeImageCounts(2), []int{2, 3, 4, 5, 6, 7}; !reflect.DeepEqual(got, want) {
		t.Errorf("cascadeImageCounts(2) = %v, want %v", got, want)
	}
}
