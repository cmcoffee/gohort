package orchestrate

// The revision surface for the definitions this app owns: agents, machines and
// pipelines.
//
// Each one is a few lines because the list, the read-only preview and the
// restore all live in core/revisions.Surface. What stays here is the part that
// differs: who may look, what "restore" does, and which fields are noise in a
// comparison.

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/revisions"
)

// handleAgentRevisions serves /api/agents/{id}/revisions[/preview|/restore].
//
// Owner-gated the way handleAgentLock is, and for the same reason: this is a
// human acting on their own agent. Seeds load with an empty Owner until first
// shadowed, which counts as the caller's own.
func (T *OrchestrateApp) handleAgentRevisions(w http.ResponseWriter, r *http.Request, user, id, action string) {
	udb := UserDB(T.DB, user)
	a, ok := loadAgent(udb, id)
	if !ok || (a.Owner != "" && a.Owner != user) {
		http.NotFound(w, r)
		return
	}
	locked := ""
	if a.Locked {
		// A restore is the largest edit there is, so the lock that stops an
		// edit stops this too. Reading history stays open: a lock is about
		// changing the agent, not about knowing what it used to say.
		locked = "this agent is locked — unlock it first (the 🔒 icon at the top-right of the editor)"
	}
	revisions.Surface{
		Store:        udb,
		Kind:         revisions.KindAgent,
		Key:          id,
		Noun:         "agent",
		Current:      a,
		LockedReason: locked,
		Restore: func(ref string) error {
			_, err := rollbackAgent(udb, id, ref)
			return err
		},
	}.Serve(w, r, action)
}

// handleMachineRevisions serves the same three routes for a machine, taking
// the machine the router already resolved.
//
// The ring lives in the OWNER's store, because that is where the saves that
// filed it ran. A recipient reading a shared machine's history from their own
// store would find an empty list and conclude nothing had ever been edited.
//
// Restore is offered only to the owner, and by leaving the callback nil rather
// than refusing the click: a button that is always going to say no is worse
// than no button.
func (T *OrchestrateApp) handleMachineRevisions(w http.ResponseWriter, r *http.Request, user string, def MachineDef, owner string, mine bool, action string) {
	ownerDB := UserDB(T.DB, ownerOr(owner, user))
	s := revisions.Surface{
		Store:   ownerDB,
		Kind:    revisions.KindMachine,
		Key:     def.ID,
		Noun:    "machine",
		Current: def,
		// The one-deep undo snapshot the describe-a-change door stashes on the
		// record. It is a whole other version, so leaving it in would report
		// "previous" as changed on every comparison and print a machine inside
		// the diff.
		Ignore: []string{"previous"},
	}
	if mine {
		s.Restore = func(ref string) error {
			_, err := RollbackMachineDef(ownerDB, def.ID, ref)
			return err
		}
	}
	s.Serve(w, r, action)
}

// handlePipelineRevisions serves the same three routes for a pipeline.
func (T *OrchestrateApp) handlePipelineRevisions(w http.ResponseWriter, r *http.Request, user string, def PipelineDef, owner string, mine bool, action string) {
	ownerDB := UserDB(T.DB, ownerOr(owner, user))
	s := revisions.Surface{
		Store:   ownerDB,
		Kind:    revisions.KindPipeline,
		Key:     def.ID,
		Noun:    "pipeline",
		Current: def,
		Ignore:  []string{"previous"},
	}
	if mine {
		s.Restore = func(ref string) error {
			_, err := RollbackPipelineDef(ownerDB, def.ID, ref)
			return err
		}
	}
	s.Serve(w, r, action)
}

// ownerOr falls back to the requesting user when a record carries no owner,
// which is what an unshadowed seed looks like.
func ownerOr(owner, user string) string {
	if strings.TrimSpace(owner) == "" {
		return user
	}
	return owner
}

// revisionAction turns a path tail into the sub-action Surface.Serve expects:
// "revisions" is the list, "revisions/preview" and "revisions/restore" the
// other two. ok is false when the tail is not a revisions route at all.
func revisionAction(tail string) (action string, ok bool) {
	if tail == "revisions" {
		return "", true
	}
	if rest, found := strings.CutPrefix(tail, "revisions/"); found {
		return rest, true
	}
	return "", false
}
