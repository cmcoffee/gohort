package orchestrate

// An approval nobody answered in time.
//
// The card used to wait five minutes with the turn parked behind it, then deny
// the call and let the turn go on: the agent was told the call was refused,
// which it reads as the owner saying no, and the answer, when it came, had
// nowhere to go. Now a card that times out is kept: the turn ends saying it is
// waiting, the card stays in the conversation with its buttons live (a reload
// or a restart draws it again), and answering it later wakes the agent in that
// conversation to carry on. An approval comes with a pass for exactly that
// call, used once, so the woken turn is not asked the same question again.

import (
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// pendingApprovalTable keeps the cards waiting on an answer, by card id.
const pendingApprovalTable = "pending_call_approvals"

// approvalWakeMarker opens the note a woken turn carries. It is a wake like a
// finished background task's (isTaskWake), not a scheduled fire.
const approvalWakeMarker = "[APPROVAL ANSWERED]"

// pendingApprovalTTL is how long a kept card stays answerable, and a pass the
// answer grants stays usable.
const pendingApprovalTTL = 7 * 24 * time.Hour

// pendingApproval is one call waiting on its owner's answer.
type pendingApproval struct {
	ID        string    `json:"id"`
	Owner     string    `json:"owner"`
	AgentID   string    `json:"agent_id"`
	SessionID string    `json:"session_id"`
	Tool      string    `json:"tool"`
	Args      string    `json:"args"` // the call's arguments as the loop handed them to Confirm
	Prompt    string    `json:"prompt"`
	Detail    string    `json:"detail"`
	Created   time.Time `json:"created"`
	Answer    string    `json:"answer,omitempty"` // "", "allow" or "deny"
}

// keepForLater turns a card that timed out into one that waits: the record,
// the card drawn in its place (live, and saved with the turn), and what the
// model is told instead of a denial. False when this turn has no conversation
// to keep a card in, or the question is not about one call; the caller then
// denies as before.
func (t *chatTurn) keepForLater(req toolConfirmRequest, oldCardID string) bool {
	if t == nil || t.sse == nil || t.session == nil || RootDB == nil || strings.TrimSpace(req.args) == "" {
		return false
	}
	p := pendingApproval{
		ID: "approval-" + UUIDv4()[:12], Owner: t.user, AgentID: t.agent.ID, SessionID: t.session.ID,
		Tool: req.tool, Args: req.args, Prompt: req.prompt, Detail: req.detail, Created: time.Now(),
	}
	RootDB.Set(pendingApprovalTable, p.ID, p)
	t.sendConfirmResolvedLabel(oldCardID, "deny", "Not answered yet: kept below")
	block := p.block()
	t.sse.Send(block)
	t.bubblesMu.Lock()
	t.midTurnBubbles = append(t.midTurnBubbles, ChatMessage{
		Role: "assistant", Content: "Waiting for your approval: " + p.Prompt, Created: time.Now(), Block: block,
	})
	t.bubblesMu.Unlock()
	t.waitingRefusal = "NOT RUN YET: the owner has not answered the approval for this " + req.tool + " call. " +
		"It is kept in the conversation as a card they can answer later, and when they do you will be woken to carry on. " +
		"End your turn now with one short line saying you are waiting for their approval of " + req.tool +
		". Do not try to get the same result another way."
	t.recordApproval(req, "Not answered yet: kept for later")
	return true
}

// confirmRefusal is the agent loop's ConfirmRefusal: what the model is told
// when the last no was a card kept for later rather than a person saying no.
// Taken once.
func (t *chatTurn) confirmRefusal(string) string {
	if t == nil {
		return ""
	}
	t.bubblesMu.Lock()
	defer t.bubblesMu.Unlock()
	why := t.waitingRefusal
	t.waitingRefusal = ""
	return why
}

// block is the card as the page draws it (the "kept_approval" renderer).
func (p pendingApproval) block() map[string]any {
	return map[string]any{
		"kind": "block", "type": "kept_approval", "id": p.ID,
		"prompt": p.Prompt, "detail": p.Detail, "answer": p.Answer,
		// Answerable until answered, whatever is written after it.
		"keep_open": true,
	}
}

// --- a pass for exactly the approved call ------------------------------------

var resumePasses = struct {
	sync.Mutex
	until map[string]time.Time
}{until: map[string]time.Time{}}

func resumePassKey(owner, agentID, tool, args string) string {
	return owner + "\x00" + agentID + "\x00" + tool + "\x00" + strings.TrimSpace(args)
}

func grantResumePass(p pendingApproval) {
	resumePasses.Lock()
	defer resumePasses.Unlock()
	resumePasses.until[resumePassKey(p.Owner, p.AgentID, p.Tool, p.Args)] = time.Now().Add(pendingApprovalTTL)
}

// takeResumePass reports whether this exact call was approved on a kept card
// and not yet made, and spends the pass if so.
func takeResumePass(owner, agentID, tool, args string) bool {
	resumePasses.Lock()
	defer resumePasses.Unlock()
	k := resumePassKey(owner, agentID, tool, args)
	exp, ok := resumePasses.until[k]
	if !ok {
		return false
	}
	delete(resumePasses.until, k)
	return time.Now().Before(exp)
}

// --- answering a kept card ---------------------------------------------------

// handleApprovalAnswer is POST /api/approvals/answer {id, answer: allow|deny}.
func (T *OrchestrateApp) handleApprovalAnswer(c *jsonapi.Call, body struct {
	ID     string `json:"id"`
	Answer string `json:"answer"`
}) (any, error) {
	user := c.User
	if body.Answer != "allow" && body.Answer != "deny" {
		return nil, jsonapi.BadRequest("send id and answer (allow or deny)")
	}
	p, err := T.answerPendingApproval(user, strings.TrimSpace(body.ID), body.Answer)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"ok": true, "answer": p.Answer}, nil
}

// answerPendingApproval records the answer and wakes the agent in the
// conversation the card is in.
func (T *OrchestrateApp) answerPendingApproval(user, id, answer string) (pendingApproval, error) {
	var p pendingApproval
	if RootDB == nil || id == "" || !RootDB.Get(pendingApprovalTable, id, &p) || p.Owner != user {
		return p, Error("no such approval waiting")
	}
	if p.Answer != "" {
		return p, Error("this was already answered")
	}
	if time.Since(p.Created) > pendingApprovalTTL {
		RootDB.Unset(pendingApprovalTable, id)
		return p, Error("this approval expired; ask the agent again")
	}
	p.Answer = answer
	RootDB.Set(pendingApprovalTable, id, p)
	T.stampKeptCard(p)
	args := strings.ReplaceAll(strings.TrimSpace(p.Args), "\n", "; ")
	var note wakeNote
	if answer == "allow" {
		grantResumePass(p)
		note = wakeNote{
			prompt: approvalWakeMarker + " The owner APPROVED the call you were waiting on: " + p.Tool + " (" + args + "). " +
				"Make that call now with exactly those arguments, then finish what you were doing for them.",
			history: "The owner approved " + p.Tool + " (" + args + ").",
		}
	} else {
		note = wakeNote{
			prompt: approvalWakeMarker + " The owner DENIED the call you were waiting on: " + p.Tool + " (" + args + "). " +
				"Do not make it, and do not get the same result another way. Tell them in a line what that leaves undone.",
			history: "The owner denied " + p.Tool + " (" + args + ").",
		}
	}
	Log("[orchestrate.approval] %s answered %q on %s (%s) for agent %s", user, answer, id, p.Tool, p.AgentID)
	go func() {
		if err := T.wakeSessionWithNote(taskOrigin{SessionID: p.SessionID, User: p.Owner, AgentID: p.AgentID}, note); err != nil {
			Warn("[orchestrate.approval] could not wake session %s after %s: %v", p.SessionID, id, err)
		}
	}()
	return p, nil
}

// stampKeptCard writes the answer onto the card saved in the conversation, so
// a page drawing it again shows it answered, not waiting. Done before the
// woken turn loads the conversation, so that turn's save carries it too.
func (T *OrchestrateApp) stampKeptCard(p pendingApproval) {
	udb := UserDB(T.DB, p.Owner)
	if udb == nil {
		return
	}
	sess, ok := loadChatSession(udb, p.AgentID, p.SessionID)
	if !ok {
		return
	}
	for i := range sess.Messages {
		if b := sess.Messages[i].Block; b != nil && b["type"] == "kept_approval" && b["id"] == p.ID {
			sess.Messages[i].Block = p.block()
			_, _ = saveChatSession(udb, sess)
			return
		}
	}
}
