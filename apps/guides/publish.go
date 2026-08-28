// Publishing a guide OUT — the Publish toolbar button.
//
// The button opens a small Publisher chat rather than a form, because the
// questions publishing actually raises (which space, what should the page be
// called, is this the same page as last time) are better asked than guessed.
// This file is only the binding: it resolves the open guide, renders it as a
// publishable document, hands the Publisher agent its tools, and records where
// the guide landed. Everything about Confluence lives in apps/publish.
//
// Republish is the deterministic escape: when a guide already has a publish
// record, updating that exact page needs no conversation and doesn't get one.
package guides

import (
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	"github.com/cmcoffee/gohort/apps/publish"
)

// publishDoc renders a guide as the document a destination receives: the same
// assembled markdown the Markdown export produces, plus the standalone HTML for
// destinations that would rather have it rendered.
func publishDoc(g Guide) docs.PublishDoc {
	brand, siteName := docBranding()
	return docs.PublishDoc{
		Title:      firstNonEmpty(g.Title, "Untitled guide"),
		Subtitle:   g.Subtitle,
		Markdown:   renderGuideMarkdown(g),
		HTML:       renderGuideStandaloneHTML(g, brand, siteName),
		SourceKind: "guide",
		SourceID:   g.ID,
	}
}

// openPublishDocument resolves the guide the user has open into the shape the
// publisher tools consume, including a Save that records the result as a
// revision — so "published to Confluence" shows up in History like any other
// change to the document.
//
// Publishing requires EDIT rights, not just view. Pushing someone's document
// into a team wiki under their deployment's branding is a bigger act than
// reading it, so a view-only reader of a shared guide can't do it.
func (T *Guides) openPublishDocument(r *http.Request, udb Database, user string) (publish.Document, bool) {
	id := activeGuideID(udb)
	if id == "" {
		return publish.Document{}, false
	}
	g, ownerUDB, _, canEdit, found := T.resolve(r, udb, user, id)
	if !found || !canEdit {
		return publish.Document{}, false
	}
	return publish.Document{
		Doc:     publishDoc(g),
		Records: g.Published,
		Save: func(rec docs.PublishRecord) error {
			// Re-read before writing: the publish call took a network round
			// trip, and the guide may have been edited while it was in flight.
			cur, ok := loadGuide(ownerUDB, g.ID)
			if !ok {
				return fmt.Errorf("the guide no longer exists")
			}
			cur.Published = docs.UpsertPublishRecord(cur.Published, rec)
			where := rec.TargetTitle
			if where == "" {
				where = rec.Kind
			}
			saveGuideRev(ownerUDB, cur, "Published to "+where)
			return nil
		},
	}, true
}

// handlePublishChat dispatches the Publish modal's chat to the Publisher agent
// with this guide's publish tools injected.
func (T *Guides) handlePublishChat(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	orch := findOrchestrate()
	if orch == nil {
		http.Error(w, "orchestrate not initialized", http.StatusServiceUnavailable)
		return
	}
	agent, ok := orch.LookupAppAgent(user, publish.PublisherAgentID)
	if !ok {
		http.Error(w, "publisher agent unavailable", http.StatusServiceUnavailable)
		return
	}
	// The Publisher talks to one document and no agents. Its own AllowedTools
	// don't reach dispatch (the `agents` grouped tool is a framework tool
	// appended regardless), so the policy is set here for the same reason the
	// Guide Author sets it — an unset mode resolves to "every agent you own".
	agent.DispatchMode, agent.AllowedDispatchTargets = orchestrate.DispatchNone, nil

	var tools []AgentToolDef
	if _, ok := T.openPublishDocument(r, udb, user); ok {
		tools = publish.BuildPublishTools(r.Context(), user, func() (publish.Document, bool) {
			return T.openPublishDocument(r, udb, user)
		})
	}
	orch.PublicHandleSendWithAppTools(w, r, agent, tools)
}

// handlePublishState feeds the Publish modal's header: whether this deployment
// publishes anywhere at all, whether the caller may publish THIS guide, and
// where it has already gone. GET ?id=
func (T *Guides) handlePublishState(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	g, _, _, canEdit, found := T.resolve(r, udb, user, id)
	if !found {
		http.NotFound(w, r)
		return
	}
	type publishedRow struct {
		Kind        string `json:"kind"`
		Title       string `json:"title"`
		TargetTitle string `json:"target_title,omitempty"`
		URL         string `json:"url,omitempty"`
		Version     int    `json:"version,omitempty"`
		At          string `json:"at,omitempty"`
	}
	rows := []publishedRow{}
	for _, p := range g.Published {
		rows = append(rows, publishedRow{
			Kind: p.Kind, Title: p.Title, TargetTitle: p.TargetTitle,
			URL: p.URL, Version: p.Version, At: p.At,
		})
	}
	writeJSON(w, map[string]any{
		"configured":   docs.HasPublishDestinations(),
		"can_publish":  canEdit,
		"destinations": docs.PublishDestinations(user),
		"published":    rows,
	})
}

// handleRepublish updates an already-published copy with no conversation: the
// guide has a record saying exactly which page it is, so re-publishing it is a
// deterministic write. POST ?id=&kind=
func (T *Guides) handleRepublish(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	g, ownerUDB, _, canEdit, found := T.resolve(r, udb, user, id)
	if !found {
		http.NotFound(w, r)
		return
	}
	if !canEdit {
		http.Error(w, "You need edit access to publish this guide.", http.StatusForbidden)
		return
	}
	prev, ok := docs.FindPublishRecord(g.Published, kind)
	if !ok {
		http.Error(w, "This guide has not been published there yet — use Publish to choose where it should go.", http.StatusBadRequest)
		return
	}
	res, err := docs.PublishDocument(r.Context(), user, kind, docs.PublishRequest{
		Target:     prev.Target,
		Title:      prev.Title,
		Doc:        publishDoc(g),
		ExternalID: prev.ExternalID,
		Version:    prev.Version,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	cur, ok := loadGuide(ownerUDB, g.ID)
	if !ok {
		http.Error(w, "the guide no longer exists", http.StatusNotFound)
		return
	}
	prev.ExternalID, prev.URL, prev.Version = res.ExternalID, res.URL, res.Version
	prev.At = now()
	cur.Published = docs.UpsertPublishRecord(cur.Published, prev)
	where := firstNonEmpty(prev.TargetTitle, prev.Kind)
	saveGuideRev(ownerUDB, cur, "Published to "+where)

	writeJSON(w, map[string]any{
		"ok":      true,
		"url":     res.URL,
		"version": res.Version,
		"message": fmt.Sprintf("Updated %q in %s.", prev.Title, where),
	})
}
