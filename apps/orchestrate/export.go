// Session export — dumps a full chat session as JSON or Markdown for
// debugging, sharing, or dataset capture. Tool calls + results were
// added to ChatMessage so this export includes the full execution
// trace (orchestrator + worker tool fires), not just visible text.
//
// Endpoint: GET /api/sessions/{id}/export?agent_id=<id>&format=md|json
//
// Format defaults to markdown (easier to paste into a chat with
// another LLM). JSON is the lossless shape — preferred for
// programmatic consumption / dataset feedstock.

package orchestrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// foldCountPhrase renders how many times a thread has been folded, for the
// export's compaction note. The count matters to a reader deciding how much of
// the conversation is behind the summary.
func foldCountPhrase(seq int) string {
	switch {
	case seq <= 0:
		return "at least once"
	case seq == 1:
		return "once"
	}
	return fmt.Sprintf("%d times", seq)
}

// handleSessionExport serves the session at /api/sessions/{id}/export.
// agent_id is required as a query param (sessions are stored under
// per-agent buckets). format=json | md (default md). Sets a
// Content-Disposition so browsers download instead of rendering.
func (T *OrchestrateApp) handleSessionExport(w http.ResponseWriter, r *http.Request, agentID, sessionID string) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if agentID == "" || sessionID == "" {
		http.Error(w, "agent_id and session id are required", http.StatusBadRequest)
		return
	}
	agent, ok := loadAgent(udb, agentID)
	if !ok || (agent.Owner != user && agent.Owner != seedOwner) {
		http.NotFound(w, r)
		return
	}
	sess, ok := loadChatSession(udb, agentID, sessionID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "md"
	}
	filenameBase := fmt.Sprintf("%s-%s", slugifyAgentName(agent.Name), sess.ID)
	switch format {
	case "json":
		payload := buildExportPayload(agent, sess, udb)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filenameBase+`.json"`)
		_ = json.NewEncoder(w).Encode(payload)
	case "md", "markdown":
		body := renderSessionMarkdownWithDiag(agent, sess, udb)
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filenameBase+`.md"`)
		_, _ = w.Write([]byte(body))
	default:
		http.Error(w, "unknown format — try md or json", http.StatusBadRequest)
	}
}

// sessionExportPayload is the JSON shape returned by the export
// endpoint. Mirrors ChatSession but lifts agent metadata in so a
// downstream consumer doesn't need a second fetch to know what the
// session was for.
type sessionExportPayload struct {
	ExportedAt time.Time       `json:"exported_at"`
	Agent      exportedAgent   `json:"agent"`
	Session    exportedSession `json:"session"`
}

type exportedAgent struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
}

type exportedSession struct {
	ID                  string         `json:"id"`
	Title               string         `json:"title,omitempty"`
	Created             time.Time      `json:"created"`
	LastAt              time.Time      `json:"last_at"`
	AuthoringAgentID    string         `json:"authoring_agent_id,omitempty"`
	AwaitingUserConfirm bool           `json:"awaiting_user_confirm,omitempty"`
	Messages            []ChatMessage  `json:"messages,omitempty"`
	Plans               []PlanSnapshot `json:"plans,omitempty"`
	// Compacted records that `messages` is a TAIL, not the whole thread — the
	// leading span was folded into a summary and archived to recall before
	// being dropped from storage. Absent when nothing has been folded, so a
	// short session's payload is unchanged. This file calls JSON the lossless
	// shape; a truncation it does not mention is the one way it could lie.
	Compacted *exportedCompaction `json:"compacted,omitempty"`
}

type exportedCompaction struct {
	Summary string `json:"summary"`
	Folds   int    `json:"folds,omitempty"`
}

func buildExportPayload(agent AgentRecord, sess ChatSession, udb Database) sessionExportPayload {
	return sessionExportPayload{
		ExportedAt: time.Now(),
		Agent: exportedAgent{
			ID:           agent.ID,
			Name:         agent.Name,
			Description:  agent.Description,
			AllowedTools: agent.AllowedTools,
		},
		Session: exportedSession{
			ID:                  sess.ID,
			Title:               sess.Title,
			Created:             sess.Created,
			LastAt:              sess.LastAt,
			AuthoringAgentID:    sess.AuthoringAgentID,
			AwaitingUserConfirm: sess.AwaitingUserConfirm,
			Messages:            sess.Messages,
			Plans:               sess.Plans,
			Compacted:           exportCompaction(udb, agent.ID, sess.ID),
		},
	}
}

// exportCompaction reports the fold state when a thread has one, so an export
// can say that its message list is a tail. nil when nothing has been folded, or
// when the caller has no store to read.
func exportCompaction(udb Database, agentID, sessID string) *exportedCompaction {
	if udb == nil {
		return nil
	}
	st := loadCompactState(udb, agentID, sessID)
	if strings.TrimSpace(st.Summary) == "" {
		return nil
	}
	return &exportedCompaction{Summary: strings.TrimSpace(st.Summary), Folds: st.FoldSeq}
}

// renderSessionMarkdown produces a readable transcript suitable for
// pasting into another LLM conversation. Each message is a section
// with the role, timestamp, and content; tool calls render as nested
// "🔧 <name>(args)" / "↳ <result>" lines under the assistant message
// that owns them.
func renderSessionMarkdown(agent AgentRecord, sess ChatSession) string {
	return renderSessionMarkdownWithDiag(agent, sess, nil)
}

// renderSessionMarkdownWithDiag is renderSessionMarkdown plus the session's
// guardrail activity, read from the diagnostics trail. udb may be nil (callers
// that have no store, or don't want the section).
func renderSessionMarkdownWithDiag(agent AgentRecord, sess ChatSession, udb Database) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Session export — %s\n\n", sess.Title)
	fmt.Fprintf(&b, "- **Agent:** %s (id: %s)\n", agent.Name, agent.ID)
	if agent.Description != "" {
		fmt.Fprintf(&b, "- **Description:** %s\n", agent.Description)
	}
	fmt.Fprintf(&b, "- **Session id:** %s\n", sess.ID)
	if !sess.Created.IsZero() {
		fmt.Fprintf(&b, "- **Created:** %s\n", sess.Created.Format(time.RFC3339))
	}
	if !sess.LastAt.IsZero() {
		fmt.Fprintf(&b, "- **Last activity:** %s\n", sess.LastAt.Format(time.RFC3339))
	}
	if sess.AuthoringAgentID != "" {
		fmt.Fprintf(&b, "- **Authoring focus:** %s\n", sess.AuthoringAgentID)
	}
	fmt.Fprintf(&b, "- **Exported:** %s\n", time.Now().Format(time.RFC3339))

	// A long thread is TRIMMED in storage (trimStoredHistory): leading messages
	// already folded into the running summary and archived to recall are dropped
	// so the thread doesn't grow without bound. The export can only show what is
	// stored — and printing the original Created timestamp above a transcript
	// that starts hours later reads as a broken copy rather than a retention
	// policy. Say what happened, and carry the summary that stands in for the
	// missing span, so the export is complete in substance even when it cannot
	// be complete in verbatim.
	if st := exportCompaction(udb, agent.ID, sess.ID); st != nil {
		fmt.Fprintf(&b, "\n> **Earlier turns are not reproduced verbatim.** This thread has been compacted %s: the messages before the first one below were folded into the running summary and archived to this agent's recall index, then dropped from the stored thread to bound its size. The summary follows; the verbatim tail starts after it. Nothing was lost by the export — ask the agent about an earlier turn and it can still recall it.\n",
			foldCountPhrase(st.Folds))
		fmt.Fprintf(&b, "\n## Summary of earlier turns\n\n%s\n", st.Summary)
	}
	fmt.Fprintf(&b, "\n---\n\n")

	// Guardrail activity, if any. Placed BEFORE the transcript so a reader meets it
	// before the conversation it explains — a turn that ends in a flat refusal
	// otherwise reads as the agent being unhelpful.
	//
	// Events only: the KIND and the time, never the diag's detail. The detail names
	// the rule that fired, and an export is exactly the artifact that gets handed
	// to somebody else — shipping the rules inside it would hand a reader the
	// bisection signal the declines work to withhold. Full detail stays in the
	// app's own ⚠ trail, which only the owner can read.
	if udb != nil {
		if events := guardrailExportEvents(udb, agent.ID, sess.ID); len(events) > 0 {
			fmt.Fprintf(&b, "## Guardrail activity\n\n")
			fmt.Fprintf(&b, "An enforced check acted %d time(s) during this session. Which rule fired is\n", len(events))
			fmt.Fprintf(&b, "deliberately not included here; it is in the session's diagnostics in the app.\n\n")
			for _, e := range events {
				fmt.Fprintf(&b, "- %s — %s\n", e.At.Format(time.RFC3339), e.What)
			}
			b.WriteString("\n---\n\n")
		}
	}

	// An agent that enforces guardrails may have tool output carrying the very
	// thing its rules protect. Runtime containment covers what the agent says and
	// does; it has never covered what its tools RETURN, because that renders only
	// in the owner's own pane — which is fine until the transcript is exported and
	// handed to someone else.
	withholdResults := resolveGuardrailHooks(agent) != nil
	if withholdResults {
		b.WriteString("> **Tool results are withheld from this export.** This agent enforces guardrails, so\n")
		b.WriteString("> what its tools returned is not serialized here — only the calls it made. The\n")
		b.WriteString("> results were visible in the live session.\n\n")
	}

	planByIdx := map[int]PlanSnapshot{}
	for _, p := range sess.Plans {
		planByIdx[p.RoundIndex] = p
	}
	assistantSeq := 0
	for _, m := range sess.Messages {
		ts := ""
		if !m.Created.IsZero() {
			ts = " — " + m.Created.Format(time.RFC3339)
		}
		header := strings.ToUpper(m.Role[:1]) + m.Role[1:]
		fmt.Fprintf(&b, "## %s%s\n\n", header, ts)
		if strings.TrimSpace(m.Content) != "" {
			b.WriteString(strings.TrimSpace(m.Content))
			b.WriteString("\n\n")
		}
		if m.Role == "assistant" {
			// Plan snapshot, if any, was indexed by round
			if p, ok := planByIdx[assistantSeq]; ok && !p.Synthetic && len(p.Steps) > 0 {
				b.WriteString("**Plan:**\n\n")
				for _, st := range p.Steps {
					fmt.Fprintf(&b, "  %d. **%s** — _intent:_ %s\n", st.ID, st.Title, st.Intent)
					if st.Output != "" {
						out := st.Output
						if len(out) > 800 {
							out = out[:800] + "… [truncated]"
						}
						fmt.Fprintf(&b, "     _output:_ %s\n", strings.ReplaceAll(out, "\n", " "))
					}
				}
				b.WriteString("\n")
			}
			if len(m.ToolCalls) > 0 {
				b.WriteString("**Tool calls:**\n\n")
				for _, tc := range m.ToolCalls {
					argsJSON, _ := json.Marshal(tc.Args)
					argsStr := string(argsJSON)
					if len(argsStr) > 400 {
						argsStr = argsStr[:400] + "… [truncated]"
					}
					marker := ""
					if tc.Cached {
						marker = " ♻ cached"
					}
					fmt.Fprintf(&b, "- 🔧 `%s(%s)`%s\n", tc.Name, argsStr, marker)
					if tc.Err != "" {
						fmt.Fprintf(&b, "  ↳ ERROR: %s\n", tc.Err)
					} else if tc.Result != "" {
						if withholdResults {
							// Name and args stay — that is most of the debug value, and
							// neither carries content. The RESULT is what a guardrail
							// exists to keep in: an export is the one place tool output
							// leaves the owner-only pane, and a rule that stops the agent
							// SAYING something is not served by shipping the same content
							// in a transcript someone can forward.
							b.WriteString("  ↳ [result withheld — see below]\n")
						} else {
							res := tc.Result
							if len(res) > 800 {
								res = res[:800] + "… [truncated]"
							}
							fmt.Fprintf(&b, "  ↳ %s\n", strings.ReplaceAll(res, "\n", " "))
						}
					}
				}
				b.WriteString("\n")
			}
			if m.Usage != nil {
				// InputTokens is the whole prompt. Name the cached share when
				// there is one — otherwise a 40k-token prompt and a 40k-token
				// prompt that was almost entirely a cache hit read identically,
				// and they cost very different amounts.
				cached := ""
				if n := m.Usage.CacheReadTokens + m.Usage.CacheWriteTokens; n > 0 {
					cached = fmt.Sprintf(" (%d cached)", n)
				}
				fmt.Fprintf(&b, "_Stats: %d in%s / %d out / %d think / %.0f tok/s / %dms_\n\n",
					m.Usage.InputTokens, cached, m.Usage.OutputTokens, m.Usage.ReasoningTokens,
					m.Usage.TokensPerSec, m.Usage.ElapsedMs)
			}
			assistantSeq++
		}
		b.WriteString("---\n\n")
	}
	return b.String()
}

// slugifyAgentName makes a filename-safe version of the agent's name.
// Lowercases, replaces non-alphanumeric runs with single hyphens,
// trims leading/trailing hyphens, caps length.
func slugifyAgentName(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		isAlnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlnum {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "session"
	}
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// guardrailExportEvent is one guardrail action, reduced to what is safe to put in
// an exported transcript: when, and what kind of thing happened.
type guardrailExportEvent struct {
	At   time.Time
	What string
}

// guardrailExportEvents reads the session's diagnostics trail and returns the
// guardrail entries, translated into plain descriptions.
//
// The translation is the point. A diag's Detail names the rule and the warden's
// reason, which is right for the owner's own ⚠ trail and wrong for a file that
// gets forwarded. An unrecognized kind is dropped rather than passed through,
// because a future guardrail diag would otherwise leak its detail into exports by
// default — the safe direction for a list like this is closed.
func guardrailExportEvents(udb Database, agentID, sessionID string) []guardrailExportEvent {
	if udb == nil || agentID == "" || sessionID == "" {
		return nil
	}
	var list []SessionDiag
	udb.Get(sessionDiagTable, agentID+":"+sessionID, &list)
	label := map[string]string{
		"guardrail-blocked":            "an action or reply was blocked",
		"guardrail-input-blocked":      "a request was refused before the model ran",
		"guardrail-blocked-output":     "a reply was withheld and sent back for a rewrite",
		"guardrail-input":              "a request was flagged and the turn was steered",
		"guardrail-halted":             "the turn was stopped; the reply was written by a separate check",
		"guardrail-periodic-block":     "the turn was flagged mid-flight and redirected",
		"guardrail-output-substituted": "a reply kept breaking a rule and a neutral decline was substituted",
		"guardrail-error":              "a check could not run; the turn proceeded unchecked",
		"guardrail-no-verdict":         "a check reached no verdict; the turn proceeded unchecked",
	}
	var out []guardrailExportEvent
	for _, d := range list {
		if what, ok := label[d.Kind]; ok {
			out = append(out, guardrailExportEvent{At: d.At, What: what})
		}
	}
	return out
}
