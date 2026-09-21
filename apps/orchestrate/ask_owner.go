package orchestrate

// Asking the person whose agent this is for something only they can give.
//
// The recipient side of a share has had no voice. The manifest tells somebody
// "add a credential named wiki"; the collection guard tells them "ask alice to
// make you a contributor"; the reach panel tells the OWNER what is missing.
// Then nothing — they go and find the person on some other system, and the
// context of what actually failed stays behind in a chat they have left.
//
// The agent is the one thing in the room that knows all of it: whose agent it
// is, what it just could not reach, and what would fix it. So this is a tool
// rather than a form. The user says "ask her for access" and the request
// arrives with the detail already in it.
//
// Deliberately NOT a message channel. It writes one notice into the owner's
// inbox, folded like every other, and the owner's answer is the grant itself
// rather than a reply. A second direction would be a different feature with a
// different set of questions, and half of one is worse than none.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
)

// askOwnerCap is how many requests one person may send an owner about one
// agent in a day.
//
// A shared agent with a broken dependency fails the same way for everybody who
// runs it, and without a cap the owner's inbox becomes a stream of the same
// sentence from eight people — which is the surface they then stop reading.
// The fold handles repetition from one person; this handles persistence.
const askOwnerCap = 3

// askOwnerToolDef is offered only on somebody ELSE's agent. On your own there
// is nobody to ask: you are the person who would grant it.
func askOwnerToolDef(sess *ToolSession, asker, owner, agentID, agentName string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "ask_owner",
			Description: "Ask " + owner + ", who owns this agent, for something only they can grant — access to a document collection it cannot read, a tool it cannot reach, or a credential it needs. " +
				"Use it when you have hit one of those and the user wants it fixed; say what you could not do and what would fix it, because they cannot see this conversation. " +
				"It writes to their notifications and does not reach you back: their answer is the change itself, so tell the user to try again once it arrives rather than waiting here.",
			Parameters: map[string]ToolParam{
				"request": {Type: "string", Description: "What to ask for, in one or two sentences. Name the thing you could not reach."},
			},
			Required: []string{"request"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			req := strings.TrimSpace(oArgStr(args, "request"))
			if req == "" {
				return "", fmt.Errorf("request is required")
			}
			return askOwnerFor(asker, owner, agentID, agentName, req)
		},
	}
}

// askOwnerFor records the request, or says why it did not.
//
// Returns text the agent reports to the user, and an error only when nothing
// was written — so "you have asked enough today" reads as an answer rather
// than a failure the model tries to work around.
func askOwnerFor(asker, owner, agentID, agentName, request string) (string, error) {
	owner, asker = strings.TrimSpace(owner), strings.TrimSpace(asker)
	if owner == "" || asker == "" || owner == asker {
		return "", fmt.Errorf("there is nobody to ask: this agent is yours")
	}
	if RootDB == nil {
		return "", fmt.Errorf("no store for notifications")
	}
	if n := askOwnerCount(asker, owner, agentID); n >= askOwnerCap {
		// Said plainly, and as a RESULT rather than an error: the model
		// should tell the user to follow up another way, not retry.
		return "You have already asked " + owner + " about " + agentName +
			" a few times today, so this one was not sent. Tell the user to reach them another way.", nil
	}
	notices.Record(RootDB, notices.Notice{
		Owner: owner,
		Agent: agentID,
		// Blocked, not Report: somebody is waiting on them, and the thing
		// they can do about it is the point of the notice.
		Kind:  notices.KindBlocked,
		Title: asker + " needs something for \"" + agentName + "\"",
		Body: request + "\n\nAsked by " + asker + ", who is using your agent \"" + agentName +
			"\". They cannot grant this themselves. Sharing a tool, a collection or a credential is done from that thing's own page; what you change is what they get, with no reply needed here.",
	})
	askOwnerNote(asker, owner, agentID)
	Log("[orchestrate.ask_owner] %q asked %q about agent %s: %s", asker, owner, agentID, request)
	return "Sent to " + owner + ". They will see it in their notifications; there is no reply here, so tell the user to try again once it is granted.", nil
}

// askOwnerTable records what somebody has already sent, keyed by the three
// things that make one conversation: who asked, who they asked, and about what.
const askOwnerTable = "ask_owner_sent"

func askOwnerKey(asker, owner, agentID string) string {
	return asker + "\x00" + owner + "\x00" + agentID
}

// askOwnerCount is how many have been sent in the last day. Stored as the
// timestamps themselves rather than a counter with a reset, so there is no
// moment where a window rolls over and eight requests arrive at once.
func askOwnerCount(asker, owner, agentID string) int {
	if orchestrateBaseDB == nil {
		return 0
	}
	var sent []time.Time
	orchestrateBaseDB.Get(askOwnerTable, askOwnerKey(asker, owner, agentID), &sent)
	n := 0
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, at := range sent {
		if at.After(cutoff) {
			n++
		}
	}
	return n
}

func askOwnerNote(asker, owner, agentID string) {
	if orchestrateBaseDB == nil {
		return
	}
	key := askOwnerKey(asker, owner, agentID)
	var sent []time.Time
	orchestrateBaseDB.Get(askOwnerTable, key, &sent)
	cutoff := time.Now().Add(-24 * time.Hour)
	kept := []time.Time{}
	for _, at := range sent {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	orchestrateBaseDB.Set(askOwnerTable, key, append(kept, time.Now()))
}

// handleAskOwner is the button's half of the same request the tool files.
//
// Two doors, one function: the model reaches for the tool when it hits a wall,
// and a user who has already decided to ask presses a button rather than
// phrasing a sentence that makes a model choose a tool. Both land in
// askOwnerFor, so the cap, the wording and the fold cannot come apart.
func (T *OrchestrateApp) handleAskOwner(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if id == "" {
		http.Error(w, "agent_id is required", http.StatusBadRequest)
		return
	}
	// Resolved the way a run resolves it, so the button reaches exactly the
	// agents this user may actually open — their own (where there is nobody to
	// ask) and the ones shared with them.
	a, found := findAgentByNameOrID(udb, user, id)
	if !found {
		http.NotFound(w, r)
		return
	}
	T.askOwnerRequest(w, r, user, a)
}

// askOwnerRequest is the body both doors share, once the agent is resolved.
func (T *OrchestrateApp) askOwnerRequest(w http.ResponseWriter, r *http.Request, user string, a AgentRecord) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Request string `json:"request"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Request) == "" {
		http.Error(w, "say what you need", http.StatusBadRequest)
		return
	}
	out, err := askOwnerFor(user, a.Owner, a.ID, chFirst(a.Name, a.ID), body.Request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"result": out})
}

// hasSomeoneElsesAgent reports whether this user's picker can land on an agent
// they do not own.
//
// The toolbar is built once and the agent is chosen client-side, so the entry
// cannot appear and disappear per selection. A user with nothing shared to them
// would otherwise carry a permanent button whose only possible answer is "this
// agent is yours" — so it is dropped for them entirely, and the endpoint still
// refuses on its own for anybody who reaches it another way.
func hasSomeoneElsesAgent(agents []AgentRecord, user string) bool {
	user = strings.TrimSpace(user)
	if user == "" {
		return false
	}
	for _, a := range agents {
		// A seed belongs to the framework: there is no person behind it.
		if a.Owner != "" && a.Owner != user && !isSeedID(a.ID) {
			return true
		}
	}
	return false
}

// PublicHandleAskOwner is the restricted /agents/<slug> surface's door onto the
// same request.
//
// It takes the resolved record rather than an id because the caller has already
// done the reaching: apps/agents matched the slug and ran the access gate, and
// re-deriving any of that here would be a second, differently-worded answer to
// a question already settled. This is also the surface where the ask MATTERS —
// a recipient of a shared agent lives here, not in the workbench.
func (T *OrchestrateApp) PublicHandleAskOwner(w http.ResponseWriter, r *http.Request, agent AgentRecord) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.askOwnerRequest(w, r, user, agent)
}

// OwnerLabel is how to refer to an agent's owner in front of somebody running
// it, which is not always by name.
//
// A peer share names them: they know who handed them the agent, and "ask alice"
// is the actionable form. A PUBLISHED agent does not. It reaches every signed-in
// user, none of whom was told whose it is, and an account here is an email
// address — so naming the owner on that surface publishes their address to the
// whole deployment as a side effect of a refusal.
//
// Empty user, or the owner themselves, gets the name: there is nobody it could
// be disclosed to.
func OwnerLabel(agent AgentRecord, user string) string {
	owner := strings.TrimSpace(agent.Owner)
	if owner == "" || owner == seedOwner {
		return "its owner"
	}
	user = strings.TrimSpace(user)
	if user == "" || user == owner || containsString(agent.AllowedUsers, user) {
		return owner
	}
	return "its owner"
}
