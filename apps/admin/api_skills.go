package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// registerSkillsRoutes wires the skills API under the admin sub-mux.
func (a *AdminApp) registerSkillsRoutes(sub *http.ServeMux) {
	// Skills: conditional prompt addendums that auto-activate based
	// on the user's message. GET lists all the admin's skills; POST
	// upserts (id empty = create, present = update) — Builder
	// authors most of these but the admin UI is the canonical
	// "list/edit/toggle/delete" surface. DELETE drops one by id.
	// Pipelines — declarative multi-stage workflows (core.PipelineDef),
	// stored per-user in orchestrate. Admins see the whole deployment:
	// the list walks every user's store and attributes each pipeline to
	// its owner. GET lists; DELETE ?id= removes one (pipeline IDs are
	// UUIDs, so the owner is resolved by scanning — no owner param needed).
	// Authoring lives in Agency (the pipeline tool / Builder); this is a
	// read + prune surface, mirroring the Skills section.
	sub.HandleFunc("/api/pipelines", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		// Pipelines are stored in orchestrate's per-app bucket
		// (get_agentstore("orchestrate") = global.db.Bucket("orchestrate")),
		// then per-user via UserDB — NOT in RootDB like skills/temp-tools
		// (those resolve to RootDB internally). So admin must reach into
		// the same bucket orchestrate writes to; a.db (= global.db / RootDB)
		// alone misses them. This couples admin to orchestrate's app name,
		// which is acceptable: admin is the deployment console.
		orchestrateBase := a.db.Bucket("orchestrate")
		switch r.Method {
		case http.MethodGet:
			type wire struct {
				ID          string      `json:"id"`
				Owner       string      `json:"owner"`
				Name        string      `json:"name"`
				Description string      `json:"description"`
				Stages      int         `json:"stages"`
				Detail      PipelineDef `json:"detail"`
			}
			var out []wire
			for _, u := range AuthListUsers(a.db) {
				udb := UserDB(orchestrateBase, u.Username)
				if udb == nil {
					continue
				}
				for _, d := range ListPipelineDefs(udb, u.Username) {
					out = append(out, wire{
						ID: d.ID, Owner: u.Username, Name: d.Name,
						Description: d.Description, Stages: len(d.Stages), Detail: d,
					})
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"pipelines": out})
		case http.MethodDelete:
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if id == "" {
				http.Error(w, "id required", http.StatusBadRequest)
				return
			}
			for _, u := range AuthListUsers(a.db) {
				udb := UserDB(orchestrateBase, u.Username)
				if udb == nil {
					continue
				}
				if _, ok := LoadPipelineDef(udb, u.Username, id); ok {
					DeletePipelineDef(udb, id)
					Log("[admin] %q deleted pipeline %s (owner=%q)", AuthCurrentUser(r), id, u.Username)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"deleted": id})
					return
				}
			}
			http.NotFound(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	sub.HandleFunc("/api/skills", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		username := AuthCurrentUser(r)
		if username == "" {
			http.Error(w, "no user identity", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			skills := LoadSkills(a.db, username)
			// Strip the embedding from the wire payload — it's a
			// large float32 array that the admin UI doesn't need
			// and would just bloat the response.
			type wire struct {
				ID                  string   `json:"id"`
				Name                string   `json:"name"`
				Description         string   `json:"description"`
				Triggers            []string `json:"triggers"`
				AllowedTools        []string `json:"allowed_tools"`
				AttachedCollections []string `json:"attached_collections"`
				Instructions        string   `json:"instructions"`
				Disabled            bool     `json:"disabled"`
				Updated             string   `json:"updated"`
			}
			out := make([]wire, 0, len(skills))
			for _, s := range skills {
				out = append(out, wire{
					ID: s.ID, Name: s.Name,
					Description: s.Description,
					Triggers:    s.Triggers, AllowedTools: s.AllowedTools,
					AttachedCollections: s.AttachedCollections,
					Instructions:        s.Instructions, Disabled: s.Disabled,
					Updated: s.Updated.Format("2006-01-02 15:04:05"),
				})
			}
			json.NewEncoder(w).Encode(out)
		case http.MethodPost:
			// Partial-update mode: ?action=enable|disable just flips
			// the Disabled flag and persists. Used by the per-row
			// toggle button so a quick mute doesn't require a full
			// record round-trip. The full POST body path below
			// remains for the Edit form.
			if action := strings.TrimSpace(r.URL.Query().Get("action")); action == "enable" || action == "disable" {
				id := strings.TrimSpace(r.URL.Query().Get("id"))
				if id == "" {
					http.Error(w, "missing id", http.StatusBadRequest)
					return
				}
				var found *SkillRecord
				for _, s := range LoadSkills(a.db, username) {
					if s.ID == id {
						copy := s
						found = &copy
						break
					}
				}
				if found == nil {
					http.Error(w, "skill not found", http.StatusNotFound)
					return
				}
				found.Disabled = (action == "disable")
				if _, err := SaveSkill(a.db, username, *found); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			var body SkillRecord
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(body.Name) == "" {
				http.Error(w, "name is required", http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(body.Description) == "" {
				http.Error(w, "description is required", http.StatusBadRequest)
				return
			}
			// If ID is set, preserve fields that the Edit form doesn't
			// surface from the prior record. Disabled has its own
			// dedicated toggle endpoint (?action=enable|disable), so
			// the full-body PATH from the Edit form / ChipPicker
			// never represents a deliberate Disabled change — always
			// preserve it from the prior.
			if body.ID != "" {
				for _, prior := range LoadSkills(a.db, username) {
					if prior.ID == body.ID {
						body.Created = prior.Created
						body.Disabled = prior.Disabled
						break
					}
				}
			}
			saved, err := SaveSkill(a.db, username, body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(saved)
		case http.MethodDelete:
			id := strings.TrimSpace(r.URL.Query().Get("id"))
			if id == "" {
				http.Error(w, "missing id", http.StatusBadRequest)
				return
			}
			if !DeleteSkill(a.db, username, id) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Per-skill GET — backs the row-expand FormPanel.Source. POST /
	// DELETE go through the list endpoint above (body / query
	// carries the id). Trailing-slash form so FormPanel.Source can
	// template "api/skills/{id}" cleanly.
	sub.HandleFunc("/api/skills/", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		username := AuthCurrentUser(r)
		if username == "" {
			http.Error(w, "no user identity", http.StatusUnauthorized)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/skills/")
		id = strings.Trim(id, "/")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		for _, s := range LoadSkills(a.db, username) {
			if s.ID == id {
				w.Header().Set("Content-Type", "application/json")
				// Strip embedding from the wire payload — same as list.
				type wire struct {
					ID                  string   `json:"id"`
					Name                string   `json:"name"`
					Description         string   `json:"description"`
					Triggers            []string `json:"triggers"`
					AllowedTools        []string `json:"allowed_tools"`
					AttachedCollections []string `json:"attached_collections"`
					Instructions        string   `json:"instructions"`
					Disabled            bool     `json:"disabled"`
				}
				_ = json.NewEncoder(w).Encode(wire{
					ID: s.ID, Name: s.Name, Description: s.Description,
					Triggers: s.Triggers, AllowedTools: s.AllowedTools,
					AttachedCollections: s.AttachedCollections,
					Instructions:        s.Instructions, Disabled: s.Disabled,
				})
				return
			}
		}
		http.NotFound(w, r)
	})

	// Collections (admin-side): GET returns the current user's
	// Document Collections as a lightweight picker payload so the
	// Skills editor can offer an attached_collections ChipPicker
	// alongside allowed_tools. Mirrors the per-user scope orchestrate
	// uses (UserDB under the orchestrate bucket) — admin doesn't own
	// collection storage, it just exposes a read view. List-only by
	// design: create/edit/delete still happen on the Collections page
	// in orchestrate.
	sub.HandleFunc("/api/collections", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		username := AuthCurrentUser(r)
		if username == "" {
			http.Error(w, "no user identity", http.StatusUnauthorized)
			return
		}
		orchestrateBase := a.db.Bucket("orchestrate")
		udb := UserDB(orchestrateBase, username)
		type entry struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		out := []entry{}
		if udb != nil {
			for _, c := range ListCollections(udb, username) {
				out = append(out, entry{ID: c.ID, Name: c.Name, Description: c.Description})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

}
