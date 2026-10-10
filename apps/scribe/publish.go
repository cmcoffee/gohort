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
package scribe

import (
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/core/docs"

	"github.com/cmcoffee/oddjob/apps/orchestrate"
	"github.com/cmcoffee/oddjob/apps/publish"
)

// publishDoc renders a guide as the document a destination receives: the same
// assembled markdown the Markdown export produces, plus the standalone HTML for
// destinations that would rather have it rendered. udb is the guide owner's
// store, where its pictures are.
func publishDoc(g Guide, udb Database) docs.PublishDoc {
	// The destination cannot reach /scribe/img, so pictures travel inside.
	g = inlineGuideImages(g, udb)
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
func (T *Scribe) openPublishDocument(r *http.Request, udb Database, user string) (publish.Document, bool) {
	// From the request. The Publisher pushes a document into a team wiki under
	// the deployment's branding, so a stale answer here does not show the wrong
	// list — it publishes the wrong document, to a real place, for other people.
	id := requestGuideID(r, udb)
	if id == "" {
		return publish.Document{}, false
	}
	g, ownerUDB, _, canEdit, found := T.resolve(r, udb, user, id)
	if !found || !canEdit {
		return publish.Document{}, false
	}
	return publish.Document{
		Doc:     publishDoc(g, ownerUDB),
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
func (T *Scribe) handlePublishChat(w http.ResponseWriter, r *http.Request, udb Database, user string) {
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

	// The turn's lifetime, not the page's: same reason as handleChatSend.
	turnCtx, endTurn := turnContext(r)
	defer endTurn()
	var tools []AgentToolDef
	if _, ok := T.openPublishDocument(r, udb, user); ok {
		tools = publish.BuildPublishTools(turnCtx, user, func() (publish.Document, bool) {
			return T.openPublishDocument(r, udb, user)
		})
	}
	orch.PublicHandleSendWithAppTools(w, r, agent, followTurn(tools, endTurn))
}

// handlePublishState feeds the Publish modal's header: whether this deployment
// publishes anywhere at all, whether the caller may publish THIS guide, and
// where it has already gone. GET ?id=
func (T *Scribe) handlePublishState(w http.ResponseWriter, r *http.Request, udb Database, user string) {
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
		// A target's publish is rerun through the job, with its answers.
		Target  string            `json:"target,omitempty"`
		Answers map[string]string `json:"answers,omitempty"`
	}
	rows := []publishedRow{}
	for _, p := range g.Published {
		rows = append(rows, publishedRow{
			Kind: p.Kind, Title: p.Title, TargetTitle: p.TargetTitle,
			URL: p.URL, Version: p.Version, At: p.At, Target: p.Target, Answers: p.Answers,
		})
	}
	// The person's own targets, each with its form, and whether a publish of
	// this guide is running now, so a modal opened mid-run rejoins it.
	targets := docs.PublishTargetSpecs(r.Context(), user)
	others := 0
	for _, d := range docs.PublishDestinations(user) {
		if d.Available && d.Kind != "target:" {
			others++
		}
	}
	job, _ := currentPublishJob(id)
	writeJSON(w, map[string]any{
		"configured":   docs.HasPublishDestinations(),
		"can_publish":  canEdit,
		"destinations": docs.PublishDestinations(user),
		"published":    rows,
		"targets":      targets,
		"other_count":  others,
		"targets_url":  docs.PublishTargetsSetupURL(),
		"running":      job.Target != "" && !job.Done,
	})
}

// handleRepublish updates an already-published copy with no conversation: the
// guide has a record saying exactly which page it is, so re-publishing it is a
// deterministic write. POST ?id=&kind=
func (T *Scribe) handleRepublish(w http.ResponseWriter, r *http.Request, udb Database, user string) {
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
		http.Error(w, "This guide has not been published there yet: use Publish to choose where it should go.", http.StatusBadRequest)
		return
	}
	if j, running := currentPublishJob(g.ID); running && !j.Done {
		http.Error(w, "This guide is already being published to "+j.Target+".", http.StatusConflict)
		return
	}
	// A job like any publish: it outlives the request, the dialog follows its
	// steps, and a dialog opened mid-run rejoins it. It used to run on the
	// request, a dead button for as long as the publish took.
	where := firstNonEmpty(prev.TargetTitle, prev.Kind)
	runPublishJob(r, g, ownerUDB, user, kind, where, docs.PublishRequest{
		Target:     prev.Target,
		Title:      prev.Title,
		Doc:        publishDoc(g, ownerUDB),
		ExternalID: prev.ExternalID,
		Version:    prev.Version,
		Answers:    prev.Answers,
	}, prev)
	writeJSON(w, map[string]any{"started": true, "target": where})
}
