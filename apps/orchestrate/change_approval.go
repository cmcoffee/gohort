package orchestrate

// Changes to what already exists wait for the owner.
//
// Builder (and any agent allowed to author) changes the owner's things: their
// tools, apps, skills, pipelines, machines, agents. A tool could be Locked to
// stop that, which protected only what somebody remembered to lock, and said
// nothing at the moment a change was about to happen. Now nothing that
// already exists changes without the owner saying so, each time, where the
// work was asked for:
//
//   - something made in this session is the session's own work, and its
//     author changes it freely;
//   - the first change to anything else asks the owner in the chat, with
//     what is about to change, Approve or Deny. Approve covers that one item
//     for the rest of the conversation when the owner is the one asking, and
//     for the rest of the job when another agent sent the work
//     (resetChangeApprovals): the next job asks again;
//   - with nobody watching (a schedule, a job another agent sent), the change
//     is not made: it waits as a card in the conversation it came from, and
//     on the Requests tab, with a bell notice. Approving it runs the agent
//     again to make that one change (runApprovedChange, withApprovedChange),
//     with the result delivered back to that conversation. Nothing is
//     remembered past that run;
//   - the one thing the owner picked in Builder's form ("Change" or "Fix",
//     then "Which one?") is already approved in that session: picking it is
//     the owner saying so. Only changes: deleting it still asks. Only that
//     item: anything else Builder reaches for asks as before. And only
//     Builder the owner opened: Builder another agent sent asks every time.
//
// No approval stands: there is no Always allow for a change, no grant that
// outlasts the run it was given for, and approving a request to run an agent
// is not approving the changes it goes on to make. Who may send work to
// Builder is a setting of the agent (Can dispatch Builder), and Builder is
// offered only in a conversation somebody is watching (canDispatchBuilder),
// so its changes come back to the person who asked.
//
// Running or testing never asks. Only writes do, and only to what existed.

import (
	"context"
	"fmt"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
)

// changeApprovalAction is the approval-queue entry for a change made with
// nobody watching.
const changeApprovalAction = "approve_change"

// changeApprovalSet is what one session may change without asking again:
// what it made, and what the owner approved in it. named is what the owner
// picked in Builder's form, which covers changes but not deletion, so it is
// kept apart from items. made is what the session made, the only thing it
// deletes without asking: an approved change is not an approved deletion.
type changeApprovalSet struct {
	mu    sync.Mutex
	items map[string]bool
	named map[string]bool
	made  map[string]bool
}

// has reports whether the session may make op to the item without asking.
func (s *changeApprovalSet) has(key string, op ChangeOp) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if op == ChangeDelete {
		return s.made[key]
	}
	return s.items[key]
}

// madeHere records an item the session made.
func (s *changeApprovalSet) madeHere(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[key], s.made[key] = true, true
}

// isNamed reports whether the owner picked this item in Builder's form.
func (s *changeApprovalSet) isNamed(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.named[key]
}

// covers reports whether this session made, was approved for, or was asked
// by name to work on the item: what Builder may run to test it.
func (s *changeApprovalSet) covers(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items[key] || s.named[key]
}

// names is the item names of one kind the set covers ("app" -> its slugs).
func (s *changeApprovalSet) names(kind string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := changeKey(kind, "")
	seen := map[string]bool{}
	var out []string
	for _, m := range []map[string]bool{s.items, s.named} {
		for k, on := range m {
			if on && strings.HasPrefix(k, prefix) && !seen[k] {
				seen[k] = true
				out = append(out, strings.TrimPrefix(k, prefix))
			}
		}
	}
	return out
}

// name records an item the owner picked in Builder's form.
func (s *changeApprovalSet) name(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.named[key] = true
}

func (s *changeApprovalSet) add(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[key] = true
}

func newChangeApprovalSet() *changeApprovalSet {
	return &changeApprovalSet{items: map[string]bool{}, named: map[string]bool{}, made: map[string]bool{}}
}

// sessionChangeApprovals is per owner and session. In memory: a restart asks
// again, which is the safe direction.
var sessionChangeApprovals sync.Map // owner\x00session -> *changeApprovalSet

func changeApprovalsFor(owner, session string) *changeApprovalSet {
	v, _ := sessionChangeApprovals.LoadOrStore(owner+"\x00"+session, newChangeApprovalSet())
	return v.(*changeApprovalSet)
}

// changeOpFor is the op for a write to something that existed: a deletion,
// or a change.
func changeOpFor(deletes bool) ChangeOp {
	if deletes {
		return ChangeDelete
	}
	return ChangeEdit
}

// editOrNew is the op for a write that changes the item when it existed and
// makes it when it did not.
func editOrNew(existed bool) ChangeOp {
	if existed {
		return ChangeEdit
	}
	return ChangeNew
}

// changeKey names one item. An agent is named by its id: two agents can share
// a name, and keyed by name, approving (or making) one "Report Writer" let the
// session change or delete a different one without asking.
func changeKey(kind, name string) string {
	return strings.ToLower(strings.TrimSpace(kind)) + ":" + strings.TrimSpace(name)
}

type approvedChangeKey struct{}

// approvedChange is one change the owner approved from a card left waiting:
// the agent that asked for it and the item.
type approvedChange struct{ agentID, key string }

// withApprovedChange marks the run that makes a change the owner approved
// from a card left waiting: agentID may make that one change to key, of
// whatever kind it was (a deletion included), in this run alone. An agent the
// run dispatches does not inherit it, and nothing is stored.
func withApprovedChange(ctx context.Context, agentID, key string) context.Context {
	return context.WithValue(ctx, approvedChangeKey{}, approvedChange{agentID, key})
}

// approvedChangeFor reports whether ctx is the run the owner approved for
// agentID to change key.
func approvedChangeFor(ctx context.Context, agentID, key string) bool {
	if ctx == nil || strings.TrimSpace(agentID) == "" || key == "" {
		return false
	}
	a, _ := ctx.Value(approvedChangeKey{}).(approvedChange)
	return a.agentID == agentID && a.key == key
}

// resetChangeApprovals starts a new job in session: what an earlier job there
// was approved for, or made, is asked about again. Called where another agent
// sends work, whose thread (dispatchThreadID) outlasts the job.
func resetChangeApprovals(owner, session string) {
	sessionChangeApprovals.Delete(owner + "\x00" + session)
}

// changeAsker is everything one approval needs: whose things these are, the
// agent making the change, the session an approval lasts for, the store the
// queue's grants live in, and the card that reaches the owner live (nil when
// nobody is watching: the change queues). sess, when there is one, says where
// the queue entry was asked from.
type changeAsker struct {
	owner, agentID, agentName string
	session                   string
	db                        Database
	sentBy                    string
	ask                       endedAsk
	sess                      *ToolSession
	// ctx is the run's, for the one change the owner approved from a card
	// left waiting (withApprovedChange).
	ctx context.Context
}

// changeApprover builds a session's ApproveChange, with a card whose every
// no reads as the owner's (changeApproverEnded tells them apart).
func changeApprover(sess *ToolSession, owner, agentID, agentName string, ask func(prompt, detail string, yes []string) string) func(kind, name, detail string, op ChangeOp) string {
	return changeApproverEnded(sess, owner, agentID, agentName, saidNo(ask))
}

// saidNo is ask as an endedAsk that reads every no as the person's. Nil for
// nil.
func saidNo(ask func(prompt, detail string, yes []string) string) endedAsk {
	if ask == nil {
		return nil
	}
	return func(prompt, detail string, yes []string) (string, askEnd) {
		return ask(prompt, detail, yes), askNo
	}
}

// changeApproverEnded builds a session's ApproveChange from a card that says
// how it ended, so a refusal says whether the owner said no or never
// answered.
func changeApproverEnded(sess *ToolSession, owner, agentID, agentName string, ask endedAsk) func(kind, name, detail string, op ChangeOp) string {
	return func(kind, name, detail string, op ChangeOp) string {
		if sess == nil {
			return ""
		}
		return changeAsker{owner: owner, agentID: agentID, agentName: agentName, session: sess.ChatSessionID,
			db: sess.DB, sentBy: sess.DispatchParentAgentID, ask: ask, sess: sess,
			ctx: sess.Context()}.approve(kind, name, detail, op)
	}
}

// approveChange is the same question for the authoring tools that run on the
// turn itself (app_def, pipeline, machine).
func (t *chatTurn) approveChange(kind, name, detail string, op ChangeOp) string {
	if t == nil {
		return ""
	}
	ask := saidNo(t.changeAsk)
	if ask == nil {
		ask = t.endedAsker() // this chat's card, or the one of the chat that started this run
	}
	return changeAsker{owner: t.user, agentID: t.agent.ID, agentName: t.agent.Name, session: t.changeSessionID(),
		db: t.udb, sentBy: t.sentBy, ask: ask, ctx: t.ctx}.approve(kind, name, detail, op)
}

// changeSessionID is the session this turn's change approvals last for: its
// own, or for a handed-over run with none, the thread it works in.
func (t *chatTurn) changeSessionID() string {
	if id := t.chatSessionID(); id != "" {
		return id
	}
	return t.changeThread
}

// approve is "" when the change may go ahead, or the refusal to return.
func (c changeAsker) approve(kind, name, detail string, op ChangeOp) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	agentName := c.agentName
	if strings.TrimSpace(agentName) == "" {
		agentName = c.agentID
	}
	key := changeKey(kind, name)
	// A run with no session to last for remembers nothing: one approval
	// covers the one change. Kept under a blank session, it covered every
	// other such run's too.
	set := newChangeApprovalSet()
	if strings.TrimSpace(c.session) != "" {
		set = changeApprovalsFor(c.owner, c.session)
	}
	if op == ChangeNew {
		// Made here: the session's own work.
		set.madeHere(key)
		return ""
	}
	if set.has(key, op) {
		return ""
	}
	// Picked in Builder's form by the owner: approved, for a change. Builder
	// another agent sent did not get it from the owner, so it asks.
	if op == ChangeEdit && strings.TrimSpace(c.sentBy) == "" && set.isNamed(key) {
		return ""
	}
	// The one change the owner approved from a card left waiting, in the
	// run that makes it.
	if approvedChangeFor(c.ctx, c.agentID, key) {
		set.add(key)
		return ""
	}
	what := strings.TrimSpace(kind) + " " + changeItemLabel(c.db, kind, name)
	if c.ask != nil {
		prompt := fmt.Sprintf("Let %s change the %s?", agentName, what)
		body := strings.TrimSpace(detail)
		if body == "" {
			body = "Change it."
		}
		lasts := "the rest of this conversation"
		if strings.TrimSpace(c.sentBy) != "" {
			lasts = "the rest of this job: the next job asks again"
		}
		body += "\n\nApprove covers changes to this " + strings.TrimSpace(kind) + " for " + lasts + "."
		label, end := c.ask(prompt, body, []string{"Approve"})
		if label != "" {
			set.add(key)
			return ""
		}
		return changeRefusal(what, end)
	}
	then := "once they approve it, it is made in a fresh run"
	if queueChangeApproval(c, kind, name, detail) {
		then = "once they approve it, it is made in a fresh run and the result comes back to this conversation"
	}
	return fmt.Sprintf("NOT DONE: changing the %s needs the owner's approval, and nobody is here to give it. It is waiting in their approval queue; %s. Do not make it another way, and do not recreate it under a different name.", what, then)
}

// sessionApprove is the change question for a tool that holds only the
// session: "" when the change may go ahead, the refusal otherwise. A session
// with no approver (a test, an assembly with no turn) asks nothing.
// ChangeNew marks something just made as this session's own.
func sessionApprove(sess *ToolSession, kind, name, detail string, op ChangeOp) string {
	if sess == nil || sess.ApproveChange == nil {
		return ""
	}
	return sess.ApproveChange(kind, name, detail, op)
}

// queueChangeApproval puts a change made with nobody watching in the owner's
// approval queue, once: a run that tries again before they answer does not
// add a second entry. Reports whether approving it brings the result back to
// a conversation (it recorded one to come back to).
func queueChangeApproval(c changeAsker, kind, name, detail string) bool {
	key := changeKey(kind, name)
	for _, a := range ListAuthorizations(RootDB, c.owner) {
		if a.Action == changeApprovalAction && a.Agent == c.agentID && a.Handle == key {
			return a.FromSession != ""
		}
	}
	brief := fmt.Sprintf("Change the %s %s.", strings.TrimSpace(kind), changeItemLabel(c.db, kind, name))
	if d := strings.TrimSpace(detail); d != "" {
		brief += " " + d
	}
	a := Authorization{
		Owner:     c.owner,
		Action:    changeApprovalAction,
		Agent:     c.agentID,
		FromAgent: chFirst(strings.TrimSpace(c.sentBy), c.agentID), // shows where it was asked
		Handle:    key,                                             // the item, matched when it is approved
		Brief:     brief,
	}
	if c.sess != nil {
		a.Text = strings.TrimSpace(c.sess.IntentText) // what the re-run finishes
	}
	stampRequestOrigin(&a, c.sess)
	SaveAuthorization(RootDB, a)
	Log("[orchestrate.change] %s: %s wants to change %s, queued for approval", c.owner, c.agentID, key)
	// The bell: the work stopped on the owner, somewhere they may not be
	// looking.
	where := "on the Requests tab."
	if a.FromSession != "" {
		where = "in the conversation it came from, or on the Requests tab."
	}
	agentName := chFirst(strings.TrimSpace(c.agentName), c.agentID)
	notices.Record(RootDB, notices.Notice{
		Owner: c.owner, Agent: c.agentID, Kind: notices.KindBlocked,
		Title: agentName + " is waiting for you to approve a change",
		Body:  brief + "\n\nNothing was changed. Approve or deny it " + where,
	})
	return a.FromSession != ""
}

// runApprovedChange runs the agent that wanted a change the owner just
// approved, to make that one change now (withApprovedChange), with the result
// back in the conversation it came from when there is one. The approval is
// spent on that run: nothing is kept for a later one.
//
// Approval used to grant the agent the item for a day, and run nothing
// without a conversation to come back to: a standing permission given by a
// one-off click, and a change that sat approved and unmade.
func (T *OrchestrateApp) runApprovedChange(a Authorization) {
	if strings.TrimSpace(a.Agent) == "" || strings.TrimSpace(a.Handle) == "" {
		return
	}
	brief := "The owner approved a change you were held from making earlier, with nobody there to approve it: " + strings.TrimSpace(a.Brief) + " Make that change now and check that it took."
	if work := strings.TrimSpace(a.Text); work != "" {
		brief += " It was part of the work below: finish what the change was holding up, and nothing that was already done.\n\n" + truncateObs(work, 2000)
	}
	name := T.delegationTargetName(a.Owner, a.Agent)
	T.runApproved(approvedRun{
		a: a, target: a.Agent, brief: brief, change: &approvedChange{a.Agent, a.Handle},
		label: fmt.Sprintf("the change %s queued for the user's approval, which they approved (%s)", name, truncateObs(a.Brief, 160)),
	})
}

// changeItemLabel is how a change names its item to a person: an agent by its
// name, since it is asked about by id.
func changeItemLabel(db Database, kind, name string) string {
	if strings.EqualFold(strings.TrimSpace(kind), "agent") && db != nil {
		if a, ok := loadAgent(db, strings.TrimSpace(name)); ok && strings.TrimSpace(a.Name) != "" {
			return a.Name
		}
	}
	return name
}

// intakeChangeKeys is the item the owner picked in Builder's form, as the
// keys a change to it is asked under, or nil when the form did not pick one
// (Create, or a kind with no "Which one?"). The values are the form's own
// (builtin/builder.md: action, kind, target, listed by authoringTargets), so
// each kind maps to the name its authoring tool asks with: an agent's value
// carries its id, and a pipeline can be a pipeline tool, written by tool_def.
// An API is a credential, which no change question covers.
func intakeChangeKeys(values map[string]string) []string {
	switch strings.ToLower(strings.TrimSpace(values["action"])) {
	case "change", "fix":
	default:
		return nil
	}
	target := strings.TrimSpace(values["target"])
	if target == "" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(values["kind"])) {
	case "agent":
		name, id := target, ""
		if i := strings.LastIndex(target, " (id: "); i >= 0 && strings.HasSuffix(target, ")") {
			name, id = strings.TrimSpace(target[:i]), strings.TrimSpace(target[i+len(" (id: "):len(target)-1])
		}
		// By id, as the change is asked (changeKey); the name only when the
		// form gave no id, which then matches no agent change and asks.
		if id != "" {
			return []string{changeKey("agent", id)}
		}
		return []string{changeKey("agent", name)}
	case "tool":
		return []string{changeKey("tool", target)}
	case "pipeline":
		return []string{changeKey("pipeline", target), changeKey("tool", target)}
	case "app":
		return []string{changeKey("app", target)}
	case "machine":
		return []string{changeKey("machine", target)}
	case "skill":
		return []string{changeKey("skill", target)}
	case "connection":
		return []string{changeKey("bridge", target)}
	}
	return nil
}

// noteIntakeTargets marks what the owner picked in this Builder session's
// form as approved for changes in it. Read from the stored form values every
// turn, so a restart keeps it; never from the message text, which anything
// reaching the conversation could imitate. A session another agent sent has
// no form values, and its approver asks regardless (approve, sentBy).
func (t *chatTurn) noteIntakeTargets() {
	if t == nil || t.session == nil || !isBuilderAgent(t.agent.ID) {
		return
	}
	var set *changeApprovalSet
	for _, m := range t.session.Messages {
		if m.Role != "user" || len(m.IntakeValues) == 0 {
			continue
		}
		for _, key := range intakeChangeKeys(m.IntakeValues) {
			if set == nil {
				set = changeApprovalsFor(t.user, t.chatSessionID())
			}
			set.name(key)
		}
	}
}

// changeRefusal is what the agent is told when the owner's card for a change
// to what ended without a yes, saying how it ended: told as a decline only
// when the owner said no.
func changeRefusal(what string, end askEnd) string {
	const hold = " Leave it as it is: do not make the change another way, and do not recreate it under a different name."
	switch end {
	case askUnanswered:
		return "NOT DONE: the owner did not answer the approval for this change to the " + what + " in time, so it was not made." + hold + " Tell them what you would change and why; when they are back, it can be asked again."
	case askNobody:
		return "NOT DONE: nobody was there to answer the approval for this change to the " + what + ", so it was not made." + hold + " Tell them what you would change and why."
	case askUnknown:
		return "NOT DONE: the owner did not approve this change to the " + what + ": they said no, or did not answer in time." + hold + " Tell them what you would have changed and why, and ask how they want to proceed."
	}
	return "NOT DONE: the owner declined this change to the " + what + "." + hold + " Tell them what you would have changed and why, and ask how they want to proceed."
}
