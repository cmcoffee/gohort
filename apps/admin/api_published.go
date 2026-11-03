// What users have shared with the whole deployment, kind by kind, and the
// administrator's take-back.
//
// Each kind lists its own and takes one back through the owner's own door
// (core/shareledger: Published, Unpublish): a skill or a collection returns
// to its author's pool with no recipients, an app's Share is turned off, a
// guide loses Everyone. Nothing is deleted, and the owner is told.

package admin

import (
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/core/shareledger"
)

// handlePublished lists every deployment-wide grant the kinds report.
func (a *AdminApp) handlePublished(c *jsonapi.Call) (any, error) {
	c.W.Header().Set("Cache-Control", "no-store")
	type row struct {
		Key       string `json:"key"`
		Kind      string `json:"kind"`
		Label     string `json:"label"`
		ID        string `json:"id"`
		Name      string `json:"name"`
		Owner     string `json:"owner"`
		Reach     string `json:"reach,omitempty"`
		Revocable bool   `json:"revocable"`
	}
	out := []row{}
	for _, g := range shareledger.Published() {
		out = append(out, row{Key: g.Kind + ":" + g.Owner + ":" + g.ID, Kind: g.Kind, Label: g.Label,
			ID: g.ID, Name: g.Name, Owner: g.Owner, Reach: g.Reach, Revocable: g.Revocable})
	}
	return out, nil
}

// handlePublishedTakeBack takes one back: POST ?kind=&owner=&id=.
func (a *AdminApp) handlePublishedTakeBack(c *jsonapi.Call, _ struct{}) (any, error) {
	q := c.R.URL.Query()
	kind, owner, id := strings.TrimSpace(q.Get("kind")), strings.TrimSpace(q.Get("owner")), strings.TrimSpace(q.Get("id"))
	if kind == "" || owner == "" || id == "" {
		return nil, jsonapi.BadRequest("kind, owner and id are required")
	}
	name, label := id, kind
	for _, g := range shareledger.Published() {
		if g.Kind == kind && g.Owner == owner && g.ID == id {
			name, label = g.Name, strings.ToLower(g.Label)
			break
		}
	}
	if err := shareledger.Unpublish(kind, owner, id); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	Log("[admin] %q took back %s's %s %q from the deployment", c.User, owner, kind, name)
	if RootDB != nil {
		notices.Record(RootDB, notices.Notice{
			Owner: owner,
			Kind:  notices.KindReport,
			Title: fmt.Sprintf("Your %s %q is no longer shared with everyone", label, name),
			Body:  "An administrator took it back from the deployment. It is still yours, and nothing in it changed. Its own page says who it is shared with now.",
		})
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}
