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

// spillAndID runs a text through SpillOutput and returns the reply plus the
// id the trailer printed, which is the only way an agent learns the handle.
func spillAndID(t *testing.T, text string, max int) (reply, id string) {
	t.Helper()
	reply = SpillOutput(text, max, "read_output")
	const marker = `output_id="`
	i := strings.Index(reply, marker)
	if i < 0 {
		t.Fatalf("spilled reply names no output_id:\n%s", reply)
	}
	rest := reply[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("output_id is unterminated in:\n%s", reply)
	}
	return reply, rest[:j]
}

func TestReleaseOutputsFromHistory(t *testing.T) {
	full := strings.Repeat("a line of output that goes on\n", 800)
	reply, id := spillAndID(t, full, 2000)

	// A second page of the same capture, as read_output would have returned
	// it — releasing has to find every copy, not only the first.
	page, err := OutputPage{ID: id, Offset: 1500, Max: 1000, Tool: "read_output"}.Read()
	if err != nil {
		t.Fatalf("reading page 2: %v", err)
	}

	history := []Message{
		{Role: "user", Content: "look at the log"},
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "1", Name: "run_command"}}},
		{Role: "user", ToolResults: []ToolResult{{ID: "1", Content: reply}}},
		{Role: "user", ToolResults: []ToolResult{{ID: "2", Content: page}, {ID: "3", Content: "something else entirely"}}},
	}
	before := len(reply) + len(page)

	n, reclaimed := ReleaseOutputsFromHistory(history, []string{id})
	if n != 2 {
		t.Fatalf("released %d results, want both copies of the capture", n)
	}
	if reclaimed <= 0 || reclaimed >= before {
		t.Fatalf("reclaimed %d chars, want more than 0 and less than the %d released", reclaimed, before)
	}
	for _, m := range history {
		for _, r := range m.ToolResults {
			if r.ID == "3" {
				if r.Content != "something else entirely" {
					t.Fatalf("an unrelated result was rewritten: %q", r.Content)
				}
				continue
			}
			if !strings.HasPrefix(r.Content, releasedMarker) {
				t.Fatalf("result %s was not released:\n%s", r.ID, r.Content)
			}
			// The stub must name the handle. Without it the agent has
			// dropped something it can no longer reach.
			if !strings.Contains(r.Content, id) {
				t.Fatalf("stub for %s does not name the id:\n%s", r.ID, r.Content)
			}
			if !strings.Contains(r.Content, "read_output") {
				t.Fatalf("stub for %s does not say how to read it back:\n%s", r.ID, r.Content)
			}
		}
	}

	// The capture survives the release — that is the whole claim.
	back, err := OutputPage{ID: id, Offset: 0, Max: 500, Tool: "read_output"}.Read()
	if err != nil {
		t.Fatalf("capture was lost by releasing it: %v", err)
	}
	if !strings.Contains(back, "a line of output") {
		t.Fatalf("read-back does not hold the original text:\n%s", back)
	}

	// Releasing again finds nothing new: a stub is not a second reclaim.
	if n2, r2 := ReleaseOutputsFromHistory(history, []string{id}); n2 != 0 || r2 != 0 {
		t.Fatalf("re-release claimed %d results / %d chars, want 0 / 0", n2, r2)
	}
}

// The prompt-tool path appends a result as plain text rather than as a
// ToolResult. Both shapes are tool results and both must release.
func TestReleaseOutputsFromHistoryPromptToolShape(t *testing.T) {
	full := strings.Repeat("noisy output\n", 900)
	reply, id := spillAndID(t, full, 1500)
	history := []Message{
		{Role: "user", Content: "Tool result from run_command:\n" + reply},
		// A user turn that merely quotes the id is not a tool result and
		// must be left exactly as the person wrote it.
		{Role: "user", Content: `what was in output_id="` + id + `"?`},
	}
	n, _ := ReleaseOutputsFromHistory(history, []string{id})
	if n != 1 {
		t.Fatalf("released %d, want 1", n)
	}
	if !strings.Contains(history[0].Content, releasedMarker) {
		t.Fatalf("plain-text result not released:\n%s", history[0].Content)
	}
	if !strings.HasPrefix(history[0].Content, toolResultTextPrefix) {
		t.Fatalf("released result lost its tool-result framing:\n%s", history[0].Content)
	}
	if !strings.HasPrefix(history[1].Content, "what was in") {
		t.Fatalf("a user turn was rewritten: %q", history[1].Content)
	}
}

func TestReleaseIDsFromArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want []string
	}{
		{"one", map[string]any{"output_id": "abc"}, []string{"abc"}},
		{"comma separated", map[string]any{"output_id": "abc, def"}, []string{"abc", "def"}},
		{"json array", map[string]any{"output_id": []any{"abc", "def"}}, []string{"abc", "def"}},
		{"quoted", map[string]any{"output_id": `"abc"`}, []string{"abc"}},
		{"repeated", map[string]any{"output_id": "abc abc"}, []string{"abc"}},
		{"missing", map[string]any{}, nil},
	} {
		got := ReleaseIDsFromArgs(tc.args)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
			}
		}
	}
}

// Reading a capture to its end offers the release; reading a document the
// caller supplied has nothing to offer, because there is no capture.
func TestReleaseHintOnlyForKeptCaptures(t *testing.T) {
	_, id := spillAndID(t, strings.Repeat("x\n", 2000), 1200)
	end, err := OutputPage{ID: id, Offset: 0, Max: 0, Tool: "read_output"}.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(end, ReleaseOutputToolName) {
		t.Fatalf("end of a capture does not offer the release:\n%s", end[len(end)-300:])
	}
	doc, err := OutputPage{Text: "a short document", Ref: `doc_id="d1"`, Tool: "fetch_knowledge_doc"}.Read()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(doc, ReleaseOutputToolName) {
		t.Fatalf("a supplied document offered a release it cannot honor:\n%s", doc)
	}
}
