package bundle

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The one masking rule against the lines it exists for: every variable part
// a real log carries becomes <*>, and the words around it stay.
func TestShapeMasksWhatVaries(t *testing.T) {
	cases := []struct{ line, want string }{
		{"2026-10-27T12:00:00.123Z INFO [worker-3] request id=0003f2a1 handled path=/api/v1/files/123 status=200 dur=12ms",
			"<*> INFO [<*>] request id=<*> handled path=/api/<*>/files/<*> status=<*> dur=<*>"},
		{"Mar 14 02:11:09 web01 sshd[1234]: Accepted publickey for root from 10.0.0.5 port 51234",
			"Mar <*> <*> <*> sshd[<*>]: Accepted publickey for root from <*> port <*>"},
		{"job 6f1c2a9e-3b7d-4c11-9a2e-0d5f8b1e7c44 failed: connection refused",
			"job <*> failed: connection refused"},
		{"ERROR: user \"alice\" not found", "ERROR: user \"alice\" not found"}, // no digit, kept
		{"überprüfung 42 fehlgeschlagen", "überprüfung <*> fehlgeschlagen"},
		{"round 1: starting, port 22.", "round <*>: starting, port <*>."},
		{"", ""},
	}
	for _, c := range cases {
		if got := Shape(c.line); got != c.want {
			t.Errorf("Shape(%q)\n got %q\nwant %q", c.line, got, c.want)
		}
	}
	// Two lines that differ only in what varies are one shape.
	a := Shape("2026-10-27T12:00:01Z Timeout after 3000ms talking to 10.0.0.5:5432")
	b := Shape("2026-10-28T09:14:59Z Timeout after 120ms talking to 192.168.1.20:6379")
	if a != b {
		t.Errorf("lines differing only in numbers got different shapes:\n%q\n%q", a, b)
	}
}

// A long line's shape is capped, marked, and never split inside a rune: the
// cap is what stops a payload-tailed line from being a shape of its own.
func TestShapeCapsLongLines(t *testing.T) {
	line := strings.Repeat("é", maxShapeBytes) + " tail"
	got := Shape(line)
	if !utf8.ValidString(got) {
		t.Fatalf("the cap split a rune: %q", got)
	}
	if !strings.HasSuffix(got, "…") || len(got) > maxShapeBytes+len("…") {
		t.Errorf("shape is %d bytes, want at most %d and marked as cut", len(got), maxShapeBytes)
	}
	if Shape(line+" more") != got {
		t.Error("lines agreeing past the cap should share a shape")
	}
}

// Counts, files, span and example, and the order a reply lists them in.
func TestGrouperCountsAndSpans(t *testing.T) {
	g := NewGrouper()
	t0 := time.Date(2026, 10, 27, 2, 14, 0, 0, time.UTC)
	g.Add("a.log", 1, "Timeout after 3000ms", t0.Add(time.Minute))
	g.Add("a.log", 7, "Timeout after 12ms", t0)
	g.Add("b.log", 3, "Timeout after 5ms", t0.Add(time.Hour))
	g.AddBytes("b.log", 4, []byte("disk full"), time.Time{})

	all := g.Sorted()
	if len(all) != 2 || g.Lines != 4 {
		t.Fatalf("got %d groups from %d lines, want 2 from 4", len(all), g.Lines)
	}
	top := all[0]
	if top.Shape != "Timeout after <*>" || top.Count != 3 || top.Files != 2 {
		t.Errorf("top group = %q x%d in %d files", top.Shape, top.Count, top.Files)
	}
	if top.Path != "a.log" || top.Line != 1 || top.Text != "Timeout after 3000ms" {
		t.Errorf("example should be the first line seen, got %s:%d %q", top.Path, top.Line, top.Text)
	}
	if !top.First.Equal(t0) || !top.Last.Equal(t0.Add(time.Hour)) {
		t.Errorf("span %s to %s", top.First, top.Last)
	}
	if !all[1].First.IsZero() {
		t.Error("a group with no dated line should have no span")
	}
}

// An example is the group's own copy: a caller reuses its line buffer, and a
// bundle line is a piece of a whole decrypted slice that must not be pinned.
func TestGrouperExampleIsACopy(t *testing.T) {
	g := NewGrouper()
	buf := []byte("disk full on /var")
	g.AddBytes("a.log", 1, buf, time.Time{})
	copy(buf, "XXXX")
	if got := g.Sorted()[0].Text; got != "disk full on /var" {
		t.Errorf("example changed with the caller's buffer: %q", got)
	}
	g = NewGrouper()
	g.Add("a.log", 1, strings.Repeat("y", maxExampleBytes*3), time.Time{})
	if n := len(g.Sorted()[0].Text); n > maxExampleBytes {
		t.Errorf("example kept %d bytes, cap is %d", n, maxExampleBytes)
	}
}

// Past maxGroups a new shape is counted, not grown into the map; a known
// shape still counts normally.
func TestGrouperStopsGrowingAtItsCap(t *testing.T) {
	g := NewGrouper()
	word := func(i int) string { // distinct and digit-free, so each is its own shape
		var b strings.Builder
		for ; ; i /= 26 {
			b.WriteByte(byte('a' + i%26))
			if i < 26 {
				break
			}
		}
		return b.String()
	}
	for i := 0; i < maxGroups+10; i++ {
		g.Add("a.log", i+1, "event "+word(i), time.Time{})
	}
	g.Add("a.log", 1, "event "+word(0), time.Time{})
	if g.Len() != maxGroups || g.Ungrouped != 10 || g.Lines != maxGroups+11 {
		t.Errorf("len %d ungrouped %d lines %d", g.Len(), g.Ungrouped, g.Lines)
	}
	if !strings.Contains(g.Render(400), "counted but not grouped") {
		t.Error("the reply does not say lines went ungrouped")
	}
}

// With more shapes than fit, the reply lists the most frequent AND the rarest,
// since the one odd line is as often the answer as the flood.
func TestGrouperRenderShowsTheRarest(t *testing.T) {
	g := NewGrouper()
	for i := 0; i < GroupsShown+GroupsRarest+5; i++ {
		for n := 0; n <= i; n++ {
			g.Add("a.log", n+1, fmt.Sprintf("message kind%c%c %d", 'a'+i/26, 'a'+i%26, n), time.Time{})
		}
	}
	out := g.Render(400)
	if !strings.Contains(out, "Most frequent first:") || !strings.Contains(out, "5 shape(s) between, not listed. The rarest:") {
		t.Fatalf("missing sections:\n%s", out)
	}
	if !strings.Contains(out, "message kindaa <*>") {
		t.Error("the rarest shape (seen once) is not listed")
	}
}
