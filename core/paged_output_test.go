package core

import (
	"strings"
	"testing"
	"time"
)

// A page ends at a paragraph break when one is available past the midpoint,
// the next offset resumes exactly there, and paging to the end walks the
// whole text without skipping or repeating a byte.
func TestWindowTextPagesTheWholeText(t *testing.T) {
	var paras []string
	for i := 0; i < 20; i++ {
		paras = append(paras, strings.Repeat("p"+string(rune('a'+i))+" ", 30))
	}
	text := strings.Join(paras, "\n\n")
	var got strings.Builder
	offset := 0
	pages := 0
	for {
		w, end := WindowText(text, offset, 400)
		if end <= offset && offset < len(text) {
			t.Fatalf("no progress at offset %d", offset)
		}
		got.WriteString(w)
		pages++
		if end >= len(text) {
			break
		}
		if !strings.HasSuffix(w, " ") && !strings.HasSuffix(text[:end], "\n\n") && !strings.HasSuffix(text[:end], "\n") {
			t.Fatalf("page %d did not end at a boundary: %q", pages, w[len(w)-12:])
		}
		offset = end
	}
	if got.String() != text {
		t.Fatalf("pages do not reassemble the text (%d pages)", pages)
	}
	if pages < 4 {
		t.Fatalf("expected several pages, got %d", pages)
	}
}

// Offsets the agent passes are made safe: negative, past the end, or inside
// a multi-byte character; and a cut never splits one.
func TestWindowTextIsSafeOnAnyOffset(t *testing.T) {
	text := "não é assim — mas é"
	if w, end := WindowText(text, -5, 4); w != "não" || end != len("não") {
		t.Fatalf("negative offset: %q %d", w, end)
	}
	if w, end := WindowText(text, 999, 10); w != "" || end != len(text) {
		t.Fatalf("past the end: %q %d", w, end)
	}
	// Byte 2 is the tail of "ã"; the window must start at the "ã", not in it.
	if w, _ := WindowText(text, 2, 4); !strings.HasPrefix(w, "ã") {
		t.Fatalf("mid-rune offset: %q", w)
	}
	// A cap landing inside "—" backs off to before it.
	idx := strings.Index(text, "—")
	if w, _ := WindowText(text, 0, idx+1); w != text[:idx] {
		t.Fatalf("mid-rune cut: %q", w)
	}
	if w, end := WindowText(text, 0, 0); w != text || end != len(text) {
		t.Fatalf("max 0 must return everything: %q", w)
	}
}

// Output over the cap is kept; the reply names an id and the next offset;
// paging by that id walks the whole capture without re-running anything.
func TestSpilledOutputPagesWithoutRerunning(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 300; i++ {
		b.WriteString(strings.Repeat("x", 40))
		b.WriteString("\n")
	}
	full := strings.TrimSpace(b.String())
	if got := SpillOutput("short", 100, "run_command"); got != "short" {
		t.Fatalf("within the cap must pass through, got %q", got)
	}
	first := SpillOutput(full, 2000, "run_command")
	if !strings.Contains(first, "output_id=") || !strings.Contains(first, "offset=") {
		t.Fatalf("a spilled reply must name an id and an offset:\n%s", first[len(first)-300:])
	}
	id := between(first, "output_id=\"", "\"")
	var got strings.Builder
	got.WriteString(first[:strings.Index(first, "\n... [TRUNCATED")])
	offset := nextOffset(first)
	pages := 1
	for {
		page, err := PageOutput(id, offset, 2000, "run_command")
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if i := strings.Index(page, "\n... [end of output"); i >= 0 {
			got.WriteString(page[:i])
			break
		}
		got.WriteString(page[:strings.Index(page, "\n... [TRUNCATED")])
		offset = nextOffset(page)
	}
	if got.String() != full {
		t.Fatalf("pages do not reassemble the capture (%d pages, %d vs %d chars)", pages, got.Len(), len(full))
	}
	if pages < 5 {
		t.Fatalf("expected several pages, got %d", pages)
	}
	if _, err := PageOutput("nope", 0, 100, "run_command"); err == nil {
		t.Fatal("an unknown id must be an error the agent can act on")
	}
	if msg, err := PageOutput(id, len(full)+5, 100, "run_command"); err != nil || !strings.Contains(msg, "past the end") {
		t.Fatalf("past the end: %q %v", msg, err)
	}
}

// A capture is gone after its TTL.
func TestSpilledOutputExpires(t *testing.T) {
	base := spilledNow()
	defer func() { spilledNow = func() time.Time { return base } }()
	reply := SpillOutput(strings.Repeat("y\n", 3000), 100, "run_command")
	id := between(reply, "output_id=\"", "\"")
	if _, err := PageOutput(id, 100, 100, "run_command"); err != nil {
		t.Fatalf("fresh capture must page: %v", err)
	}
	spilledNow = func() time.Time { return base.Add(spilledOutputTTL + time.Minute) }
	if _, err := PageOutput(id, 100, 100, "run_command"); err == nil {
		t.Fatal("an expired capture must not page")
	}
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	if j := strings.Index(s, b); j >= 0 {
		return s[:j]
	}
	return s
}

func nextOffset(reply string) int {
	n := 0
	for _, c := range between(reply, "offset=", ")") {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
