// pipeline_sharing.go — peer-sharing of user-owned pipelines, the pipeline half
// of namespacing phase 5 (agent_sharing.go is the agent half, and this file
// deliberately mirrors it rather than inventing a second vocabulary).
//
// A pipeline's recipient set lives in PipelineDef.AllowedUsers; empty means
// private to the owner, which is what every pipeline was before this. The owner
// edits it with the "Share with users" ACLPicker on the pipeline page.
//
// WHAT A SHARE GIVES. The recipe, not the authority. A recipient reads the
// definition, runs it, and can copy it; the run happens in the RECIPIENT's
// namespace — their agents answer its agent stages, their catalog resolves its
// tool names, their credentials back those tools, their guardrails judge the
// boundary. Nothing of the owner's travels except the text of the pipeline. That
// is the same rule a shared agent states, and it is why sharing a pipeline is a
// decision about a way of working rather than a decision about secrets.
//
// WHAT IT DOESN'T GIVE. Editing. A recipient cannot change the definition,
// attach it to their agents, revise it, or delete it — those are the owner's,
// and a shared pipeline that anybody could rewrite is not shared, it is jointly
// owned, which is a different feature with a different set of questions.
//
// DISPATCH. A shared pipeline is reachable by the recipient's AGENTS as well as
// by the recipient (v0.6.267 — it was page-only for one version, on the argument
// that an agent choosing unprompted to run somebody else's recipe is a different
// question from a person clicking Run). It is a difference of degree, and none
// of the guards turn on it: the run happens in the requester's namespace either
// way, the dispatch policy reads a shared pipeline exactly as it reads an owned
// one, and the caller's warden judges its actions. See dispatchablePipelines.
//
// What a share still does NOT reach is a SCHEDULE. StandingAgent resolves its
// PipelineID in the owner's own store, so a schedule can only fire a pipeline
// its owner has — and an unattended, recurring run of somebody else's recipe is
// the case where "a person is in the loop" stops being true in a way dispatch
// does not.
//
// WHY AN INDEX. A recipient looking for "pipelines shared with me" is asking
// about records in other users' stores. Walking every user's store on every page
// load is what the admin audit does — correct there, once, for a console. Here
// it would run on the miss path of every lookup, so the share is registered in
// one small table in the app's own store, kept current from PipelineSavedHook
// (which every save path funnels through) and PipelineDeletedHook.
package orchestrate

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	AdminListUserOwnedPipelines = listUserOwnedPipelinesForAdmin
	AdminRevokePipelineShare = revokePipelineShareForAdmin
}

// sharedPipelinesTable maps a shared pipeline's id to its owner, in the app's
// own store rather than any user's. Presence means "shared with at least one
// person"; who it is shared WITH stays on the record, so there is one copy of
// the recipient list and the index can never contradict it.
const sharedPipelinesTable = "shared_pipelines"

// syncPipelineShareIndex is wired to PipelineSavedHook: it adds the pipeline to
// the index when it names recipients and removes it when the last one is taken
// off, so un-sharing needs no separate call and cannot be forgotten.
func syncPipelineShareIndex(def PipelineDef) {
	if orchestrateBaseDB == nil || strings.TrimSpace(def.ID) == "" {
		return
	}
	if len(def.AllowedUsers) > 0 && strings.TrimSpace(def.Owner) != "" {
		orchestrateBaseDB.Set(sharedPipelinesTable, def.ID, def.Owner)
		return
	}
	orchestrateBaseDB.Unset(sharedPipelinesTable, def.ID)
}

// dropPipelineShareIndex clears a deleted pipeline's entry. Called from
// pipelineDeleted, beside the other things a deletion has to tell.
func dropPipelineShareIndex(id string) {
	if orchestrateBaseDB == nil || strings.TrimSpace(id) == "" {
		return
	}
	orchestrateBaseDB.Unset(sharedPipelinesTable, id)
}

// userCanRunSharedPipeline reports whether reqUser (not the owner) may use a
// shared pipeline: the pipeline lists them. The owner always can through their
// own store, so this is consulted only for a non-owner.
//
// The ONE place the question is answered — every surface that could hand a
// pipeline to somebody who does not own it calls this, so a future surface
// that forgets is a missing call rather than a second, subtly different rule.
func userCanRunSharedPipeline(def PipelineDef, reqUser string) bool {
	if strings.TrimSpace(reqUser) == "" {
		return false
	}
	for _, u := range def.AllowedUsers {
		if u == reqUser {
			return true
		}
	}
	return false
}

// normalizeShareList cleans a submitted recipient list: trimmed, de-duplicated,
// and with the owner removed.
//
// The owner is dropped rather than kept-and-ignored because the list reads back
// as "who ELSE": leaving yourself on it makes a pipeline look shared the moment
// you open the picker, and un-sharing would then mean removing a name the UI
// put there itself.
func normalizeShareList(in []string, owner string) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range in {
		u = strings.TrimSpace(u)
		if u == "" || u == owner || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

// sharedPipelinesFor returns the pipelines OTHER users have shared with
// reqUser, each with its owner. The index bounds the walk to pipelines somebody
// actually shared, and the record's own recipient list decides the rest.
func sharedPipelinesFor(reqUser string) []SharedPipeline {
	if orchestrateBaseDB == nil || strings.TrimSpace(reqUser) == "" {
		return nil
	}
	var out []SharedPipeline
	for _, id := range orchestrateBaseDB.Keys(sharedPipelinesTable) {
		var owner string
		if !orchestrateBaseDB.Get(sharedPipelinesTable, id, &owner) || owner == "" || owner == reqUser {
			continue
		}
		def, ok := LoadPipelineDef(UserDB(orchestrateBaseDB, owner), owner, id)
		if !ok || !userCanRunSharedPipeline(def, reqUser) {
			continue
		}
		out = append(out, SharedPipeline{Def: def, Owner: owner})
	}
	return out
}

// SharedPipeline is one pipeline reachable by somebody who does not own it,
// carried with its owner because everything downstream needs both: the recipe
// comes from the owner's store, and the run happens as the requester.
type SharedPipeline struct {
	Def   PipelineDef
	Owner string
}

// resolvePipelineFor finds a pipeline for a requester: their own first, then one
// shared with them. Returns the def, its owner, and whether the requester owns
// it — callers gate writes on that last value rather than re-deriving it.
//
// Own-first is deliberate and matches the custom-apps rule: a requester's own
// record shadows a foreign shared one with the same id, so nothing another user
// does can change what an id means in your own store.
func resolvePipelineFor(reqUser string, udb Database, id string) (def PipelineDef, owner string, mine bool, found bool) {
	if def, ok := LoadPipelineDef(udb, reqUser, id); ok {
		return def, reqUser, true, true
	}
	for _, sp := range sharedPipelinesFor(reqUser) {
		if sp.Def.ID == id {
			return sp.Def, sp.Owner, false, true
		}
	}
	return PipelineDef{}, "", false, false
}

// listUserOwnedPipelinesForAdmin walks every user's store and returns the
// pipelines they own, each with its recipient list — the admin's audit over a
// plane that otherwise lives entirely in per-user stores.
//
// Deliberately NOT read from the share index: an audit that only sees what the
// index knows cannot show an unshared pipeline, and cannot reveal an index that
// has drifted from the records. The console is the one place worth paying a full
// walk for.
func listUserOwnedPipelinesForAdmin(db Database) []UserOwnedPipelineRow {
	out := []UserOwnedPipelineRow{}
	if db == nil {
		return out
	}
	for _, u := range AuthListUsers(db) {
		udb := UserDB(db, u.Username)
		for _, d := range ListPipelineDefs(udb, u.Username) {
			if d.Owner != u.Username || d.Owner == "" {
				continue
			}
			out = append(out, UserOwnedPipelineRow{
				ID:         d.ID,
				Owner:      d.Owner,
				Name:       d.Name,
				SharedWith: strings.Join(d.AllowedUsers, ", "),
				Shared:     len(d.AllowedUsers) > 0,
				Stages:     len(d.Stages),
			})
		}
	}
	return out
}

// revokePipelineShareForAdmin clears a pipeline's recipient list. The owner
// keeps the pipeline; everybody else loses it. Saved through SavePipelineDef so
// the share index follows the record rather than needing its own revoke.
func revokePipelineShareForAdmin(db Database, owner, id string) error {
	if db == nil || owner == "" || id == "" {
		return fmt.Errorf("owner and id required")
	}
	udb := UserDB(db, owner)
	def, ok := LoadPipelineDef(udb, owner, id)
	if !ok {
		return fmt.Errorf("no pipeline %q owned by %q", id, owner)
	}
	def.Owner = owner
	def.AllowedUsers = nil
	SavePipelineDef(udb, def)
	Log("[orchestrate.pipelines] admin revoked every share of pipeline %q (owner=%q)", def.Name, owner)
	return nil
}
