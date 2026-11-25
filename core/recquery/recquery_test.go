package recquery

import (
	"strings"
	"testing"
)

func rows() []map[string]any {
	return []map[string]any{
		{"id": "a", "kind": "entry", "status": "open", "day": "2026-11-09", "n": float64(3), "title": "Fix the fence", "tags": []any{"home"}},
		{"id": "b", "kind": "entry", "status": "done", "day": "2026-11-10", "n": float64(10), "title": "Call the vet"},
		{"id": "c", "kind": "note", "status": "open", "day": "2026-11-11", "n": float64(9), "title": "Fence posts are cedar", "tags": []any{"home", "wood"}},
		{"id": "d", "kind": "entry", "status": "open", "day": "2026-11-12", "title": "Buy paint"},
		{"id": "e", "kind": "entry", "status": "open", "day": "2026-11-13", "n": float64(1), "title": "Sand the deck"},
	}
}

func ids(rs []map[string]any) string {
	var out []string
	for _, r := range rs {
		out = append(out, text(r["id"]))
	}
	return strings.Join(out, ",")
}

func run(t *testing.T, raw string) (string, string) {
	t.Helper()
	q, err := Parse(raw)
	if err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	page, next, err := Run(rows(), "id", q)
	if err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return ids(page), next
}

// Each comparison does what it says, over text, numbers, lists and missing
// fields; all conditions must hold.
func TestAQueryKeepsTheRecordsThatMeetItsConditions(t *testing.T) {
	cases := map[string]string{
		``:                           "a,b,c,d,e",
		`{"where":{"kind":"entry"}}`: "a,b,d,e",
		`{"where":{"kind":"entry","status":"open"}}`: "a,d,e",
		`{"where":{"status":{"!=":"open"}}}`:         "b",
		`{"where":{"day":{">=":"2026-11-11"}}}`:      "c,d,e",
		`{"where":{"day":{"<":"2026-11-10"}}}`:       "a",
		`{"where":{"n":{">":3}}}`:                    "b,c", // numbers by value: 10 > 3, and d has no n
		`{"where":{"n":{"<=":3}}}`:                   "a,e",
		`{"where":{"n":3}}`:                          "a",
		`{"where":{"n":{"!=":3}}}`:                   "b,c,d,e", // missing counts as not equal
		`{"where":{"status":{"in":["done","x"]}}}`:   "b",
		`{"where":{"title":{"contains":"FENCE"}}}`:   "a,c", // text, any case
		`{"where":{"tags":{"contains":"wood"}}}`:     "c",   // a list, by element
		`{"where":{"missing":"x"}}`:                  "",
	}
	for raw, want := range cases {
		if got, _ := run(t, raw); got != want {
			t.Errorf("%s: got %q, want %q", raw, got, want)
		}
	}
}

// Order is one field either way, numbers by value, records without the
// field last; ties keep the store's order.
func TestAQueryOrdersByOneField(t *testing.T) {
	for raw, want := range map[string]string{
		`{"order":"-day"}`: "e,d,c,b,a",
		`{"order":"n"}`:    "e,a,c,b,d",
		`{"order":"-n"}`:   "b,c,a,e,d",
		`{"where":{"kind":"entry"},"order":"status"}`: "b,a,d,e",
	} {
		if got, _ := run(t, raw); got != want {
			t.Errorf("%s: got %q, want %q", raw, got, want)
		}
	}
}

// A limit pages through, each page saying where the next starts; the last
// says nothing; a lost place is an error, not a skip.
func TestAQueryPagesByTheLastKey(t *testing.T) {
	page, next := run(t, `{"order":"-day","limit":2}`)
	if page != "e,d" || next != "d" {
		t.Fatalf("first page %q next %q", page, next)
	}
	page, next = run(t, `{"order":"-day","limit":2,"after":"d"}`)
	if page != "c,b" || next != "b" {
		t.Fatalf("second page %q next %q", page, next)
	}
	page, next = run(t, `{"order":"-day","limit":2,"after":"b"}`)
	if page != "a" || next != "" {
		t.Fatalf("last page %q next %q", page, next)
	}
	q, _ := Parse(`{"limit":2,"after":"gone"}`)
	if _, _, err := Run(rows(), "id", q); err == nil || !strings.Contains(err.Error(), "from the start") {
		t.Errorf("a lost place: %v", err)
	}
}

// A malformed query says what to fix.
func TestAMalformedQuerySaysWhatToFix(t *testing.T) {
	for raw, want := range map[string]string{
		`{"limit":501}`:                 "limit is 1 to 500",
		`{"limit":-1}`:                  "limit is 1 to 500",
		`{"where":{"n":{"~":3}}}`:       "not a comparison",
		`{"where":{"n":{">":1,"<":5}}}`: "one comparison",
		`{"where":{"s":{"in":"x"}}}`:    "in takes a list",
		`{"order":"-"}`:                 "order names one field",
		`[1,2]`:                         "a JSON object",
	} {
		if _, err := Parse(raw); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want it to say %q", raw, err, want)
		}
	}
	if n, err := Count(rows(), map[string]any{"status": "open"}); err != nil || n != 4 {
		t.Errorf("count: %d %v", n, err)
	}
}
