package orchestrate

// Changes to what already exists wait for the owner.
//
// Builder (and any agent allowed to author) changes the owner's things: their
// tools, apps, skills, pipelines, machines, agents. A tool could be Locked to
// stop that, which protected only what somebody remembered to lock, and said
// nothing at the moment a change was about to happen. Now nothing that
// already exists changes without the owner saying so:
//
//   - something made in this session is the session's own work, and its
//     author changes it freely;
//   - the first change to anything else asks the owner in the chat, with
//     what is about to change, Approve or Deny. Approve covers that one item
//     for the rest of the session, so iterating on it asks once;
//   - with nobody watching (a schedule, a job another agent sent), the change
//     is not made: it waits in the owner's approval queue, and approving it
//     lets that agent make it in its next run (for a day);
//   - the one thing the owner picked in Builder's form ("Change" or "Fix",
//     then "Which one?") is already approved in that session: picking it is
//     the owner saying so. Only changes: deleting it still asks. Only that
//     item: anything else Builder reaches for asks as before. And only
//     Builder the owner opened: Builder another agent sent asks every time.
//
// Running or testing never asks. Only writes do, and only to what existed.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// changeApprovalAction is the approval-queue entry for a change made with
// nobody watching.
const changeApprovalAction = "approve_change"

// changeGrantTable holds what the owner approved from the queue, per agent
// and item, until it expires.
const changeGrantTable = "change_grants"

// changeGrantTTL is how long an approval from the queue lasts: long enough for
// the agent's next run, not a standing permission.
const changeGrantTTL = 24 * time.Hour

type changeGrant struct {
	Until time.Time `json:"until"`
}

// changeApprovalSet is what one session may change without asking again:
// what it made, and what the owner approved in it. named is what the owner
// picked in Builder's form, which covers changes but not deletion, so it is
// kept apart from items.
type changeApprovalSet struct {
	mu    sync.Mutex
	items map[string]bool
	named map[string]bool
}

func (s *changeApprovalSet) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items[key]
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

// sessionChangeApprovals is per owner and session. In memory: a restart asks
// again, which is the safe direction.
var sessionChangeApprovals sync.Map // owner\x00session -> *changeApprovalSet

func changeApprovalsFor(owner, session string) *changeApprovalSet {
	v, _ := sessionChangeApprovals.LoadOrStore(owner+"\x00"+session, &changeApprovalSet{items: map[string]bool{}, named: map[string]bool{}})
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

// changeKey names one item.
func changeKey(kind, name string) string {
	return strings.ToLower(strings.TrimSpace(kind)) + ":" + strings.TrimSpace(name)
}

func changeGrantKey(agentID, key string) string { return agentID + "\x00" + key }

// changeGranted reports whether the owner approved this agent's change to
// this item from the queue, and it has not expired.
func changeGranted(db Database, agentID, key string) bool {
	if db == nil {
		return false
	}
	var g changeGrant
	if !db.Get(changeGrantTable, changeGrantKey(agentID, key), &g) {
		return false
	}
	return time.Now().Before(g.Until)
}

// grantChange records an approval from the queue.
func grantChange(db Database, agentID, key string) {
	if db != nil {
		db.Set(changeGrantTable, changeGrantKey(agentID, key), changeGrant{Until: time.Now().Add(changeGrantTTL)})
	}
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
	ask                       func(prompt, detail string, yes []string) string
	sess                      *ToolSession
}

// changeApprover builds a session's ApproveChange.
func changeApprover(sess *ToolSession, owner, agentID, agentName string, ask func(prompt, detail string, yes []string) string) func(kind, name, detail string, op ChangeOp) string {
	return func(kind, name, detail string, op ChangeOp) string {
		if sess == nil {
			return ""
		}
		return changeAsker{owner: owner, agentID: agentID, agentName: agentName, session: sess.ChatSessionID,
			db: sess.DB, sentBy: sess.DispatchParentAgentID, ask: ask, sess: sess}.approve(kind, name, detail, op)
	}
}

// approveChange is the same question for the authoring tools that run on the
// turn itself (app_def, pipeline, machine).
func (t *chatTurn) approveChange(kind, name, detail string, op ChangeOp) string {
	if t == nil {
		return ""
	}
	ask := t.changeAsk
	if ask == nil {
		ask = t.chatAsker() // this chat's card, or the one of the chat that started this run
	}
	return changeAsker{owner: t.user, agentID: t.agent.ID, agentName: t.agent.Name, session: t.chatSessionID(),
		db: t.udb, ask: ask}.approve(kind, name, detail, op)
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
	set := changeApprovalsFor(c.owner, c.session)
	if op == ChangeNew {
		// Made here: the session's own work.
		set.add(key)
		return ""
	}
	if set.has(key) {
		return ""
	}
	// Picked in Builder's form by the owner: approved, for a change. Builder
	// another agent sent did not get it from the owner, so it asks.
	if op == ChangeEdit && strings.TrimSpace(c.sentBy) == "" && set.isNamed(key) {
		return ""
	}
	if changeGranted(c.db, c.agentID, key) {
		set.add(key)
		return ""
	}
	what := strings.TrimSpace(kind) + " " + name
	if c.ask != nil {
		prompt := fmt.Sprintf("Let %s change the %s?", agentName, what)
		body := strings.TrimSpace(detail)
		if body == "" {
			body = "Change it."
		}
		body += "\n\nApprove covers changes to this " + strings.TrimSpace(kind) + " for the rest of this session."
		if c.ask(prompt, body, []string{"Approve"}) != "" {
			set.add(key)
			return ""
		}
		return fmt.Sprintf("NOT DONE: the owner declined this change to the %s. Leave it as it is: do not make the change another way, and do not recreate it under a different name. Tell them what you would have changed and why, and ask how they want to proceed.", what)
	}
	queueChangeApproval(c, kind, name, detail)
	return fmt.Sprintf("NOT DONE: changing the %s needs the owner's approval, and nobody is here to give it. It is waiting in their approval queue; once they approve it, the change can be made in the next run. Do not make it another way, and do not recreate it under a different name.", what)
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
// add a second entry.
func queueChangeApproval(c changeAsker, kind, name, detail string) {
	key := changeKey(kind, name)
	for _, a := range ListAuthorizations(RootDB, c.owner) {
		if a.Action == changeApprovalAction && a.Agent == c.agentID && a.Handle == key {
			return
		}
	}
	brief := fmt.Sprintf("Change the %s %s.", strings.TrimSpace(kind), name)
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
	stampRequestOrigin(&a, c.sess)
	SaveAuthorization(RootDB, a)
	Log("[orchestrate.change] %s: %s wants to change %s, queued for approval", c.owner, c.agentID, key)
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
		keys := []string{changeKey("agent", name)}
		if id != "" {
			keys = append(keys, changeKey("agent", id))
		}
		return keys
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
