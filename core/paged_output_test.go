package core

import (
	"fmt"
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
		page, err := OutputPage{ID: id, Offset: offset, Max: 2000, Tool: "run_command"}.Read()
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
	if _, err := (OutputPage{ID: "nope", Max: 100, Tool: "run_command"}).Read(); err == nil {
		t.Fatal("an unknown id must be an error the agent can act on")
	}
	if msg, err := (OutputPage{ID: id, Offset: len(full) + 5, Max: 100, Tool: "run_command"}).Read(); err != nil || !strings.Contains(msg, "past the end") {
		t.Fatalf("past the end: %q %v", msg, err)
	}
}

// A capture is gone after its TTL.
func TestSpilledOutputExpires(t *testing.T) {
	base := spilledNow()
	defer func() { spilledNow = func() time.Time { return base } }()
	reply := SpillOutput(strings.Repeat("y\n", 3000), 100, "run_command")
	id := between(reply, "output_id=\"", "\"")
	if _, err := (OutputPage{ID: id, Offset: 100, Max: 100, Tool: "run_command"}).Read(); err != nil {
		t.Fatalf("fresh capture must page: %v", err)
	}
	spilledNow = func() time.Time { return base.Add(spilledOutputTTL + time.Minute) }
	if _, err := (OutputPage{ID: id, Offset: 100, Max: 100, Tool: "run_command"}).Read(); err == nil {
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

// Searching a kept capture returns the matching lines with their line number
// and the offset their line starts at — and that offset, read back, lands on
// the line. Context lines ride under a hit; a regex that does not compile is
// a substring; the match list pages like anything else.
func TestSpilledOutputCanBeSearched(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 400; i++ {
		if i%97 == 0 {
			fmt.Fprintf(&b, "ERROR [unit-%d] failed to start\n", i)
		} else {
			fmt.Fprintf(&b, "line %d ok\n", i)
		}
	}
	full := strings.TrimSpace(b.String())
	reply := SpillOutput(full, 500, "run_command")
	id := between(reply, "output_id=\"", "\"")
	if !strings.Contains(reply, "grep=") {
		t.Fatalf("the truncation note must offer grep:\n%s", reply[len(reply)-300:])
	}

	out, err := OutputPage{ID: id, Tool: "run_command", Grep: "error", Context: 1}.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "4 of 400 lines match") {
		t.Fatalf("header: %q", out[:60])
	}
	if !strings.Contains(out, "L97 @") || !strings.Contains(out, "   L96: line 96 ok") || !strings.Contains(out, "   L98: line 98 ok") {
		t.Fatalf("hits carry line numbers and context:\n%s", out)
	}
	if strings.Count(out, "\n--\n") != 3 {
		t.Fatalf("non-adjacent groups are separated:\n%s", out)
	}
	// The @offset on a hit reads back to that very line.
	off := nextNumber(between(out, "L97 @", ":"))
	around, err := OutputPage{ID: id, Offset: off, Max: 40, Tool: "run_command"}.Read()
	if err != nil || !strings.HasPrefix(around, "ERROR [unit-97]") {
		t.Fatalf("offset from a hit must land on it: %q %v", around, err)
	}
	// A pattern that is not a valid regex still works as a substring.
	if out, _ := (OutputPage{ID: id, Tool: "run_command", Grep: "[unit-194]"}).Read(); !strings.Contains(out, "1 of 400 lines match") {
		t.Fatalf("bracketed substring: %q", out[:80])
	}
	// A regex works as one.
	if out, _ := (OutputPage{ID: id, Tool: "run_command", Grep: "unit-(97|291)"}).Read(); !strings.HasPrefix(out, "2 of 400") {
		t.Fatalf("regex: %q", out[:80])
	}
	if out, _ := (OutputPage{ID: id, Tool: "run_command", Grep: "nothing here"}).Read(); !strings.HasPrefix(out, "no line") {
		t.Fatalf("no match: %q", out)
	}
	// A wide match list pages by offset with a trailer that keeps the grep.
	wide, _ := (OutputPage{ID: id, Max: 300, Tool: "run_command", Grep: "ok"}).Read()
	if !strings.Contains(wide, "TRUNCATED match list") || !strings.Contains(wide, "grep=\"ok\"") {
		t.Fatalf("match list must page:\n%s", wide)
	}
}

func nextNumber(s string) int {
	n := 0
	for _, c := range strings.TrimSpace(s) {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// A caller with the text in hand (a reassembled document) searches it the
// same way, and the trailer names the source the caller's way.
func TestSuppliedTextCanBeSearchedAndPaged(t *testing.T) {
	doc := "# Guide\n\n## Install\n\nrun the installer\n\n## Ports\n\nopen 8443 for the api\nopen 22 for ssh\n"
	out, err := OutputPage{Text: doc, Ref: `doc_id="g1"`, Tool: "fetch_knowledge_doc", Grep: "open \\d+"}.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "2 of ") || !strings.Contains(out, `fetch_knowledge_doc(doc_id="g1", offset=<the @offset on its line>)`) {
		t.Fatalf("supplied text must search and name the doc in the trailer:\n%s", out)
	}
	off := nextNumber(between(out, "@", ":"))
	around, err := OutputPage{Text: doc, Ref: `doc_id="g1"`, Offset: off, Max: 30, Tool: "fetch_knowledge_doc"}.Read()
	if err != nil || !strings.HasPrefix(around, "open 8443") {
		t.Fatalf("offset from a hit must land on it: %q %v", around, err)
	}
	if !strings.Contains(around, `fetch_knowledge_doc(doc_id="g1", offset=`) {
		t.Fatalf("paging a supplied text must name the doc, not an output_id:\n%s", around)
	}
}
