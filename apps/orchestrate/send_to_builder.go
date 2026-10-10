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
// The brief asks Builder to name the cause before it proposes anything, and,
// for a tool, to replay the call that went wrong after the fix: a correction
// the user made by hand says what the right result was, and a fix that does
// not produce it is not one.
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

	. "github.com/cmcoffee/oddjob/core"
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
	// Candidates is what the session's trouble could be in, carried to the
	// Builder session that receives this brief (builder_triage.go).
	Candidates []TriageCandidate `json:"candidates,omitempty"`
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
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// Builder improves OTHER agents — handing it its own session would
	// be a no-op loop. Send the agent you actually want to fix.
	if agent.ID == "seed-builder" {
		http.Error(w, "Builder improves other agents: open the agent you want to improve, then send its session to Builder.", http.StatusBadRequest)
		return
	}
	sess, ok := loadChatSession(udb, agent.ID, sessionID)
	if !ok || len(sess.Messages) == 0 {
		http.Error(w, "no session to send: chat with the agent first", http.StatusNotFound)
		return
	}
	// Why the user is sending it. Optional, and best-effort to read: a body
	// that will not parse costs the reason, not the handoff.
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	cands := triageCandidates(agent, sess, udb, user)
	brief := builderBriefRecord{
		ID:            UUIDv4(),
		Text:          buildBuilderBrief(agent, sess, body.Reason, exportForOwner(agent, user), cands),
		SourceAgentID: agent.ID,
		Created:       time.Now(),
		Candidates:    cands,
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
//
// reason is what the USER said is wrong, asked for at the button. It leads
// the brief, because the alternative is Builder inferring a reason from a
// transcript it has already been told contains a failure — which produces a
// confident diagnosis of whichever problem it noticed first, and that is not
// reliably the one the user cared about. An empty reason is stated as absent
// rather than papered over, so Builder asks instead of guessing.
func buildBuilderBrief(agent AgentRecord, sess ChatSession, reason string, forOwner bool, cands []TriageCandidate) string {
	var b strings.Builder
	reason = strings.TrimSpace(reason)
	if reason != "" {
		b.WriteString("I want to improve one of my agents. Here is what is wrong with it, in my own words:\n\n")
		// The reason is the user's own text and it is the INSTRUCTION here, so
		// it is quoted for legibility rather than fenced as untrusted: it came
		// from the person Builder is working for, typed into this button.
		for _, line := range strings.Split(reason, "\n") {
			b.WriteString("> " + line + "\n")
		}
		b.WriteString("\nThe session below is where it happened. Treat what I just said as the problem to solve; use the transcript as evidence for it, and tell me if what I described is not what you find there.\n\n")
	} else {
		b.WriteString("I was just working with one of my agents and want it improved. I have NOT told you what went wrong: read the session below, and if more than one thing could be the problem, ask me which before you change anything.\n\n")
	}
	fmt.Fprintf(&b, "**Agent the session was with:** %s  (id: `%s`)\n", agent.Name, agent.ID)
	if d := strings.TrimSpace(agent.Description); d != "" {
		fmt.Fprintf(&b, "**What it's for:** %s\n", d)
	}
	// The fault is not always the agent: a broken tool looks like a
	// misbehaving agent from the chat. The candidates say where else it
	// could be, and choose_target is how Builder commits to one.
	if len(cands) > 0 {
		b.WriteString("\n" + triageBriefSection(cands))
	}
	b.WriteString("\nPlease:\n")
	b.WriteString("1. Pull the current definition of what you are fixing before changing anything: for the agent, agents(action=\"get\", full=true) for its prompt, rules and tools; for a tool, tool_def(action=\"get\"); for a pipeline or machine, its own get.\n")
	if reason != "" {
		b.WriteString("2. Find the behavior I described in the transcript below: the turns where it actually happened. If you cannot find it, say so rather than fixing something else.\n")
	} else {
		b.WriteString("2. Read the session transcript below and pinpoint where its behavior fell short of what I wanted: the spots where I had to correct, redirect, or repeat myself.\n")
	}
	b.WriteString("3. Say what went wrong and why: the part of its definition that produced it. If you are not sure, say what you would check.\n")
	b.WriteString("4. Propose specific changes (prompt wording, standing rules, tools, or knowledge) that would prevent the problem, and walk me through them before you apply anything.\n")
	b.WriteString("5. If the trouble was a tool, after I accept a change replay the call that went wrong with tool_def(action=\"test\") and its arguments from the transcript, and show me what it returns now. " +
		"A fix that does not change that result is not a fix.\n\n")

	// Builder is being asked to diagnose this agent, which it cannot do from
	// the calls alone. Owner-only either way: handleSendToBuilder is reached
	// from the workbench, and an agent somebody else owns is not one Builder
	// may edit.
	transcript := renderSessionMarkdown(agent, sess, forOwner)
	if len(transcript) > maxBriefTranscript {
		transcript = "_[Earlier turns omitted: showing the most recent part of the session.]_\n\n" +
			transcript[len(transcript)-maxBriefTranscript:]
	}
	// The transcript is another session's content — user text, the agent's
	// replies, tool output — landing in front of an agent holding
	// update/delete rights. Fence it so nothing inside can read as a
	// directive to Builder.
	b.WriteString(UntrustedData("session transcript to analyze", transcript))
	return b.String()
}

// (The agent-facing send_to_builder TOOL was removed: agents reach Builder by
// DIRECT dispatch — agents(action="run", agent="builder") — and iterate with it
// in-thread, rather than handing the user a one-click link into a separate
// session. The handlers above remain for the USER-initiated toolbar "Send to
// Builder" button, which loads a misbehaving agent's session into Builder to
// improve it — a different, button-driven flow.)
