// collections — Builder-internal grouped tool that lists / reads
// the calling user's Document Collections so Builder can wire them
// into new agents by ID.
//
// NOT globally registered; reaches catalogs only via
// builderInternalTools when the active agent IS Builder. Read-only
// — collection authoring (create / upload / autofill / delete)
// stays on the Knowledge surface; Builder just resolves names →
// IDs so attached_collections=[...] in create_agent / update_agent
// points at the right things.

package orchestrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func collectionsListTool() ChatTool {
	gt := NewGroupedTool("collections",
		"Inspect the user's Document Collections so you can wire them into new agents (attached_collections=[...]). Read-only — collection authoring stays on the Knowledge surface.")

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
			for _, k := range sess.DB.Keys(EmbeddedChunks) {
				var ch EmbeddedChunk
				if !sess.DB.Get(EmbeddedChunks, k, &ch) {
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

	return gt
}
