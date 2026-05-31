// Tool-result spill: when a tool returns a body larger than the
// inline cap, write the full body to the session's workspace and
// hand the LLM back a short stub describing what was spilled and how
// to query it (head / tail / grep / read_lines / stat). Prevents one
// fat result (a giant log read, a verbose sub-agent transcript, an
// unbounded pipeline output) from filling the model's context window
// in a single round.
//
// Why a wrap-layer concern, not per-tool: every author would have to
// remember to cap their output, and the framework can't trust that
// they will. Catching it once at the dispatch boundary covers all
// tools — registered ChatTools, temp tools, sub-agent dispatches,
// future authors, third-party additions — with no per-author work.

package orchestrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// spillThresholdBytes is the inline cap. Anything above this gets
// spilled. Sized at 100 KB (~25k tokens, ~12% of a 200K context
// window): big enough that normal tool outputs — news aggregations,
// full-page fetches, structured JSON dumps, multi-paragraph
// summaries — pass through inline as the LLM expects, small enough
// to catch the real failure modes (a megabyte log read, a multi-MB
// pipeline aggregation, a runaway sub-agent transcript) before they
// blow the window.
//
// The original 503k-token incident was 2 MB in a single message;
// any cap south of ~250 KB would have prevented it. 100 KB is the
// "noisy outputs still feel normal; obviously-huge ones get caught"
// sweet spot. Tune up or down per real-world observation.
const spillThresholdBytes = 100 * 1024

// spillHeadBytes / spillTailBytes are sampled from the start/end of
// the original body and embedded in the stub. Sized so the LLM gets
// a real look at the shape — a CSV header + first few rows, the
// opening of a structured doc, the first chunk of a log — not just
// a teaser. Stub total is ~6 KB; small relative to the threshold,
// large enough to often answer the question without a follow-up
// query at all.
const (
	spillHeadBytes = 4000
	spillTailBytes = 1000
)

// spillDirName is the per-workspace subdir under which spilled
// bodies land. Keeps them visually separated from user-written
// files so a `workspace ls` doesn't drown in framework artifacts.
const spillDirName = ".tool_spill"

// spillCounter disambiguates spill filenames within a single second.
// Atomic so concurrent tool dispatches in the same turn don't collide.
var spillCounter uint64

// maybeSpillToolResult inspects a tool result. If it's large enough
// to warrant a spill (and there's a usable session workspace), it
// writes the full body to disk under <workspace>/.tool_spill/ and
// returns a stub plus ok=true. Otherwise returns ("", false) and the
// caller keeps the original body.
//
// Failures (no workspace, mkdir error, write error) silently fall
// through to ok=false — a failed spill must NOT lose the original
// result, since "smaller-than-ideal context" is strictly better than
// "tool call appeared to return nothing." The Log line surfaces the
// failure for operator diagnosis.
func maybeSpillToolResult(sess *ToolSession, toolName, body string) (string, bool) {
	if len(body) <= spillThresholdBytes {
		return "", false
	}
	if sess == nil {
		return "", false
	}
	wsDir := strings.TrimSpace(sess.WorkspaceDir)
	if wsDir == "" {
		// Auto-mint a workspace so spill always has a place to land.
		// Without this, sub-agent dispatches and other paths that
		// don't pre-mint a workspace would lose the guard.
		dir, err := EnsureSessionWorkspace(sess)
		if err != nil {
			Log("[spill] no workspace for %s and auto-mint failed: %v — returning body as-is (%d bytes)", toolName, err, len(body))
			return "", false
		}
		wsDir = dir
	}
	spillDir := filepath.Join(wsDir, spillDirName)
	if err := os.MkdirAll(spillDir, 0700); err != nil {
		Log("[spill] mkdir %s failed: %v — returning body as-is", spillDir, err)
		return "", false
	}
	fname := spillFilename(toolName, body)
	abs := filepath.Join(spillDir, fname)
	if err := os.WriteFile(abs, []byte(body), 0600); err != nil {
		Log("[spill] write %s failed: %v — returning body as-is", abs, err)
		return "", false
	}
	rel := filepath.Join(spillDirName, fname)
	stub := renderSpillStub(toolName, rel, body)
	Log("[spill] %s → %s (%d bytes, stub=%d bytes)", toolName, rel, len(body), len(stub))
	return stub, true
}

// renderSpillStub builds the LLM-facing replacement: header + head
// sample + tail sample + actionable directive. The directive names
// the workspace query actions explicitly so the LLM doesn't have to
// remember the API surface — the message itself documents the next
// move.
func renderSpillStub(toolName, rel, body string) string {
	head := body
	if len(head) > spillHeadBytes {
		head = head[:spillHeadBytes]
	}
	var tail string
	if len(body) > spillHeadBytes+spillTailBytes {
		tail = body[len(body)-spillTailBytes:]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Tool %q returned %d bytes — too large to inline. Full body saved to workspace as %q.]\n",
		toolName, len(body), rel)
	fmt.Fprintf(&b, "\n--- first %d bytes ---\n%s\n", min2(len(body), spillHeadBytes), head)
	if tail != "" {
		fmt.Fprintf(&b, "\n--- last %d bytes ---\n%s\n", spillTailBytes, tail)
	}
	b.WriteString("\n--- next ---\n")
	b.WriteString("To explore the rest, use the workspace query actions against this path:\n")
	fmt.Fprintf(&b, "  workspace(action=\"stat\", path=%q)              → size, mtime, line count, kind hint\n", rel)
	fmt.Fprintf(&b, "  workspace(action=\"head\", path=%q, lines=N)      → first N lines\n", rel)
	fmt.Fprintf(&b, "  workspace(action=\"tail\", path=%q, lines=N)      → last N lines\n", rel)
	fmt.Fprintf(&b, "  workspace(action=\"read_lines\", path=%q, start=A, end=B) → lines A–B\n", rel)
	fmt.Fprintf(&b, "  workspace(action=\"grep\", path=%q, pattern=\"…\")  → matching lines (RE2)\n", rel)
	b.WriteString("Pick the action that matches what you need. Don't re-call the original tool — its full output is already on disk.")
	return b.String()
}

// spillFilename names the spill file: <tool>_<unix-secs>_<counter>.txt.
// Tool name first so an `ls` shows what produced each file at a
// glance; unix-secs + counter make it unique even under burst
// concurrent dispatches.
func spillFilename(toolName, body string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			return r
		}
		return '_'
	}, toolName)
	if clean == "" {
		clean = "tool"
	}
	n := atomic.AddUint64(&spillCounter, 1)
	ext := sniffExt(body)
	return fmt.Sprintf("%s_%d_%d%s", clean, time.Now().Unix(), n, ext)
}

// sniffExt picks an extension based on the body's first few bytes.
// Keeps the on-disk file human-recognizable in an `ls` so the
// operator (or the LLM glancing at workspace contents) can tell
// "this was JSON" without opening it. Heuristic — falls back to .txt.
func sniffExt(body string) string {
	s := strings.TrimSpace(body)
	if len(s) == 0 {
		return ".txt"
	}
	switch s[0] {
	case '{', '[':
		return ".json"
	case '<':
		lower := strings.ToLower(s)
		if strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") {
			return ".html"
		}
		return ".xml"
	}
	return ".txt"
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- attachment spill ---

// attachmentSpillThresholdBytes is the inline cap for user-uploaded
// attachments. Matches the tool-result threshold (100 KB) — small
// attachments stay inline as the user intended; only the genuinely
// large ones (a 50+ page PDF, a multi-MB transcript) get spilled to
// a queryable workspace file. Attachments are user-intentional so
// the bar is the same as for tool results, not stricter.
const attachmentSpillThresholdBytes = 100 * 1024

// attachmentSpillDirName is where extracted attachment text lands
// inside the session workspace.
const attachmentSpillDirName = ".attachments"

// buildAttachmentPreamble formats the per-attachment block prepended
// to the user message. Small attachments inline verbatim via the
// existing FormatAttachmentPreamble. Large ones spill the extracted
// text to <workspace>/.attachments/<name>.txt and inject a stub: head
// sample + tail sample + path + directive to use workspace stat/head/
// grep/read_lines/tail. Same vocabulary the LLM already uses for the
// tool-result spill — one set of muscle memory covers both.
//
// Falls back to inline whenever the spill plumbing fails (no
// workspace and auto-mint failed, mkdir error, write error). A failed
// spill must NOT discard the attachment; inlining a large body is a
// worse outcome than the spill stub, but it's better than dropping
// the user's content entirely.
func buildAttachmentPreamble(sess *ToolSession, name, mime, text string) string {
	if len(text) <= attachmentSpillThresholdBytes {
		return FormatAttachmentPreamble(name, mime, text)
	}
	// Audio transcripts are special — even when long, the agent prompt
	// in FormatAttachmentPreamble specifically tells the LLM "this IS
	// the complete transcript, don't try to re-transcribe." Spilling
	// would lose that directive. For audio, fall through to inline so
	// the audio-specific framing stays intact. (Audio transcripts that
	// are huge will trigger the regular history-compaction path on
	// later rounds; the user has already paid for the transcription.)
	if isAudioMime(mime, name) {
		return FormatAttachmentPreamble(name, mime, text)
	}
	wsDir := strings.TrimSpace(sess.WorkspaceDir)
	if wsDir == "" {
		dir, err := EnsureSessionWorkspace(sess)
		if err != nil {
			Log("[attachment_spill] no workspace for %q and auto-mint failed: %v — inlining %d bytes", name, err, len(text))
			return FormatAttachmentPreamble(name, mime, text)
		}
		wsDir = dir
	}
	spillDir := filepath.Join(wsDir, attachmentSpillDirName)
	if err := os.MkdirAll(spillDir, 0700); err != nil {
		Log("[attachment_spill] mkdir %s failed: %v — inlining", spillDir, err)
		return FormatAttachmentPreamble(name, mime, text)
	}
	fname := attachmentSpillFilename(name)
	abs := filepath.Join(spillDir, fname)
	if err := os.WriteFile(abs, []byte(text), 0600); err != nil {
		Log("[attachment_spill] write %s failed: %v — inlining", abs, err)
		return FormatAttachmentPreamble(name, mime, text)
	}
	rel := filepath.Join(attachmentSpillDirName, fname)
	stub := renderAttachmentStub(name, mime, rel, text)
	Log("[attachment_spill] %q → %s (%d bytes extracted, stub=%d bytes)", name, rel, len(text), len(stub))
	return stub
}

// renderAttachmentStub formats the inline block for a spilled
// attachment. Same vocabulary as renderSpillStub (head sample, tail
// sample, workspace query directives) so the LLM doesn't need to
// learn a second pattern.
func renderAttachmentStub(name, mime, rel, text string) string {
	head := text
	if len(head) > spillHeadBytes {
		head = head[:spillHeadBytes]
	}
	var tail string
	if len(text) > spillHeadBytes+spillTailBytes {
		tail = text[len(text)-spillTailBytes:]
	}
	// Rough page estimate for PDFs at ~3000 chars/page average. Hint
	// only — useful for "this is a 50-page document" intuition.
	pageHint := ""
	if strings.Contains(strings.ToLower(mime), "pdf") || strings.HasSuffix(strings.ToLower(name), ".pdf") {
		pages := (len(text) + 2999) / 3000
		pageHint = fmt.Sprintf(" (~%d pages)", pages)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Attached document: %s%s\n\n", name, pageHint)
	fmt.Fprintf(&b, "[Document text is %d characters — too large to inline. Full extracted text saved to workspace as %q.]\n",
		len(text), rel)
	fmt.Fprintf(&b, "\n--- first %d characters ---\n%s\n", min2(len(text), spillHeadBytes), head)
	if tail != "" {
		fmt.Fprintf(&b, "\n--- last %d characters ---\n%s\n", spillTailBytes, tail)
	}
	b.WriteString("\n--- query the rest with these workspace actions ---\n")
	fmt.Fprintf(&b, "  workspace(action=\"stat\", path=%q)              → size, line count, kind hint\n", rel)
	fmt.Fprintf(&b, "  workspace(action=\"head\", path=%q, lines=N)      → first N lines\n", rel)
	fmt.Fprintf(&b, "  workspace(action=\"tail\", path=%q, lines=N)      → last N lines\n", rel)
	fmt.Fprintf(&b, "  workspace(action=\"read_lines\", path=%q, start=A, end=B) → lines A–B\n", rel)
	fmt.Fprintf(&b, "  workspace(action=\"grep\", path=%q, pattern=\"…\")  → matching lines (RE2)\n", rel)
	b.WriteString("Reach for grep when looking for a specific section; head/tail for orientation; read_lines once you know roughly where the answer is. Don't pull the whole document in — pull the slice that answers the question.\n\n---\n\n")
	return b.String()
}

// attachmentSpillFilename produces a workspace-safe filename from
// the user's attachment name. Preserves the readable stem so an `ls`
// of .attachments shows what the user uploaded. Always ends in .txt
// — the extracted plaintext doesn't preserve the original format.
func attachmentSpillFilename(name string) string {
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			return r
		}
		return '_'
	}, stem)
	if clean == "" {
		clean = "attachment"
	}
	// A counter suffix disambiguates multiple uploads with the same
	// name in one turn (e.g. two "report.pdf" attachments).
	n := atomic.AddUint64(&spillCounter, 1)
	return fmt.Sprintf("%s_%d.txt", clean, n)
}

// isAudioMime reports whether an attachment is audio (so the spill
// helper can keep the special transcript framing inline).
func isAudioMime(mime, name string) bool {
	lower := strings.ToLower(mime)
	if strings.HasPrefix(lower, "audio/") {
		return true
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".mp3", ".wav", ".m4a", ".ogg", ".flac", ".aac", ".wma", ".opus":
		return true
	}
	return false
}
