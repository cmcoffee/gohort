package orchestrate

// An app's collections, from where they are read and added to
// (docs/design/app-history.md, piece E2 step 3). Only the app's own parts
// reach one: its agents read it and add to it (Builder too, building the
// app), its scripts add to it with gohort.knowledge_add, and the people
// using the app reach it through its page and the Knowledge page. An agent
// outside the app neither sees it nor pushes into it.

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/appscript"
)

func init() {
	appscript.AppKnowledgeAdd = appKnowledgeAdd
}

// agentReadsCollections is ids without the collections a is not to read: an
// app's collection is read by that app's own agents only.
func agentReadsCollections(a AgentRecord, ids []string) []string {
	if len(ids) == 0 || orchestrateBaseDB == nil {
		return ids
	}
	udb := UserDB(orchestrateBaseDB, a.Owner)
	out := ids[:0:0]
	for _, id := range ids {
		if c, ok := LoadCollection(udb, a.Owner, id); ok && c.OwningApp != "" && c.OwningApp != a.OwningApp {
			continue
		}
		out = append(out, id)
	}
	return out
}

// agentCollectionRefusal is why the agent running sess may not reach c, ""
// when it may: c is an app's, and the agent is neither one of that app's
// nor Builder building it.
func agentCollectionRefusal(sess *ToolSession, c Collection) string {
	if c.OwningApp == "" || sess == nil || appEditOf(sess) != "" {
		return ""
	}
	if a, ok := loadAgent(sess.DB, sess.AgentID); ok && a.OwningApp == c.OwningApp {
		return ""
	}
	return fmt.Sprintf("collection %q is part of the app %q: only that app's own agents reach it", c.Name, c.OwningApp)
}

// appKnowledgeAdd is appscript's gohort.knowledge_add: a document added to
// one of spec's own collections, named by its name or id. It is the app's
// data, indexed under the script's name for it, and never one of the app's
// seed files (what someone added by hand).
func appKnowledgeAdd(ctx context.Context, spec AppSpec, caller, collection, title, text string) (string, error) {
	if VectorDB == nil || orchestrateBaseDB == nil {
		return "", fmt.Errorf("collections are not available")
	}
	udb := UserDB(orchestrateBaseDB, spec.Owner)
	want := strings.TrimSpace(collection)
	var c Collection
	found := false
	for _, x := range ListCollections(udb, spec.Owner) {
		if x.OwningApp == spec.Slug && x.Owner == spec.Owner && (x.ID == want || strings.EqualFold(x.Name, want)) {
			c, found = x, true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("the app has no collection %q of its own: knowledge_add reaches only the app's own collections", want)
	}
	title, text = strings.TrimSpace(title), strings.TrimSpace(text)
	if len(text) < pasteMinChars {
		return "", fmt.Errorf("the text is too short (%d characters, the least is %d)", len(text), pasteMinChars)
	}
	reportID := "script-" + c.ID + "-" + slugForReportID(title)
	replaced := countReportChunks(VectorDB, reportID) > 0
	n := IngestDocument(ctx, VectorDB, collectionSource(c.ID), reportID, title, "## "+title+"\n\n"+text)
	verb := "Added"
	if replaced {
		verb = "Replaced"
	}
	return fmt.Sprintf("%s %q in %s (%d passages).", verb, title, c.Name, n), nil
}

// agentDocBy is who an agent's document in c is added by: "agent" for an
// app's collection, whose agents' documents are the app's data and not its
// seed files; "" otherwise, as before.
func agentDocBy(c Collection) string {
	if c.OwningApp != "" {
		return "agent"
	}
	return ""
}

// docReportPrefix is the report id prefix of a document added as kind
// ("paste", "file"), or by what by names when it does.
func docReportPrefix(kind string, by []string) string {
	if len(by) > 0 && strings.TrimSpace(by[0]) != "" {
		return strings.TrimSpace(by[0]) + "-"
	}
	return kind + "-"
}
