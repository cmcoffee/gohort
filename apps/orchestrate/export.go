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
		payload := buildExportPayload(agent, sess)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filenameBase+`.json"`)
		_ = json.NewEncoder(w).Encode(payload)
	case "md", "markdown":
		body := renderSessionMarkdown(agent, sess)
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
	ExportedAt time.Time      `json:"exported_at"`
	Agent      exportedAgent  `json:"agent"`
	Session    exportedSession `json:"session"`
}

type exportedAgent struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description,omitempty"`
	AllowedTools     []string `json:"allowed_tools,omitempty"`
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
}

func buildExportPayload(agent AgentRecord, sess ChatSession) sessionExportPayload {
	return sessionExportPayload{
		ExportedAt: time.Now(),
		Agent: exportedAgent{
			ID:               agent.ID,
			Name:             agent.Name,
			Description:      agent.Description,
			AllowedTools:     agent.AllowedTools,
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
		},
	}
}

// renderSessionMarkdown produces a readable transcript suitable for
// pasting into another LLM conversation. Each message is a section
// with the role, timestamp, and content; tool calls render as nested
// "🔧 <name>(args)" / "↳ <result>" lines under the assistant message
// that owns them.
func renderSessionMarkdown(agent AgentRecord, sess ChatSession) string {
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
	fmt.Fprintf(&b, "\n---\n\n")

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
			if p, ok := planByIdx[assistantSeq]; ok && len(p.Steps) > 0 {
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
						res := tc.Result
						if len(res) > 800 {
							res = res[:800] + "… [truncated]"
						}
						fmt.Fprintf(&b, "  ↳ %s\n", strings.ReplaceAll(res, "\n", " "))
					}
				}
				b.WriteString("\n")
			}
			if m.Usage != nil {
				fmt.Fprintf(&b, "_Stats: %d in / %d out / %d think / %.0f tok/s / %dms_\n\n",
					m.Usage.InputTokens, m.Usage.OutputTokens, m.Usage.ReasoningTokens,
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
