package core

// Paged tool output.
//
// A tool reply is capped so one call cannot fill the context, and the cap
// used to be the end of the road: fetch_knowledge_doc read a document from
// the top and stopped, and a command's output was cut at 10,000 characters
// with a note saying how much there had been. An agent that needed the
// rest had no move — re-reading returned the same opening slice, and
// re-running a command is not free and not always safe.
//
// The fix is the same shape everywhere: the reply carries an offset the
// agent can pass back to read the next window. For a document the window
// is cut from a fresh reassembly each call, so nothing is kept. For command
// output the full capture has to be kept the moment it spills (see
// OutputStore below), because the source cannot be asked again.

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// WindowText returns up to max bytes of text starting at offset, and the
// absolute offset just past what was returned — the value a caller prints
// for the agent to pass back as the next offset. A window that would end
// mid-document is shortened to the last paragraph break past its midpoint,
// or failing that the last line break, so a page never ends mid-sentence
// when a boundary is available; a window that reaches the end of the text
// is returned whole. Offsets are clamped into the text and moved back off
// a multi-byte character's tail, and the cut never lands inside one, so any
// offset the agent passes is safe. max <= 0 returns everything from offset.
func WindowText(text string, offset, max int) (window string, end int) {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(text) {
		return "", len(text)
	}
	for offset > 0 && !utf8.RuneStart(text[offset]) {
		offset--
	}
	rest := text[offset:]
	if max <= 0 || len(rest) <= max {
		return rest, len(text)
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(rest[cut]) {
		cut--
	}
	page := rest[:cut]
	if idx := strings.LastIndex(page, "\n\n"); idx > cut/2 {
		page = page[:idx]
	} else if idx := strings.LastIndex(page, "\n"); idx > cut/2 {
		page = page[:idx]
	}
	return page, offset + len(page)
}

// --- spilled command output ------------------------------------------------

// Spilled output is kept in memory only, for a bounded time, under an
// unguessable id. Nothing is written to disk: a command's output can hold
// anything the appliance holds. The bounds keep one runaway `cat` from
// holding memory for the day.
const (
	spilledOutputTTL        = 30 * time.Minute
	spilledOutputEntryBytes = 2 << 20  // per capture; beyond this the head is kept and the note says so
	spilledOutputTotalBytes = 16 << 20 // across every capture; oldest evicted first
)

type spilledEntry struct {
	text string
	kept time.Time
}

var spilled struct {
	mu      sync.Mutex
	entries map[string]*spilledEntry
	total   int
}

// spilledNow is time.Now, swappable so a test can age entries.
var spilledNow = time.Now

// keepSpilled stores text and returns its id, evicting expired entries and
// then the oldest until the total fits.
func keepSpilled(text string) string {
	spilled.mu.Lock()
	defer spilled.mu.Unlock()
	if spilled.entries == nil {
		spilled.entries = map[string]*spilledEntry{}
	}
	now := spilledNow()
	for id, e := range spilled.entries {
		if now.Sub(e.kept) > spilledOutputTTL {
			spilled.total -= len(e.text)
			delete(spilled.entries, id)
		}
	}
	for spilled.total+len(text) > spilledOutputTotalBytes && len(spilled.entries) > 0 {
		var oldest string
		var oldestAt time.Time
		for id, e := range spilled.entries {
			if oldest == "" || e.kept.Before(oldestAt) {
				oldest, oldestAt = id, e.kept
			}
		}
		spilled.total -= len(spilled.entries[oldest].text)
		delete(spilled.entries, oldest)
	}
	id := UUIDv4()
	spilled.entries[id] = &spilledEntry{text: text, kept: now}
	spilled.total += len(text)
	return id
}

// lookupSpilled returns a kept capture, or false when the id is unknown or
// expired.
func lookupSpilled(id string) (string, bool) {
	spilled.mu.Lock()
	defer spilled.mu.Unlock()
	e, ok := spilled.entries[id]
	if !ok {
		return "", false
	}
	if spilledNow().Sub(e.kept) > spilledOutputTTL {
		spilled.total -= len(e.text)
		delete(spilled.entries, id)
		return "", false
	}
	return e.text, true
}

// SpillOutput caps a tool's captured text at max characters for the reply.
// Text within the cap is returned as is. Text over it is kept in the spill
// store and the first window is returned with a note naming the id and the
// offset to pass back to tool, so the agent reads the rest by paging instead
// of re-running the command — which was the only move it had before, and
// one that costs a round trip and is not always safe to make twice.
func SpillOutput(text string, max int, tool string) string {
	if max <= 0 || len(text) <= max {
		return text
	}
	kept := text
	clipped := ""
	if len(kept) > spilledOutputEntryBytes {
		kept, _ = WindowText(kept, 0, spilledOutputEntryBytes)
		clipped = fmt.Sprintf(" Only the first %d chars of %d were kept.", len(kept), len(text))
	}
	id := keepSpilled(kept)
	window, end := WindowText(kept, 0, max)
	return window + spillNote(tool, fmt.Sprintf("output_id=%q", id), 0, end, len(kept), kept) + clipped
}

// OutputPage is one read of a large text the agent is working through: a
// window by offset, or — with Grep set — the lines matching a pattern, each
// with its line number and the character offset its line starts at, so the
// agent can follow a hit with an Offset read of what surrounds it. The text
// is a kept command capture (ID) or supplied by the caller (Text, for a
// document reassembled per call). Neither runs anything.
//
// Grep exists because the agent's alternative was to run the command again
// through a pipe, or to save the spill to the workspace and grep that by
// hand — both of which were observed, and both of which are this call.
type OutputPage struct {
	ID      string // output_id of a kept capture; ignored when Text is set
	Text    string // the text itself, when the caller has it (a reassembled document)
	Ref     string // how the trailer names the source to the agent, e.g. `doc_id="…"`; defaults to output_id=ID
	Offset  int    // character offset to read from (into the text, or into the match list when Grep is set)
	Max     int    // window size; <= 0 means everything
	Tool    string // the tool name the trailer tells the agent to call
	Grep    string // when set, return matching lines instead of a window
	Context int    // lines of context around each match
}

// ref is the argument the trailer tells the agent to pass to reach this text.
func (p OutputPage) ref() string {
	if p.Ref != "" {
		return p.Ref
	}
	return fmt.Sprintf("output_id=%q", p.ID)
}

// Read serves the page. An unknown id is an error the agent can act on: the
// capture expired, or was made by another instance (a peer's exec keeps its
// own store).
func (p OutputPage) Read() (string, error) {
	text := p.Text
	if text == "" {
		var ok bool
		if text, ok = lookupSpilled(strings.TrimSpace(p.ID)); !ok {
			return "", fmt.Errorf("output_id %q is unknown here: the capture has expired (kept %s), or was made by another instance; re-run the command", p.ID, spilledOutputTTL)
		}
	}
	if strings.TrimSpace(p.Grep) != "" {
		return p.search(text)
	}
	if p.Offset >= len(text) {
		return fmt.Sprintf("offset %d is past the end of this output (%d chars); it has been read in full.", p.Offset, len(text)), nil
	}
	window, end := WindowText(text, p.Offset, p.Max)
	if end >= len(text) {
		return window + fmt.Sprintf("\n... [end of output: chars %d–%d of %d.%s]", p.Offset, end, len(text), p.releaseHint()), nil
	}
	return window + spillNote(p.Tool, p.ref(), p.Offset, end, len(text), text), nil
}

// search renders the matching lines. The pattern is a case-insensitive
// regular expression when it compiles as one and a plain substring
// otherwise, so a bracket in a log line is not a syntax error. Each match
// is "L<line> @<offset>: <text>", context lines are indented under it, and
// groups that are not adjacent are separated. The report is itself
// windowed by Offset and Max, with a trailer that names both the way to
// read on through the matches and the way to read around one.
func (p OutputPage) search(text string) (string, error) {
	pattern := strings.TrimSpace(p.Grep)
	match := func(s string) bool { return strings.Contains(strings.ToLower(s), strings.ToLower(pattern)) }
	if re, err := regexp.Compile("(?i)" + pattern); err == nil {
		match = re.MatchString
	}
	lines := strings.Split(text, "\n")
	starts := make([]int, len(lines)) // character offset each line begins at
	for i := 1; i < len(lines); i++ {
		starts[i] = starts[i-1] + len(lines[i-1]) + 1
	}
	var hits []int
	for i, l := range lines {
		if match(l) {
			hits = append(hits, i)
		}
	}
	if len(hits) == 0 {
		return fmt.Sprintf("no line of this output (%d lines) matches %q.", len(lines), pattern), nil
	}
	ctx := p.Context
	if ctx < 0 {
		ctx = 0
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d of %d lines match %q:\n", len(hits), len(lines), pattern)
	last := -1 // last line index rendered
	for _, h := range hits {
		from, to := h-ctx, h+ctx
		if from < 0 {
			from = 0
		}
		if to > len(lines)-1 {
			to = len(lines) - 1
		}
		if from <= last {
			from = last + 1
		} else if last >= 0 {
			b.WriteString("--\n")
		}
		for i := from; i <= to; i++ {
			if i == h {
				fmt.Fprintf(&b, "L%d @%d: %s\n", i+1, starts[i], lines[i])
			} else {
				fmt.Fprintf(&b, "   L%d: %s\n", i+1, lines[i])
			}
		}
		if to > last {
			last = to
		}
	}
	report := strings.TrimRight(b.String(), "\n")
	if p.Offset >= len(report) && p.Offset > 0 {
		return fmt.Sprintf("offset %d is past the end of the match list (%d chars); every match has been read.", p.Offset, len(report)), nil
	}
	window, end := WindowText(report, p.Offset, p.Max)
	ref := p.ref()
	trailer := fmt.Sprintf("\n... [To read around a match: %s(%s, offset=<the @offset on its line>).%s]", p.Tool, ref, p.releaseHint())
	if end < len(report) {
		trailer = fmt.Sprintf("\n... [TRUNCATED match list: chars %d–%d of %d. More matches: %s(%s, grep=%q, offset=%d). "+
			"To read around a match: %s(%s, offset=<the @offset on its line>).]",
			p.Offset, end, len(report), p.Tool, ref, pattern, end, p.Tool, ref)
	}
	return window + trailer, nil
}

// releaseHint is the one sentence that tells the agent it may now let this
// capture go. It rides on the trailers that mark an ENDING — the last window
// of a read-through, and the end of a match list — because those are the two
// moments where the agent has what it came for and the text behind it has
// stopped earning its place.
//
// Empty for a text the caller supplied (a document reassembled per call):
// there is no capture to release, and the document was never in the
// conversation whole.
func (p OutputPage) releaseHint() string {
	if p.Text != "" || strings.TrimSpace(p.ID) == "" {
		return ""
	}
	return fmt.Sprintf(" Done with it? %s(%s) drops what you have read from this conversation: the capture stays, and %s brings it back.",
		ReleaseOutputToolName, p.ref(), p.Tool)
}

// SpilledOutputExists reports whether an id still names a kept capture, so
// release_output can refuse a typo rather than report a release that
// matched nothing.
func SpilledOutputExists(id string) bool {
	_, ok := lookupSpilled(strings.TrimSpace(id))
	return ok
}

// OutputPagingToolDefs returns read_output and release_output for a loop
// that assembles its catalog by hand instead of drawing from an agent's
// allowed-tools list. Both are stateless, so neither needs a session.
//
// Every capped reply names read_output in its trailer, and a named tool the
// caller does not hold is an invitation to improvise: the agent sees that
// something was cut, is told how to reach the rest, and finds no such tool.
// Any loop whose tools can overflow wants these two.
func OutputPagingToolDefs() []AgentToolDef {
	var out []AgentToolDef
	for _, n := range []string{"read_output", ReleaseOutputToolName} {
		if ct, ok := LookupChatTool(n); ok {
			out = append(out, ChatToolToAgentToolDef(ct))
		}
	}
	return out
}

// spillNote is the truncation trailer: where the window sat, how to read on
// without re-running, and how to search the whole capture instead, which is
// usually the better move.
func spillNote(tool, ref string, offset, end, total int, text string) string {
	shownFrom := strings.Count(text[:offset], "\n") + 1
	shownTo := strings.Count(text[:end], "\n") + 1
	lines := strings.Count(text, "\n") + 1
	return fmt.Sprintf("\n... [TRUNCATED: showing chars %d–%d of %d (lines %d–%d of %d). "+
		"Read on WITHOUT re-running: %s(%s, offset=%d). "+
		"Or search the whole capture: %s(%s, grep=\"PATTERN\"), no re-run, no workspace file needed.]",
		offset, end, total, shownFrom, shownTo, lines, tool, ref, end, tool, ref)
}

// --- releasing a spent capture ----------------------------------------------

// Releasing a spent result.
//
// Compaction decides what leaves the conversation on size alone — oldest
// first, whatever it happened to be. The agent, which is the only party
// that knows which of those results it is still working from, had no say.
// It could page through a 40,000-character capture, take the one line it
// came for, and carry the other 39,900 for the rest of the turn.
//
// A result that OVERFLOWED is the one case where dropping it costs
// nothing. Its full text is already kept outside the transcript under an
// output_id (see paged_output.go), so the conversation can hold the handle
// instead of the text, and read_output brings back any part of it. That is
// what release_output does: it replaces, it does not delete, and the
// decision stays reversible for as long as the capture lives.
//
// Only spilled results are releasable, and that is the point rather than a
// limitation — a result whose text exists nowhere else cannot be dropped
// without losing it, and a small result is not worth the round trip.

// ReleaseOutputToolName is the tool the agent calls to release a capture.
// The agent loop acts on the call BY NAME, the same way it acts on
// stay_silent: the tool itself cannot reach the conversation, and a
// name-driven hook works in every loop the framework runs rather than only
// the ones an app remembered to wire a callback into.
const ReleaseOutputToolName = "release_output"

// releasedMarker opens every stub. Matched to keep a second release of the
// same id from counting the stub as a fresh reclaim, and to keep a stub
// from being mistaken for output.
const releasedMarker = "[RELEASED"

// toolResultTextPrefix is how the prompt-tool path frames a result it
// appends as plain text (see the native/prompt split in the agent loop).
// A message is only rewritten when it is a tool result in one of the two
// shapes, so a user turn that happens to quote an output_id is untouched.
const toolResultTextPrefix = "Tool result from "

// releasedStub is what a released result leaves behind: that the text is
// gone from the conversation, how much of it there was, and the handle
// that reads it back. Naming the handle is the load-bearing part — a stub
// without it is deletion wearing a softer word.
func releasedStub(id string, chars int) string {
	return fmt.Sprintf("%s you released this result to make room; %d characters are no longer in this conversation. "+
		"The capture itself was kept: read_output(%s, offset=0) brings back any part of it, and grep searches the whole of it.]",
		releasedMarker, chars, outputIDRef(id))
}

// outputIDRef is how a spilled reply names its handle to the model, and so
// also how a release finds every copy of it. One id matches the first
// window and every page read after it, because each one printed the same
// literal in its trailer.
func outputIDRef(id string) string { return fmt.Sprintf("output_id=%q", id) }

// ReleaseIDsFromArgs pulls the output ids out of a release_output call.
// Accepts one id, several separated by commas or spaces, or a JSON array —
// models produce all three for a parameter documented as "one or more".
func ReleaseIDsFromArgs(args map[string]any) []string {
	var raw []string
	switch v := args["output_id"].(type) {
	case string:
		raw = append(raw, v)
	case []any:
		for _, e := range v {
			raw = append(raw, fmt.Sprint(e))
		}
	case []string:
		raw = append(raw, v...)
	case nil:
	default:
		raw = append(raw, fmt.Sprint(v))
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range raw {
		for _, f := range strings.FieldsFunc(r, func(c rune) bool { return c == ',' || c == ' ' || c == '\n' || c == '\t' }) {
			f = strings.Trim(strings.TrimSpace(f), `"'`)
			if f == "" || f == "<nil>" || seen[f] {
				continue
			}
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// ReleaseOutputsFromHistory rewrites every tool result carrying one of the
// given output ids down to a stub naming the handle. Both result shapes are
// covered: the native one (Message.ToolResults) and the plain-text one the
// prompt-tool path appends as a user message.
//
// A stub is never rewritten again, so releasing the same id twice is a
// no-op rather than a second claim of space that was already reclaimed.
// Result slices are copied before being written, because a history message
// can share its backing array with the caller's own conversation — the same
// care retireResolvedFailureResults takes for the same reason.
//
// Returns how many results were rewritten and how many characters that
// reclaimed.
func ReleaseOutputsFromHistory(history []Message, ids []string) (released, reclaimed int) {
	if len(ids) == 0 {
		return 0, 0
	}
	refs := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			refs = append(refs, outputIDRef(id))
		}
	}
	// Which id a result belongs to, or "" when it carries none of them. A
	// result that somehow names two is released under the first, which is
	// the one its trailer told the agent to page with.
	match := func(content string) string {
		if strings.HasPrefix(strings.TrimSpace(content), releasedMarker) {
			return ""
		}
		for i, ref := range refs {
			if strings.Contains(content, ref) {
				return strings.TrimSpace(ids[i])
			}
		}
		return ""
	}
	cloned := map[int]bool{}
	for mi := range history {
		for ri := range history[mi].ToolResults {
			id := match(history[mi].ToolResults[ri].Content)
			if id == "" {
				continue
			}
			if !cloned[mi] {
				history[mi].ToolResults = append([]ToolResult(nil), history[mi].ToolResults...)
				cloned[mi] = true
			}
			n := len(history[mi].ToolResults[ri].Content)
			history[mi].ToolResults[ri].Content = releasedStub(id, n)
			released++
			reclaimed += n - len(history[mi].ToolResults[ri].Content)
		}
		if !strings.HasPrefix(history[mi].Content, toolResultTextPrefix) {
			continue
		}
		id := match(history[mi].Content)
		if id == "" {
			continue
		}
		n := len(history[mi].Content)
		history[mi].Content = toolResultTextPrefix + "a call you have since released:\n" + releasedStub(id, n)
		released++
		reclaimed += n - len(history[mi].Content)
	}
	if reclaimed < 0 {
		reclaimed = 0
	}
	return released, reclaimed
}
