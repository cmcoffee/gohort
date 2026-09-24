package extensions

// A skill's kept versions. The list, the read-only preview and the way back
// all come from core/revisions.Surface; what is here is the part that differs.

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/revisions"
)

// handleUserSkillOne serves /api/skills/{id}/revisions[/preview|/restore].
//
// A separate route from /api/skills because that one is an exact pattern: a
// path with anything after it does not reach it at all.
func (T *Extensions) handleUserSkillOne(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/skills/")
	id, tail, _ := strings.Cut(rest, "/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	action, isRev := revisionAction(tail)
	if !isRev {
		http.NotFound(w, r)
		return
	}
	// Scoped to the caller's own skills, in either pool: one they published
	// is still theirs, with the same history. A skill id belonging to
	// somebody else simply is not found.
	own, found := findOwnSkill(user, id)
	current := own.SkillRecord
	if !found {
		http.NotFound(w, r)
		return
	}
	// The store is NOT the handle above and the key is NOT the id: skills live
	// in RootDB keyed by username, so the owner rides in the ring key. Both
	// come from core rather than being rebuilt here, because getting either
	// wrong reads as "no history" rather than as an error.
	store, key := SkillRevisionRing(AuthDB(), user, id)
	revisions.Surface{
		Store:   store,
		Kind:    revisions.KindSkill,
		Key:     key,
		Noun:    "skill",
		Current: current,
		Restore: func(ref string) error {
			_, err := RollbackSkill(AuthDB(), user, id, ref)
			return err
		},
	}.Serve(w, r, action)
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
