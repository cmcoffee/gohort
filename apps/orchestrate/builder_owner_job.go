package orchestrate

// A Builder job an agent sent that touches a tool the agent may not use goes
// to the owner, instead of ending there.
//
// A Builder run another agent sent works within that agent's limits
// (builder_delegation.go), so an agent barred from a tool cannot have Builder
// change it for it. That stays. But the job was the agent's whole answer to
// "tell Builder how you would improve this", and a refusal dropped it: the
// owner never saw what was asked. Now the first such refusal in a run queues
// the job for the owner, shown in the asking agent's chat as Approve change
// is. Approving runs the same job in Builder as the owner, with the owner's
// permissions and the edits it needs allowed; the agent's own limits are left
// as they were. Denying keeps the owner's no (proposal_declines.go), so the
// same job, reworded or not, is not put to them again.
//
// Tools only: a job refused for an API the agent may not use stays refused,
// since that is the agent's reach and not how well a tool works.

import (
	"fmt"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

// builderAsOwnerAction is a Builder job an agent sent, held for the owner to
// run as themselves.
const builderAsOwnerAction = "builder_as_owner"

// proposalKindBuilderJob is a declined builderAsOwnerAction, in the decline
// store.
const proposalKindBuilderJob = "builder_job"

// ownerJob is the work a Builder run was sent: the brief, and how to show a
// request in the chat it came from when somebody is watching (nil when nobody
// is: it waits in Requests and loads as a card in that chat).
type ownerJob struct {
	brief string
	live  func(Authorization)
}

// ownerJobHold is one Builder run's request to its owner: queued once, however
// many of the run's tool calls are refused.
type ownerJobHold struct {
	job  ownerJob
	mu   sync.Mutex
	note string // what the run is told once it is decided (queued, or declined before)
}

// heldForOwner queues the job this Builder run was sent, for the owner to run
// as themselves, and returns what the refusal adds: where it went, or that the
// owner already declined it. Empty when there is nothing to queue (no brief).
func (h *ownerJobHold) heldForOwner(sess *ToolSession, db Database, from AgentRecord, toolName string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.note != "" {
		return h.note
	}
	brief := strings.TrimSpace(h.job.brief)
	owner := strings.TrimSpace(sess.Username)
	if brief == "" || owner == "" {
		return ""
	}
	who := delegatorName(from)
	match := checkProposalDeclines(db, from.ID, proposalKindBuilderJob, brief)
	if d := match.Same; d != nil {
		h.note = fmt.Sprintf(" The owner already declined this job on %s, so it is not put to them again: say so in your reply.", d.DeclinedAt.Local().Format("Jan 2"))
		Log("[orchestrate.delegation] %s's Builder job not queued: it matches one %s declined on %s", from.ID, owner, d.DeclinedAt.Local().Format("Jan 2"))
		return h.note
	}
	fp := proposalFingerprint(proposalKindBuilderJob, brief)
	for _, a := range ListAuthorizations(RootDB, owner) {
		if a.Action == builderAsOwnerAction && a.FromAgent == from.ID && proposalFingerprint(proposalKindBuilderJob, a.Brief) == fp {
			h.note = fmt.Sprintf(" The job already waits on the owner's approval (id %s): say so in your reply.", a.ID)
			return h.note
		}
	}
	why := fmt.Sprintf("%s may not use the tool %s, so Builder could not do this for it.", who, toolName)
	if like := likeDeclinedNote(match.Similar); like != "" {
		why += " " + like
	}
	a := Authorization{Owner: owner, Action: builderAsOwnerAction, Agent: "builder", FromAgent: from.ID, Brief: brief, Text: why}
	stampRequestOrigin(&a, sess)
	a = SaveAuthorization(RootDB, a)
	if h.job.live != nil {
		h.job.live(a)
	}
	Log("[orchestrate.delegation] %s's Builder job touches %s, which it may not use: queued for %s to run as themselves (%s)", from.ID, toolName, owner, a.ID)
	h.note = fmt.Sprintf(" The whole job is queued for the owner instead (id %s): approving it runs it in Builder with their own permissions. %s Stop here and say in your reply that it waits on their approval.", a.ID, requestOriginNote(a))
	return h.note
}

// runApprovedOwnerJob runs an approved builderAsOwnerAction: Builder, as the
// owner, with the edits it needs allowed, its result back in the conversation
// that asked.
func (T *OrchestrateApp) runApprovedOwnerJob(a Authorization) {
	builder := ""
	if udb := UserDB(T.DB, a.Owner); udb != nil {
		if rec, ok := findAgentByNameOrID(udb, a.Owner, "builder"); ok && isBuilderAgent(rec.ID) {
			builder = rec.ID
		}
	}
	T.runApproved(approvedRun{
		a: a, target: "builder", brief: a.Brief, ownerApproved: builder, asOwner: true,
		label: fmt.Sprintf("the Builder job you sent, which the owner approved to run with their own permissions (%s)", truncateObs(a.Brief, 160)),
	})
}
