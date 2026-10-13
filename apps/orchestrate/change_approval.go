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
//     lets that agent make it in its next run (for a day).
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
// what it made, and what the owner approved in it.
type changeApprovalSet struct {
	mu    sync.Mutex
	items map[string]bool
}

func (s *changeApprovalSet) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.items[key]
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
	v, _ := sessionChangeApprovals.LoadOrStore(owner+"\x00"+session, &changeApprovalSet{items: map[string]bool{}})
	return v.(*changeApprovalSet)
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
func changeApprover(sess *ToolSession, owner, agentID, agentName string, ask func(prompt, detail string, yes []string) string) func(kind, name, detail string, existed bool) string {
	return func(kind, name, detail string, existed bool) string {
		if sess == nil {
			return ""
		}
		return changeAsker{owner: owner, agentID: agentID, agentName: agentName, session: sess.ChatSessionID,
			db: sess.DB, sentBy: sess.DispatchParentAgentID, ask: ask, sess: sess}.approve(kind, name, detail, existed)
	}
}

// approveChange is the same question for the authoring tools that run on the
// turn itself (app_def, pipeline, machine).
func (t *chatTurn) approveChange(kind, name, detail string, existed bool) string {
	if t == nil {
		return ""
	}
	ask := t.changeAsk
	if ask == nil {
		ask = t.chatAsker() // this chat's card, or the one of the chat that started this run
	}
	return changeAsker{owner: t.user, agentID: t.agent.ID, agentName: t.agent.Name, session: t.chatSessionID(),
		db: t.udb, ask: ask}.approve(kind, name, detail, existed)
}

// approve is "" when the change may go ahead, or the refusal to return.
func (c changeAsker) approve(kind, name, detail string, existed bool) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	agentName := c.agentName
	if strings.TrimSpace(agentName) == "" {
		agentName = c.agentID
	}
	key := changeKey(kind, name)
	set := changeApprovalsFor(c.owner, c.session)
	if !existed {
		// Made here: the session's own work.
		set.add(key)
		return ""
	}
	if set.has(key) {
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
// with no approver (a test, an assembly with no turn) asks nothing. existed
// false marks something just made as this session's own.
func sessionApprove(sess *ToolSession, kind, name, detail string, existed bool) string {
	if sess == nil || sess.ApproveChange == nil {
		return ""
	}
	return sess.ApproveChange(kind, name, detail, existed)
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
