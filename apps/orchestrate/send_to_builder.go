// Send to Builder — hands the active chat session off to the Builder
// agent so it can see where the agent fell short and improve it.
//
// The flow mirrors the cross-app "send to techwriter" handoff, but it's
// intra-app (both the agent under review and Builder are orchestrate
// agents). When the user has had to correct an agent mid-conversation,
// the toolbar "Builder" button stages a brief — a short framing plus the
// full session transcript (messages + tool calls) — and deep-links into
// a fresh Builder session that auto-sends the brief. Builder then reads
// the agent's current config, diagnoses the misbehavior, and proposes
// changes interactively before applying anything.
//
// Endpoints:
//
//	POST /api/sessions/{sid}/send-to-builder?agent_id=<id>
//	     — stage the brief; returns {brief_id, builder_agent_id}.
//	GET  /api/builder-brief/{id}
//	     — fetch the staged brief text (one-shot: consumed on read).
//
// The brief is staged server-side (not passed through the URL) because
// the transcript is too large for a query param and lives in the DB
// under per-agent session buckets the browser can't read directly.

package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// builderBriefTable holds staged improvement briefs keyed by a one-shot
// UUID. Scoped to the user's db; rows are deleted when Builder's chat
// surface fetches them, so large transcripts don't accumulate.
const builderBriefTable = "orchestrate_builder_briefs"

// builderBriefRecord is the staged handoff payload. Text is the full
// brief (framing + transcript) that becomes Builder's first user
// message.
type builderBriefRecord struct {
	ID            string    `json:"id"`
	Text          string    `json:"text"`
	SourceAgentID string    `json:"source_agent_id"`
	Created       time.Time `json:"created"`
}

// handleSendToBuilder stages a brief for the given session and returns
// its id. Reached from handleSessionOne when the path carries the
// /send-to-builder sub-action. agent is the agent under review (the one
// the user was just correcting); sessionID is the session to bundle.
func (T *OrchestrateApp) handleSendToBuilder(w http.ResponseWriter, r *http.Request, agent AgentRecord, sessionID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// Builder improves OTHER agents — handing it its own session would
	// be a no-op loop. Send the agent you actually want to fix.
	if agent.ID == "seed-builder" {
		http.Error(w, "Builder improves other agents — open the agent you want to improve, then send its session to Builder.", http.StatusBadRequest)
		return
	}
	sess, ok := loadChatSession(udb, agent.ID, sessionID)
	if !ok || len(sess.Messages) == 0 {
		http.Error(w, "no session to send — chat with the agent first", http.StatusNotFound)
		return
	}
	brief := builderBriefRecord{
		ID:            UUIDv4(),
		Text:          buildBuilderBrief(agent, sess),
		SourceAgentID: agent.ID,
		Created:       time.Now(),
	}
	udb.Set(builderBriefTable, brief.ID, brief)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"brief_id":         brief.ID,
		"builder_agent_id": "seed-builder",
	})
}

// handleBuilderBrief serves GET /api/builder-brief/{id}. Returns the
// staged brief text and deletes the row (one-shot) so the transcript
// isn't left lying around after Builder's surface consumes it.
func (T *OrchestrateApp) handleBuilderBrief(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/builder-brief/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	var brief builderBriefRecord
	if !udb.Get(builderBriefTable, id, &brief) {
		http.NotFound(w, r)
		return
	}
	udb.Unset(builderBriefTable, id)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"text": brief.Text})
}

// maxBriefTranscript caps the transcript portion of a brief. Corrections
// are usually recent, so when a session is very long we keep the tail
// (the most recent turns) and note that earlier turns were dropped.
const maxBriefTranscript = 60000

// buildBuilderBrief assembles the first-person handoff message Builder
// receives. It frames the task (improve THIS agent), points Builder at
// the agent's live config, and appends the full session transcript so
// Builder can see exactly where the behavior fell short.
func buildBuilderBrief(agent AgentRecord, sess ChatSession) string {
	var b strings.Builder
	b.WriteString("I was just working with one of my agents and had to correct it during the session below. Please review what happened and improve the agent so it handles this kind of thing correctly next time.\n\n")
	fmt.Fprintf(&b, "**Agent to improve:** %s  (id: `%s`)\n", agent.Name, agent.ID)
	if d := strings.TrimSpace(agent.Description); d != "" {
		fmt.Fprintf(&b, "**What it's for:** %s\n", d)
	}
	b.WriteString("\nPlease:\n")
	b.WriteString("1. Pull this agent's current configuration (agents tool, action \"get\", full true) so you can see its prompt, rules, and tools before changing anything.\n")
	b.WriteString("2. Read the session transcript below and pinpoint where its behavior fell short of what I wanted — the spots where I had to correct, redirect, or repeat myself.\n")
	b.WriteString("3. Propose specific changes (prompt wording, standing rules, tools, or knowledge) that would prevent the problem, and walk me through them before you apply anything.\n\n")
	b.WriteString("---\n\n")

	transcript := renderSessionMarkdown(agent, sess)
	if len(transcript) > maxBriefTranscript {
		transcript = "_[Earlier turns omitted — showing the most recent part of the session.]_\n\n" +
			transcript[len(transcript)-maxBriefTranscript:]
	}
	b.WriteString(transcript)
	return b.String()
}

// sendToBuilderToolDef lets an orchestrator agent hand an authoring request off
// to Builder as a one-click link instead of a prose punt. It stages a brief (the
// request, composed by the calling agent) and returns a deep-link into a fresh
// Builder session that auto-loads it, so Builder runs its normal back-and-forth
// intake. Request-driven and LLM-triggered, mirroring the toolbar "Send to
// Builder" handoff (which is session-transcript and button-driven).
func (t *chatTurn) sendToBuilderToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "send_to_builder",
			Description: "Hand an agent / pipeline / skill AUTHORING request off to Builder. Use this whenever the user wants to MAKE, MODIFY, CLONE, or DELETE an agent, pipeline, or skill, instead of describing Builder in prose. It stages the request and returns a one-click link; put that link in your reply so the user lands in a fresh Builder session with their request pre-loaded, where Builder asks clarifying questions, drafts, and confirms. You do not author agents/pipelines/skills yourself.",
			Parameters: map[string]ToolParam{
				"request": {Type: "string", Description: "A clear, first-person description of what the user wants built or changed, with the relevant detail from this conversation (what it should do, who it is for, examples, constraints). Builder reads this as its opening brief, so be specific enough that it need not re-ask everything."},
			},
			Required: []string{"request"},
		},
		Handler: func(args map[string]any) (string, error) {
			request := strings.TrimSpace(stringArg(args, "request"))
			if request == "" {
				return "", fmt.Errorf("request is required: describe what the user wants built or changed")
			}
			if t.udb == nil {
				return "", fmt.Errorf("no user store available to stage the request")
			}
			brief := builderBriefRecord{
				ID:      UUIDv4(),
				Text:    buildBuilderRequestBrief(request),
				Created: time.Now(),
			}
			t.udb.Set(builderBriefTable, brief.ID, brief)
			link := "?agent=seed-builder&builder_brief=" + brief.ID
			return fmt.Sprintf("Request staged for Builder. Put this link in your reply verbatim (a clickable markdown link) and end your turn. Clicking it opens a fresh Builder session with the request loaded:\n\n[✦ Open this in Builder](%s)", link), nil
		},
	}
}

// buildBuilderRequestBrief frames a request-driven handoff as Builder's opening
// message: a build/modify ask plus the request, telling Builder to run its
// normal clarify, draft, confirm intake.
func buildBuilderRequestBrief(request string) string {
	var b strings.Builder
	b.WriteString("I want to build or change something, described below. Please run your normal intake: ask me any clarifying questions, then draft it and confirm with me before you apply anything.\n\n")
	b.WriteString("---\n\n")
	b.WriteString(request)
	return b.String()
}
