package knowledge

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/gettingstarted"
)

// Knowledge's getting-started step: a collection of their own, for whoever
// can open Knowledge.
func init() {
	gettingstarted.Register(gettingstarted.Step{
		ID: "knowledge", Order: 30,
		Title: "Give it something to know",
		Help:  "A collection of your documents, for your agents to answer from.",
		Link:  "/knowledge/",
		Applies: func(r *http.Request, user string) bool {
			return UserHasAppAccess(r, "/knowledge")
		},
		Done: ownsACollection,
	})
}

// ownsACollection reports whether user has a collection of their own; the
// shared and deployment-wide ones they can also read are not theirs.
func ownsACollection(user string) bool {
	db := CollectionsDB()
	if db == nil {
		return true
	}
	for _, c := range ListCollections(UserDB(db, user), user) {
		if c.Owner == user {
			return true
		}
	}
	return false
}
