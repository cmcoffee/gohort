// collections — Builder-internal grouped tool for the calling user's
// Document Collections: list / get (resolve names → IDs to wire into
// agents), create / update of a collection's METADATA (name +
// description), AND corpus management — docs (list ingested documents),
// add_url (fetch + extract + ingest one URL), remove_doc (drop one).
// add_url/remove_doc reuse the autofill/source-delete primitives against
// the global VectorDB, so Builder can pull in an authoritative source or
// prune noise without bouncing the user to the Knowledge surface (which
// remains the better path for bulk topic-based Auto-fill).
//
// NOT globally registered; reaches catalogs only via
// builderAuthoringTools when the active agent IS Builder.

package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func collectionsListTool() ChatTool {
	gt := NewGroupedTool("collections",
		"Manage the user's Document Collections so you can wire them into agents (attached_collections=[...]). Actions: list, get, create (mint an empty collection), update (patch name/description), docs (list ingested documents), add_url (ingest one URL into the corpus), remove_doc (drop one document). Use add_url to pull a known authoritative source (a statute's full-text page) into a collection, and remove_doc to prune noise; for bulk topic-based filling, the Knowledge surface's Auto-fill is still the better path.")

	gt.AddAction("list", &GroupedToolAction{
		Description: "List every collection the user owns. Returns [{id, name, description, documents, chunks}] sorted by most-recently-updated. Use this when the user names a collection by display name and you need its ID to pass to attached_collections, or when surveying what corpus material exists for a new agent.",
		Caps:        []Capability{CapRead},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil || sess.DB == nil || sess.Username == "" {
				return "", errors.New("collections: requires authenticated session")
			}
			cols := listCollections(sess.DB, sess.Username)
			type entry struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				Description string `json:"description,omitempty"`
				Documents   int    `json:"documents"`
				Chunks      int    `json:"chunks"`
			}
			// Same one-pass stats walk handleCollections uses —
			// O(M) over chunks regardless of collection count.
			type stats struct{ docs, chunks int }
			statsByID := make(map[string]*stats, len(cols))
			seenReports := make(map[string]map[string]bool, len(cols))
			for _, c := range cols {
				statsByID[c.ID] = &stats{}
				seenReports[c.ID] = map[string]bool{}
			}
			for _, k := range VectorDB.Keys(EmbeddedChunks) {
				var ch EmbeddedChunk
				if !VectorDB.Get(EmbeddedChunks, k, &ch) {
					continue
				}
				const prefix = "collection:"
				if !strings.HasPrefix(ch.Source, prefix) {
					continue
				}
				id := strings.TrimPrefix(ch.Source, prefix)
				s, ok := statsByID[id]
				if !ok {
					continue
				}
				s.chunks++
				if !seenReports[id][ch.ReportID] {
					seenReports[id][ch.ReportID] = true
					s.docs++
				}
			}
			out := make([]entry, 0, len(cols))
			for _, c := range cols {
				s := statsByID[c.ID]
				out = append(out, entry{
					ID:          c.ID,
					Name:        c.Name,
					Description: c.Description,
					Documents:   s.docs,
					Chunks:      s.chunks,
				})
			}
			b, _ := json.Marshal(out)
			return string(b), nil
		},
	})

	gt.AddAction("get", &GroupedToolAction{
		Description: "Read one collection's full record by ID. Use after list when you need to confirm details (description, doc/chunk counts) before attaching to an agent.",
		Params: map[string]ToolParam{
			"id": {Type: "string", Description: "Collection ID (from collections(action=\"list\"))."},
		},
		Required: []string{"id"},
		Caps:     []Capability{CapRead},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil || sess.DB == nil || sess.Username == "" {
				return "", errors.New("collections: requires authenticated session")
			}
			id := strings.TrimSpace(fmt.Sprint(args["id"]))
			if id == "" {
				return "", errors.New("id is required")
			}
			c, ok := loadCollection(sess.DB, sess.Username, id)
			if !ok {
				return "", fmt.Errorf("collection %q not found", id)
			}
			b, _ := json.Marshal(c)
			return string(b), nil
		},
	})

	gt.AddAction("create", &GroupedToolAction{
		Description: "Mint a new empty collection (name + description); returns its record incl. id. Use when an agent needs a standalone reference corpus not tied to a skill. The user then fills it via the Knowledge surface (upload / Auto-fill). For a skill's OWN corpus, prefer create_collection=true on skill_def instead.",
		Params: map[string]ToolParam{
			"name":        {Type: "string", Description: "Display name."},
			"description": {Type: "string", Description: "What the collection holds — write it as \"contains X, for Y\" naming the docs/subjects; it seeds Auto-fill's queries and is how an LLM later picks the collection to attach or search."},
		},
		Required: []string{"name"},
		Caps:     []Capability{CapWrite},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil || sess.DB == nil || sess.Username == "" {
				return "", errors.New("collections: requires authenticated session")
			}
			name := strings.TrimSpace(stringArg(args, "name"))
			if name == "" {
				return "", errors.New("name is required for action=create")
			}
			c := Collection{
				ID:          UUIDv4(),
				Owner:       sess.Username,
				Name:        name,
				Description: strings.TrimSpace(stringArg(args, "description")),
				Created:     time.Now(),
			}
			saveCollection(sess.DB, c)
			Log("[orchestrate.collections] user=%q created collection %q (id=%s) via collections tool", sess.Username, name, c.ID)
			b, _ := json.Marshal(c)
			return string(b), nil
		},
	})

	gt.AddAction("update", &GroupedToolAction{
		Description: "Patch an existing collection's name and/or description by ID — only the fields you pass change. Use to refine a collection's description so it reads as a model-facing selection cue (\"contains X, for Y\" naming the docs/subjects). Does NOT touch the collection's documents — that stays on the Knowledge surface.",
		Params: map[string]ToolParam{
			"id":          {Type: "string", Description: "Collection ID (from collections(action=\"list\"))."},
			"name":        {Type: "string", Description: "(optional) New display name."},
			"description": {Type: "string", Description: "(optional) New description — \"contains X, for Y\" naming the docs/subjects it holds."},
		},
		Required: []string{"id"},
		Caps:     []Capability{CapWrite},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil || sess.DB == nil || sess.Username == "" {
				return "", errors.New("collections: requires authenticated session")
			}
			id := strings.TrimSpace(stringArg(args, "id"))
			if id == "" {
				return "", errors.New("id is required for action=update")
			}
			c, ok := loadCollection(sess.DB, sess.Username, id)
			if !ok {
				return "", fmt.Errorf("collection %q not found", id)
			}
			var changed []string
			if _, has := args["name"]; has {
				if s := strings.TrimSpace(stringArg(args, "name")); s != "" {
					c.Name = s
					changed = append(changed, "name")
				}
			}
			if _, has := args["description"]; has {
				c.Description = strings.TrimSpace(stringArg(args, "description"))
				changed = append(changed, "description")
			}
			if len(changed) == 0 {
				return "", errors.New("nothing to update — pass name and/or description")
			}
			saveCollection(sess.DB, c)
			Log("[orchestrate.collections] user=%q updated collection %q (id=%s): %s", sess.Username, c.Name, c.ID, strings.Join(changed, ", "))
			b, _ := json.Marshal(c)
			return string(b), nil
		},
	})

	gt.AddAction("docs", &GroupedToolAction{
		Description: "List the documents ingested into a collection — what's actually in its corpus. Returns [{doc_id, name, chunks}]. Use to audit a collection or to find the doc_id of something to drop via remove_doc.",
		Params: map[string]ToolParam{
			"id": {Type: "string", Description: "Collection ID (from collections(action=\"list\"))."},
		},
		Required: []string{"id"},
		Caps:     []Capability{CapRead},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil || sess.DB == nil || sess.Username == "" {
				return "", errors.New("collections: requires authenticated session")
			}
			id := strings.TrimSpace(stringArg(args, "id"))
			if id == "" {
				return "", errors.New("id is required for action=docs")
			}
			if _, ok := loadCollection(sess.DB, sess.Username, id); !ok {
				return "", fmt.Errorf("collection %q not found", id)
			}
			prefix := collectionSource(id)
			type group struct {
				name   string
				chunks int
			}
			groups := map[string]*group{}
			for _, key := range VectorDB.Keys(EmbeddedChunks) {
				var ch EmbeddedChunk
				if !VectorDB.Get(EmbeddedChunks, key, &ch) || !strings.HasPrefix(ch.Source, prefix) {
					continue
				}
				g, ok := groups[ch.ReportID]
				if !ok {
					g = &group{}
					groups[ch.ReportID] = g
				}
				g.chunks++
				if ch.Title != "" {
					g.name = ch.Title
				} else if ch.Section != "" && (g.name == "" || len(ch.Section) < len(g.name)) {
					g.name = ch.Section
				}
			}
			type docOut struct {
				DocID  string `json:"doc_id"`
				Name   string `json:"name"`
				Chunks int    `json:"chunks"`
			}
			out := make([]docOut, 0, len(groups))
			for rid, g := range groups {
				out = append(out, docOut{DocID: rid, Name: g.name, Chunks: g.chunks})
			}
			b, _ := json.Marshal(out)
			return string(b), nil
		},
	})

	gt.AddAction("remove_doc", &GroupedToolAction{
		Description: "Remove ONE document from a collection by its doc_id (from collections(action=\"docs\")). Deletes that document's chunks permanently. Use to prune noise from a corpus.",
		Params: map[string]ToolParam{
			"id":     {Type: "string", Description: "Collection ID."},
			"doc_id": {Type: "string", Description: "Document ID to remove (from collections(action=\"docs\"))."},
		},
		Required: []string{"id", "doc_id"},
		Caps:     []Capability{CapWrite},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil || sess.DB == nil || sess.Username == "" {
				return "", errors.New("collections: requires authenticated session")
			}
			id := strings.TrimSpace(stringArg(args, "id"))
			docID := strings.TrimSpace(stringArg(args, "doc_id"))
			if id == "" || docID == "" {
				return "", errors.New("id and doc_id are required for action=remove_doc")
			}
			if _, ok := loadCollection(sess.DB, sess.Username, id); !ok {
				return "", fmt.Errorf("collection %q not found", id)
			}
			prefix := collectionSource(id)
			removed := 0
			for _, key := range VectorDB.Keys(EmbeddedChunks) {
				var ch EmbeddedChunk
				if !VectorDB.Get(EmbeddedChunks, key, &ch) {
					continue
				}
				if ch.ReportID != docID || !strings.HasPrefix(ch.Source, prefix) {
					continue
				}
				VectorDB.Unset(EmbeddedChunks, key)
				removed++
			}
			if removed == 0 {
				return fmt.Sprintf("No document %q found in that collection (already removed?).", docID), nil
			}
			Log("[orchestrate.collections] user=%q removed doc %q from collection %q (%d chunks)", sess.Username, docID, id, removed)
			return fmt.Sprintf("Removed document %q (%d chunks).", docID, removed), nil
		},
	})

	gt.AddAction("add_url", &GroupedToolAction{
		Description: "Ingest ONE specific URL into a collection — fetches the page, extracts its text, and adds it to the corpus. Use to pull in a known authoritative source (a statute's full-text page, an official doc). JS-heavy pages extract poorly; prefer direct text / PDF / clean HTML URLs. No-op if the URL is already ingested.",
		Params: map[string]ToolParam{
			"id":  {Type: "string", Description: "Collection ID."},
			"url": {Type: "string", Description: "The URL to fetch and ingest."},
		},
		Required: []string{"id", "url"},
		Caps:     []Capability{CapWrite, CapNetwork},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			if sess == nil || sess.DB == nil || sess.Username == "" {
				return "", errors.New("collections: requires authenticated session")
			}
			id := strings.TrimSpace(stringArg(args, "id"))
			url := strings.TrimSpace(stringArg(args, "url"))
			if id == "" || url == "" {
				return "", errors.New("id and url are required for action=add_url")
			}
			c, ok := loadCollection(sess.DB, sess.Username, id)
			if !ok {
				return "", fmt.Errorf("collection %q not found", id)
			}
			norm := normalizeIngestURL(url)
			for _, u := range c.IngestedURLs {
				if normalizeIngestURL(u) == norm {
					return fmt.Sprintf("Already ingested: %s (nothing to do).", url), nil
				}
			}
			// Browser fetch can run 5–20s; give the headless fallback
			// room beyond the static-fetch budget.
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			name, text, _, _, gerr := fetchAndExtractForIngest(ctx, url)
			if gerr != nil {
				return "", gerr
			}
			reportID := fmt.Sprintf("manual-%s-%d", c.ID, time.Now().UnixNano())
			IngestReport(ctx, VectorDB, collectionSource(c.ID), reportID, "## "+name+"\n\n"+text)
			chunks := countReportChunks(VectorDB, reportID)
			c.IngestedURLs = append(c.IngestedURLs, url)
			saveCollection(sess.DB, c)
			Log("[orchestrate.collections] user=%q ingested %q into collection %q (%d chunks)", sess.Username, url, c.ID, chunks)
			return fmt.Sprintf("Ingested %q (%d chunks) into the collection.", name, chunks), nil
		},
	})

	return gt
}
