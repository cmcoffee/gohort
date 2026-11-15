package orchestrate

// Naming and describing a collection from what its owner said it is for: the
// guided "+ New collection" asks for that in their own words, then offers
// names to pick from and a description drafted from it.
//
//	POST /api/collection-suggest   {field, record, offered?}             one-shot: {value} or {values}
//	                               + {message, draft, history, mode}     a turn of the ✨ workbench
//
// The description matters more than it looks: Auto-fill, research, Find
// sources and the curator all write their searches from it, so a draft names
// the concrete things the collection is about (the product, the standard, the
// official documentation, the sites) rather than restating the topic.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/assist"
)

const collectionSuggestSystem = "You help someone set up a knowledge collection: a set of documents their AI agents search when they answer. " +
	"From what they said it is for, you write its name or its description. Return only what is asked for: no commentary, no quotes around it, no markdown headers."

// collectionSuggestFields are the fields it drafts.
var collectionSuggestFields = map[string]bool{"name": true, "description": true}

// collectionSetupBrief is what the person has said so far, from the form's
// record: their own words first, then any answer already given.
func collectionSetupBrief(record map[string]any) string {
	get := func(k string) string {
		switch v := record[k].(type) {
		case string:
			return strings.TrimSpace(v)
		case []any:
			var parts []string
			for _, x := range v {
				if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
					parts = append(parts, strings.TrimSpace(s))
				}
			}
			return strings.Join(parts, ", ")
		}
		return ""
	}
	var b strings.Builder
	b.WriteString("## What they said the collection is for\n\n")
	if about := get("about"); about != "" {
		b.WriteString(about + "\n")
	} else {
		b.WriteString("(nothing yet)\n")
	}
	for _, f := range []struct{ key, label string }{
		{"name", "Name so far"}, {"description", "Description so far"},
		{"currency", "How current"}, {"depth", "How much"}, {"mostly", "Material from"},
		{"prefer", "Sources to prefer"}, {"avoid", "Sources to leave out"},
	} {
		if v := get(f.key); v != "" {
			fmt.Fprintf(&b, "\n%s: %s", f.label, v)
		}
	}
	return b.String()
}

// collectionSuggestAsk is the one-shot request for a field.
func collectionSuggestAsk(field string) string {
	if field == "name" {
		return "Suggest five names for this collection, two to four words each: plain and specific about what it holds, not a slogan. One per line, nothing else."
	}
	return "Write the collection's description: one to three sentences saying what it should hold and what it is for. " +
		"Name the concrete things it is about: the products, standards, official documentation, sites or kinds of document, " +
		"because the searches that find its material are written from this. Plain text only."
}

func (T *OrchestrateApp) collectionSuggestSpec() assist.Spec {
	field := func(req assist.Request) (string, error) {
		f := req.String("field")
		if !collectionSuggestFields[f] {
			return "", assist.Refuse(http.StatusBadRequest, "field not suggestable")
		}
		return f, nil
	}
	record := func(req assist.Request) map[string]any {
		var rec map[string]any
		req.Get("record", &rec)
		return rec
	}
	return assist.Spec{
		Route: "app.orchestrate.collection_suggest", Label: "Knowledge: naming and describing a collection", App: "/orchestrate",
		Core: &T.AppCore, Think: assist.Off, Timeout: suggestTimeout,
		Allow: func(w http.ResponseWriter, r *http.Request) bool {
			_, _, ok := RequireUser(w, r, T.DB)
			return ok
		},
		// A turn of the workbench: the description being worked on with them.
		Prompt: func(r *http.Request, req assist.Request) (string, error) {
			f, err := field(req)
			if err != nil {
				return "", err
			}
			draft := strings.TrimSpace(req.Draft)
			if draft == "" {
				draft = "(empty)"
			}
			return collectionSuggestSystem + "\n\n" + collectionSetupBrief(record(req)) +
				"\n\n## The " + f + ", as it stands now\n\n" + draft +
				"\n\n## What good looks like\n\n" + collectionSuggestAsk(f) +
				"\n\n## How to reply\n\n" + AssistReplyContract(req.Mode), nil
		},
		Shot: func(w http.ResponseWriter, r *http.Request, req assist.Request) {
			f, err := field(req)
			if err != nil {
				assist.WriteError(w, err)
				return
			}
			T.collectionSuggestOnce(w, r, f, record(req), req)
		},
	}
}

// collectionSuggestOnce is a name list or a description, from the record.
func (T *OrchestrateApp) collectionSuggestOnce(w http.ResponseWriter, r *http.Request, field string, record map[string]any, req assist.Request) {
	if T.LLM == nil {
		http.Error(w, "worker LLM not configured", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), suggestTimeout)
	defer cancel()
	prompt := collectionSetupBrief(record) + "\n\n" + collectionSuggestAsk(field)
	var offered []string
	if field == "name" {
		req.Get("offered", &offered)
		prompt += offeredNamesNote(offered)
	}
	resp, err := T.LLM.Chat(ctx, []Message{{Role: "user", Content: prompt}},
		WithSystemPrompt(collectionSuggestSystem),
		WithRouteKey("app.orchestrate.collection_suggest"),
		WithThink(false),
	)
	if err != nil || resp == nil {
		http.Error(w, fmt.Sprintf("suggest failed: %v", err), http.StatusBadGateway)
		return
	}
	out := map[string]any{"value": cleanSuggestion(field, resp.Content)}
	if field == "name" {
		if cands := freshCandidates(suggestionCandidates(resp.Content), offered); len(cands) > 0 {
			out["value"] = cands[0]
			if len(cands) > 1 {
				out["values"] = cands
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
