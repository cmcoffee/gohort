package guides

import (
	"strings"
	"testing"
)

func secs(titles ...string) []Section {
	out := make([]Section, len(titles))
	for i, t := range titles {
		out[i] = Section{ID: t, Title: t, Order: i + 1}
	}
	return out
}

func order(s []Section) string {
	t := make([]string, len(s))
	for i := range s {
		t[i] = s[i].Title
	}
	return strings.Join(t, ",")
}

func TestReorderSections(t *testing.T) {
	cases := []struct {
		name      string
		in        []Section
		idx       int
		target    int
		want      string
		wantOrder []int
	}{
		{"first to last", secs("A", "B", "C", "D"), 0, 3, "B,C,D,A", []int{1, 2, 3, 4}},
		{"last to first", secs("A", "B", "C", "D"), 3, 0, "D,A,B,C", nil},
		{"middle forward", secs("A", "B", "C", "D"), 1, 2, "A,C,B,D", nil},
		{"middle back", secs("A", "B", "C", "D"), 2, 0, "C,A,B,D", nil},
		{"target past end clamps", secs("A", "B", "C"), 0, 99, "B,C,A", nil},
		{"target negative clamps", secs("A", "B", "C"), 2, -5, "C,A,B", nil},
	}
	for _, c := range cases {
		got, _ := reorderSections(c.in, c.idx, c.target)
		if order(got) != c.want {
			t.Errorf("%s: got %q, want %q", c.name, order(got), c.want)
		}
		// Order values are always 1..N in display order.
		for i := range got {
			if got[i].Order != i+1 {
				t.Errorf("%s: Order[%d] = %d, want %d", c.name, i, got[i].Order, i+1)
			}
		}
	}
}

func TestNormalizeOrder(t *testing.T) {
	g := Guide{Sections: []Section{{Title: "A", Order: 5}, {Title: "B", Order: 1}, {Title: "C", Order: 9}}}
	normalizeOrder(&g)
	// Sorted by original Order: B(1), A(5), C(9) → 1,2,3
	if order(g.Sections) != "B,A,C" {
		t.Errorf("got %q, want B,A,C", order(g.Sections))
	}
	for i := range g.Sections {
		if g.Sections[i].Order != i+1 {
			t.Errorf("Order[%d] = %d, want %d", i, g.Sections[i].Order, i+1)
		}
	}
}

func TestCoerceIntArg(t *testing.T) {
	cases := map[any]int{float64(3): 3, 5: 5, int64(7): 7, "2": 2, "x": 0, nil: 0}
	for in, want := range cases {
		if got := coerceIntArg(in); got != want {
			t.Errorf("coerceIntArg(%v) = %d, want %d", in, got, want)
		}
	}
}
