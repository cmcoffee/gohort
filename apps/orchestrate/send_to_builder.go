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
// The brief asks for the failing case BEFORE the fix, which is the whole
// difference between improving an agent and believing you did. A correction
// the user made by hand is already the case: the message that produced the bad
// turn is the prompt, and what they corrected it to is the assertion. Written
// first, it fails; written afterwards, it passes the moment it is created and
// proves nothing. The eval tool (eval_tool.go) is what makes that reachable
// from here — before it, this handoff ended at a proposal and nothing ever
// checked whether the proposal worked.
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
	brief := builderBriefRecord{
		ID:            UUIDv4(),
		Text:          buildBuilderBrief(agent, sess, body.Reason, exportForOwner(agent, user)),
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
//
// reason is what the USER said is wrong, asked for at the button. It leads
// the brief, because the alternative is Builder inferring a reason from a
// transcript it has already been told contains a failure — which produces a
// confident diagnosis of whichever problem it noticed first, and that is not
// reliably the one the user cared about. An empty reason is stated as absent
// rather than papered over, so Builder asks instead of guessing.
func buildBuilderBrief(agent AgentRecord, sess ChatSession, reason string, forOwner bool) string {
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
	fmt.Fprintf(&b, "**Agent to improve:** %s  (id: `%s`)\n", agent.Name, agent.ID)
	if d := strings.TrimSpace(agent.Description); d != "" {
		fmt.Fprintf(&b, "**What it's for:** %s\n", d)
	}
	b.WriteString("\nPlease:\n")
	b.WriteString("1. Pull this agent's current configuration (agents tool, action \"get\", full true) so you can see its prompt, rules, and tools before changing anything.\n")
	if reason != "" {
		b.WriteString("2. Find the behavior I described in the transcript below: the turns where it actually happened. If you cannot find it, say so rather than fixing something else.\n")
	} else {
		b.WriteString("2. Read the session transcript below and pinpoint where its behavior fell short of what I wanted: the spots where I had to correct, redirect, or repeat myself.\n")
	}
	b.WriteString("3. Write the failing case FIRST. Turn the correction into an eval case: the message that produced the bad turn is the prompt, and what I corrected it TO is the assertion. " +
		"Use eval(action=\"list\") to find a suite that grades this agent and eval(action=\"add_case\", ...) to add it; if there is no suite yet, eval(action=\"create_suite\", target_kind=\"agent\", target=\"" + agent.ID + "\", ...).\n")
	b.WriteString("4. Run that suite now, BEFORE you change anything: eval(action=\"run\", suite=\"<name>\", note=\"before\"). Suites run with tools stubbed, so nothing external happens. " +
		"The case you just wrote should FAIL. If it passes, the case does not capture the problem: fix the case rather than the agent, or the score will say a bug is gone that never left.\n")
	b.WriteString("5. Propose specific changes (prompt wording, standing rules, tools, or knowledge) that would prevent the problem, and walk me through them before you apply anything.\n")
	b.WriteString("6. After I accept a change, run the suite again with a note saying what you changed, and tell me BOTH scores. " +
		"A fix that does not move the number is not a fix, and a fix that moves this case while breaking another is worth knowing about before I find out in production.\n\n")

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
