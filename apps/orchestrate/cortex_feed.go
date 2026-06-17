package orchestrate

import (
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// The cortex feed — the "feed, don't act" half of the cortex model. An agent's
// cortex (its standing home thread) ABSORBS observations of what happened around
// it — a received channel message, a session digest — for awareness, WITHOUT
// running a turn over them. The cortex stays the agent's continuous context; it
// only RUNS on a real received turn or a trigger, and then it has all the
// accumulated observations on hand. See docs/channels-and-agents.md.

// AppendCortexObservation records one non-triggering observation into an agent's
// cortex. No-op unless the agent has Cortex enabled. The observation renders as a
// distinct report card (ReportFrom) and bumps LastAt only — NOT LastSeen — so the
// cortex reads "unread" (new activity) until the user opens it. Never runs the
// agent; this is awareness, not a turn.
func (T *OrchestrateApp) AppendCortexObservation(owner, agentID, from, text string) {
	if T == nil || T.DB == nil || owner == "" {
		return
	}
	appendCortexObs(UserDB(T.DB, owner), agentID, from, text)
}

// appendCortexObs is the db-level core of AppendCortexObservation — usable both
// from the app method (channel feed) and from tools that already hold the agent
// owner's db (the deliverable pointer).
func appendCortexObs(db Database, agentID, from, text string) {
	if db == nil || agentID == "" || strings.TrimSpace(text) == "" {
		return
	}
	ag, ok := loadAgent(db, agentID)
	if !ok || !ag.Cortex {
		return // cortex off for this agent — nothing to feed
	}
	sid := cortexSessionID(agentID)
	sess, _ := loadChatSession(db, agentID, sid)
	now := time.Now()
	if strings.TrimSpace(sess.ID) == "" {
		sess.ID = sid
		sess.AgentID = agentID
		sess.Created = now
	}
	sess.Messages = append(sess.Messages, ChatMessage{
		Role:       "assistant",
		ReportFrom: strings.TrimSpace(from),
		Content:    strings.TrimSpace(text),
		Created:    now,
	})
	sess.LastAt = now // mark unread (LastSeen untouched) — new cortex activity
	if _, err := saveChatSession(db, sess); err != nil {
		Log("[orchestrate.cortex] observation append failed for agent=%s: %v", agentID, err)
	}
}

// cortexDeliverableTools gives a Cortex-enabled agent the file_deliverable tool —
// the "cortex holds pointers, not bodies" rule made operational. A substantial
// produced artifact (a brief, a report) is filed as its OWN session; only a
// short pointer lands in the cortex. Keeps the standing thread lean (it seeds
// every fork). Returns nil unless the agent has Cortex on.
func cortexDeliverableTools(db Database, agentID string) []AgentToolDef {
	if db == nil || strings.TrimSpace(agentID) == "" {
		return nil
	}
	if ag, ok := loadAgent(db, agentID); !ok || !ag.Cortex {
		return nil
	}
	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "file_deliverable",
				Description: "File a DELIVERABLE (a brief, report, or other substantial artifact you produced on request) as its OWN session instead of putting the body in your standing thread. Only a short pointer lands in your cortex; the full text is a session the user opens from the rail. Use this for anything sizable (\"daily brief\", \"write up X\") so your standing thread stays lean — answer small/quick things inline as normal. Then point the user to the filed session; do NOT also paste the full body into this reply.",
				Parameters: map[string]ToolParam{
					"title": {Type: "string", Description: "Short title — becomes the session name. e.g. \"Daily brief — Jun 17\"."},
					"body":  {Type: "string", Description: "The full deliverable text."},
				},
				Required: []string{"title", "body"},
			},
			Handler: func(args map[string]any) (string, error) {
				title := strings.TrimSpace(oArgStr(args, "title"))
				body := strings.TrimSpace(oArgStr(args, "body"))
				if title == "" || body == "" {
					return "", fmt.Errorf("title and body are required")
				}
				now := time.Now()
				saved, err := saveChatSession(db, ChatSession{
					AgentID:  agentID,
					Title:    title,
					Created:  now,
					LastAt:   now,
					Messages: []ChatMessage{{Role: "assistant", Content: body, Created: now}},
				})
				if err != nil {
					return "", err
				}
				// Pointer (NOT the body) into the cortex — the standing thread
				// stays lean but records that the deliverable exists.
				appendCortexObs(db, agentID, "Deliverable", title+" — filed as a session; open it from the rail.")
				return fmt.Sprintf("Filed %q as a session (id %s). Point the user to it from the rail — do NOT paste the full body into your reply.", title, saved.ID), nil
			},
		},
	}
}

// cortexContextBlock renders the agent's recent cortex observations as a concise
// background-awareness block for a FORKED session — the "inherit the cortex's
// standing context at fork" half of the model. A new (non-cortex) session of a
// Cortex-enabled agent starts aware of what's recently come in over its channels
// / fired on its monitors, without copying the whole cortex thread. Returns ""
// when the cortex is off, empty, or has no observations. Live-read (kept short);
// cross-session continuity of FACTS rides the memory layer, not this.
func cortexContextBlock(db Database, agentID string) string {
	if db == nil || strings.TrimSpace(agentID) == "" {
		return ""
	}
	sess, ok := loadChatSession(db, agentID, cortexSessionID(agentID))
	if !ok || len(sess.Messages) == 0 {
		return ""
	}
	const maxLines = 8
	var lines []string
	for i := len(sess.Messages) - 1; i >= 0 && len(lines) < maxLines; i-- {
		m := sess.Messages[i]
		if strings.TrimSpace(m.ReportFrom) == "" {
			continue // observations are ReportFrom cards; skip ordinary turns
		}
		first := m.Content
		if idx := strings.IndexByte(first, '\n'); idx >= 0 {
			first = first[:idx]
		}
		// Prepend to restore chronological order (we walk newest-first).
		lines = append([]string{"- " + strings.TrimSpace(m.ReportFrom) + ": " + truncateObs(first, 160)}, lines...)
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\n## Recent standing activity (your cortex)\n\nBackground awareness — recent events on your channels / monitors. Use only if relevant to what the user asks; do not recite it.\n\n" + strings.Join(lines, "\n") + "\n"
}

// truncateObs shortens an observation snippet to n runes, appending an ellipsis.
func truncateObs(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
