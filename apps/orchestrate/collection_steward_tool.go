package orchestrate

// The submitter's tool: an agent that may add documents to ONE or a few of
// its owner's collections (collection_submit.go).
//
// The shape this serves is a process feeding a corpus: an agent that fetches
// the latest admin guide, or the monthly vendor reports, and puts them in a
// collection other agents read. It adds; it does not curate. What stays in
// the collection is the plan's and the check's to decide, and taking a
// document out is the owner's and the maintainers'.
//
// The add actions come from the `collections` tool's own primitives (fetch a
// page, read a file, paste text), bound to the collections the agent may
// submit to and nothing else. Each collection says how the agent's
// submissions are taken: checked at the door (the collection check, and a
// second source for a secondary page; what fails is held for the owner or
// turned away), or always allowed (added as sent). Each add can name the
// document it replaces, which is then marked as the older version.
//
// Who may submit is read from Collection.Submitters (and, for a collection
// saved by an earlier version, its CuratorAgent), so the answer is the one the
// collection page shows.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// submitActions are the actions a submitter has: see what is in the
// collection, and add to it.
var submitActions = []string{"docs", "add_url", "add_file", "add_text"}

// collectionKeepingTools are the tools an agent's place in the collections it
// reads brings: adding to the ones it may submit to, and suggesting updates to
// the ones it only reads (collection_suggestions.go). One place, for every
// path that builds a catalog: the chat turn and the dispatch runs
// (delegation, schedules, channels).
//
// Owner-only: both reach the owner's corpus, so neither goes to a run by a
// granted non-owner, or to one a stranger set going: a channel message from
// anyone but the owner marks the run's context, and the mark rides through
// every delegation it starts (authoring_requester.go).
func (t *chatTurn) collectionKeepingTools(sess *ToolSession) []AgentToolDef {
	if t == nil || t.app == nil {
		return nil
	}
	owner := t.agent.Owner == "" || t.agent.Owner == seedOwner || t.agent.Owner == t.user
	if !owner || nonOwnerRequester(t.ctx) {
		return nil
	}
	var out []AgentToolDef
	if cols := submittableCollectionsFor(UserDB(t.app.DB, t.user), t.user, t.agent.ID); len(cols) > 0 {
		if st := t.app.submitTool(cols, t.agent.ID); st != nil {
			out = append(out, ChatToolToAgentToolDefWithSession(st, sess))
			Log("[orchestrate.tools] agent=%s: submits to %d collection(s), scoped tool attached", t.agent.ID, len(cols))
		}
	}
	if td, ok := t.suggestCollectionUpdateTool(); ok {
		out = append(out, td)
	}
	return out
}

// submitTool builds the scoped tool for the collections agentID may submit
// to, or nil when there are none.
func (T *OrchestrateApp) submitTool(cols []Collection, agentID string) ChatTool {
	if len(cols) == 0 {
		return nil
	}
	grouped, ok := collectionsListTool().(*GroupedTool)
	if !ok {
		return nil
	}
	allowed := make(map[string]Collection, len(cols))
	var names []string
	for _, c := range cols {
		allowed[c.ID] = c
		names = append(names, fmt.Sprintf("%q (%s)", c.Name, c.ID))
	}
	only := cols[0].ID
	brief := "Add documents to the collection you submit to: a page by its address, a file from your workspace, or text; and list what is in it. " +
		"Adding under a title already there replaces that document, which keeps one current version. " +
		"You cannot remove documents. Scoped to " + strings.Join(names, ", ") + " and nothing else."
	gt := NewGroupedTool("collection", brief)
	for _, act := range submitActions {
		a, found := grouped.Action(act)
		if !found {
			continue
		}
		inner := T.submitHandler(act, a.Handler, agentID)
		if act != "docs" {
			a.Params["supersedes"] = ToolParam{Type: "string", Description: "(optional) The title or doc_id of the document this one replaces: it is kept, marked as the older version, and ranked below this one."}
		}
		if len(cols) == 1 {
			// One collection means there is nothing to choose, so there is no
			// parameter. A model cannot target the wrong corpus if it is never
			// asked which.
			delete(a.Params, "id")
			a.Required = dropString(a.Required, "id")
		} else if p, has := a.Params["id"]; has {
			p.Description = "Which collection, one of: " + strings.Join(names, ", ")
			a.Params["id"] = p
		}
		a.Handler = func(args map[string]any, sess *ToolSession) (string, error) {
			if args == nil {
				args = map[string]any{}
			}
			id := strings.TrimSpace(stringArg(args, "id"))
			if id == "" {
				id = only
			}
			// The gate. An id outside the grant is REFUSED rather than
			// substituted: quietly redirecting the call would teach the agent
			// that any id works.
			c, granted := allowed[id]
			if !granted {
				return "", fmt.Errorf("you may not add to collection %q; yours is %s", id, strings.Join(names, ", "))
			}
			args["id"] = id
			return inner(c, args, sess)
		}
		gt.AddAction(act, &a)
	}
	if len(gt.ActionNames()) == 0 {
		return nil
	}
	return gt
}

// submitHandler is one action of the submitter's tool, taking c's trust for
// agentID: checked submissions go through the collection check first.
func (T *OrchestrateApp) submitHandler(act string, inner func(map[string]any, *ToolSession) (string, error), agentID string) func(Collection, map[string]any, *ToolSession) (string, error) {
	checkFor := func(c Collection) pageCheck {
		if submitterTrust(c, agentID) == trustAlways {
			return nil
		}
		return T.agentPageCheck
	}
	switch act {
	case "add_url":
		return func(c Collection, args map[string]any, sess *ToolSession) (string, error) {
			msg, doc, replaces, err := addURLToCollectionDoc(args, sess, checkFor(c))
			if err != nil || doc == "" {
				return msg, err
			}
			return msg + T.afterSubmit(c, doc, replaces, stringArg(args, "supersedes")), nil
		}
	case "add_text":
		return func(c Collection, args map[string]any, sess *ToolSession) (string, error) {
			title, text := stringArg(args, "title"), stringArg(args, "text")
			replaces := ""
			if check := checkFor(c); check != nil {
				ctx, cancel := context.WithTimeout(context.Background(), vetTimeout)
				note, add, rep := check(ctx, c, vetCandidate{Title: title, Text: text})
				cancel()
				if !add {
					return note, nil
				}
				replaces = rep
			}
			if why := collectionWriteRefusal(c, sess.Username, "paste", http.MethodPost, UserIsAdmin(sess.Username)); why != "" {
				return "", errors.New(why)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			res, err := pasteIntoCollection(ctx, VectorDB, c, title, text)
			if err != nil {
				return "", err
			}
			verb := "Added"
			if res.Replaced {
				verb = "Replaced"
			}
			msg := fmt.Sprintf("%s document %q (%s, %d chunks) in the collection; doc_id %s.", verb, res.Name, res.Format, res.Chunks, res.ID)
			return msg + T.afterSubmit(c, res.ID, replaces, stringArg(args, "supersedes")), nil
		}
	case "add_file":
		return func(c Collection, args map[string]any, sess *ToolSession) (string, error) {
			if why := collectionWriteRefusal(c, sess.Username, "upload", http.MethodPost, UserIsAdmin(sess.Username)); why != "" {
				return "", errors.New(why)
			}
			src, err := ResolveUploadSource(sess, stringArg(args, "path"))
			if err != nil {
				return "", err
			}
			title := strings.TrimSpace(stringArg(args, "title"))
			if title == "" {
				title = src.Name
			}
			ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout())
			defer cancel()
			replaces := ""
			var check func(text string) (string, bool)
			if pc := checkFor(c); pc != nil {
				check = func(text string) (string, bool) {
					vctx, vcancel := context.WithTimeout(context.Background(), vetTimeout)
					defer vcancel()
					note, add, rep := pc(vctx, c, vetCandidate{Title: title, Text: text})
					replaces = rep
					return note, add
				}
			}
			res, err := addFileToCollection(ctx, VectorDB, c, title, src, check)
			if err != nil {
				return "", err
			}
			if res.Refused != "" {
				return res.Refused, nil
			}
			saveCollection(sess.DB, c)
			verb := "Added"
			if res.Replaced {
				verb = "Replaced"
			}
			msg := fmt.Sprintf("%s document %q (%d chunks) in the collection; doc_id %s.", verb, res.Name, res.Chunks, res.ID)
			return msg + T.afterSubmit(c, res.ID, replaces, stringArg(args, "supersedes")), nil
		}
	}
	return func(_ Collection, args map[string]any, sess *ToolSession) (string, error) { return inner(args, sess) }
}

// afterSubmit marks what a submitted document replaced: the document the check
// found it supersedes, and the one the submitter named. It returns what to add
// to the tool's reply.
func (T *OrchestrateApp) afterSubmit(c Collection, doc, replaces, named string) string {
	var notes []string
	mark := func(old string) {
		if old == "" || old == doc {
			return
		}
		if T.markSuperseded(c, old, doc) {
			notes = append(notes, supersededNote(docTitle(T.docChunks(c, old), old)))
		}
	}
	mark(replaces)
	if named = strings.TrimSpace(named); named != "" {
		old, ok := T.docByTitleOrID(c, named)
		switch {
		case !ok:
			notes = append(notes, fmt.Sprintf("No document %q was found to mark as the older version.", named))
		case old != doc && old != replaces:
			mark(old)
		}
	}
	if len(notes) == 0 {
		return ""
	}
	return " " + strings.Join(notes, " ")
}

// docTitle is a document's title from its passages, or fallback.
func docTitle(chunks []EmbeddedChunk, fallback string) string {
	for _, ch := range chunks {
		if t := strings.TrimSpace(ch.Title); t != "" {
			return t
		}
	}
	return fallback
}

func dropString(ss []string, drop string) []string {
	out := ss[:0]
	for _, s := range ss {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}
