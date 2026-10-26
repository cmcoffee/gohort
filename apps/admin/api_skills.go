package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
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
	sub.HandleFunc("/api/pipelines", jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handlePipelines, a.gate()),
		"DELETE": jsonapi.Delete(a.db, a.handlePipelineDelete, a.gate()),
	}.Serve)

	sub.HandleFunc("/api/skills", jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handleSkills, a.gate()),
		"POST":   a.skillPost(),
		"PATCH":  jsonapi.Patch(a.db, a.handleSkillSave, a.gate()),
		"DELETE": jsonapi.Delete(a.db, a.handleSkillDelete, a.gate()),
	}.Serve)

	// Per-skill GET — backs the row-expand FormPanel.Source. POST /
	// DELETE go through the list endpoint above (body / query
	// carries the id). Trailing-slash form so FormPanel.Source can
	// template "api/skills/{id}" cleanly.
	sub.HandleFunc("/api/skills/", jsonapi.Get(a.db, a.handleSkillOne, a.gate()))

	// Collections (admin-side): GET returns the current user's
	// Document Collections as a lightweight picker payload so the
	// Skills editor can offer an attached_collections ChipPicker
	// alongside allowed_tools. Mirrors the per-user scope orchestrate
	// uses (UserDB under the orchestrate bucket) — admin doesn't own
	// collection storage, it just exposes a read view. List-only by
	// design: create/edit/delete still happen on the Collections page
	// in orchestrate.
	sub.HandleFunc("/api/collections", jsonapi.Get(a.db, a.handleSkillCollections, a.gate()))
}

// orchestrateBase is where orchestrate keeps each user's pipelines and
// collections: its per-app bucket (get_agentstore("orchestrate") =
// global.db.Bucket("orchestrate")), then per-user via UserDB — NOT in RootDB
// like skills/temp-tools (those resolve to RootDB internally). So admin must
// reach into the same bucket orchestrate writes to; a.db (= global.db /
// RootDB) alone misses them. This couples admin to orchestrate's app name,
// which is acceptable: admin is the deployment console.
func (a *AdminApp) orchestrateBase() Database { return a.db.Bucket("orchestrate") }

// handlePipelines is every user's pipelines, each with its owner.
func (a *AdminApp) handlePipelines(c *jsonapi.Call) (any, error) {
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
		udb := UserDB(a.orchestrateBase(), u.Username)
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
	return map[string]any{"pipelines": out}, nil
}

// handlePipelineDelete removes the ?id= pipeline, whoever owns it.
func (a *AdminApp) handlePipelineDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	id := strings.TrimSpace(c.R.URL.Query().Get("id"))
	if id == "" {
		return nil, jsonapi.BadRequest("id required")
	}
	for _, u := range AuthListUsers(a.db) {
		udb := UserDB(a.orchestrateBase(), u.Username)
		if udb == nil {
			continue
		}
		if _, ok := LoadPipelineDef(udb, u.Username, id); ok {
			DeletePipelineDef(udb, id)
			Log("[admin] %q deleted pipeline %s (owner=%q)", c.User, id, u.Username)
			return map[string]any{"deleted": id}, nil
		}
	}
	return nil, jsonapi.NotFound("no such pipeline")
}

// skillUser is the signed-in administrator whose skills these are. An install
// with no accounts admits anyone to admin, and nobody to a skill.
func skillUser(c *jsonapi.Call) (string, error) {
	if c.User == "" {
		return "", jsonapi.Status(http.StatusUnauthorized, "no user identity")
	}
	return c.User, nil
}

// handleSkills is the administrator's own skills.
func (a *AdminApp) handleSkills(c *jsonapi.Call) (any, error) {
	username, err := skillUser(c)
	if err != nil {
		return nil, err
	}
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
		// Playbook is the rule count as a label, and PlaybookURL the
		// visual editor for it; the column links one to the other.
		Playbook string `json:"playbook"`
	}
	out := make([]wire, 0, len(skills))
	for _, s := range skills {
		pb := "add"
		if n := len(s.Playbook); n == 1 {
			pb = "1 rule"
		} else if n > 1 {
			pb = fmt.Sprintf("%d rules", n)
		}
		out = append(out, wire{
			ID: s.ID, Name: s.Name,
			Description: s.Description,
			Triggers:    s.Triggers, AllowedTools: s.AllowedTools,
			AttachedCollections: s.AttachedCollections,
			Instructions:        s.Instructions, Disabled: s.Disabled,
			Updated:  s.Updated.Format("2006-01-02 15:04:05"),
			Playbook: pb,
		})
	}
	return out, nil
}

// skillPost tells /api/skills' two POST shapes apart: ?action=enable|disable
// just flips the Disabled flag (the per-row toggle, so a quick mute doesn't
// require a full record round-trip), and anything else is the Edit form's
// whole-record save.
func (a *AdminApp) skillPost() http.HandlerFunc {
	toggle := jsonapi.Post(a.db, a.handleSkillToggle, a.gate())
	save := jsonapi.Post(a.db, a.handleSkillSave, a.gate())
	return func(w http.ResponseWriter, r *http.Request) {
		if action := strings.TrimSpace(r.URL.Query().Get("action")); action == "enable" || action == "disable" {
			toggle(w, r)
			return
		}
		save(w, r)
	}
}

// handleSkillToggle switches the ?id= skill on or off.
func (a *AdminApp) handleSkillToggle(c *jsonapi.Call, _ struct{}) (any, error) {
	username, err := skillUser(c)
	if err != nil {
		return nil, err
	}
	q := c.R.URL.Query()
	id := strings.TrimSpace(q.Get("id"))
	if id == "" {
		return nil, jsonapi.BadRequest("missing id")
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
		return nil, jsonapi.NotFound("skill not found")
	}
	found.Disabled = strings.TrimSpace(q.Get("action")) == "disable"
	if _, err := SaveSkill(a.db, username, *found); err != nil {
		return nil, jsonapi.Status(http.StatusInternalServerError, err.Error())
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleSkillSave creates a skill, or saves the one the body or ?id= names.
func (a *AdminApp) handleSkillSave(c *jsonapi.Call, raw map[string]json.RawMessage) (any, error) {
	username, err := skillUser(c)
	if err != nil {
		return nil, err
	}
	body, err := decodeSkillBody(raw, c.R.URL.Query().Get("id"), func(id string) (SkillRecord, bool) {
		for _, s := range LoadSkills(a.db, username) {
			if s.ID == id {
				return s, true
			}
		}
		return SkillRecord{}, false
	})
	if err != nil {
		return nil, jsonapi.BadRequest("invalid body: " + err.Error())
	}
	if probs := body.PlaybookProblems(); len(probs) > 0 {
		return nil, jsonapi.BadRequest("playbook: " + strings.Join(probs, "; "))
	}
	if strings.TrimSpace(body.Name) == "" {
		return nil, jsonapi.BadRequest("name is required")
	}
	if strings.TrimSpace(body.Description) == "" {
		return nil, jsonapi.BadRequest("description is required")
	}
	// One name, one skill, per person. A create (no id) or a rename
	// onto a name they already have, here or published, is refused:
	// two skills under one name is a name that picks neither, and the
	// agents naming it resolve whichever comes first. An edit that
	// keeps its name is never refused over a clash it did not cause.
	if other, taken := ownSkillNamedLike(a.db, username, body); taken {
		return nil, jsonapi.Status(http.StatusConflict, "you already have a skill called "+other.Name+"; pick another name, or edit that one")
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
		return nil, jsonapi.Status(http.StatusInternalServerError, err.Error())
	}
	return saved, nil
}

// handleSkillDelete drops the ?id= skill.
func (a *AdminApp) handleSkillDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	username, err := skillUser(c)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(c.R.URL.Query().Get("id"))
	if id == "" {
		return nil, jsonapi.BadRequest("missing id")
	}
	if !DeleteSkill(a.db, username, id) {
		return nil, jsonapi.NotFound("not found")
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleSkillOne is one skill, for its row's Edit form.
func (a *AdminApp) handleSkillOne(c *jsonapi.Call) (any, error) {
	username, err := skillUser(c)
	if err != nil {
		return nil, err
	}
	id := strings.Trim(strings.TrimPrefix(c.R.URL.Path, "/api/skills/"), "/")
	if id == "" || strings.Contains(id, "/") {
		return nil, jsonapi.NotFound("no such skill")
	}
	for _, s := range LoadSkills(a.db, username) {
		if s.ID != id {
			continue
		}
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
			// The playbook travels as text: the editor is a textarea,
			// and the save handler parses it back (decodeSkillBody).
			PlaybookText string `json:"playbook_text"`
		}
		pb := ""
		if len(s.Playbook) > 0 {
			if raw, err := json.MarshalIndent(s.Playbook, "", "  "); err == nil {
				pb = string(raw)
			}
		}
		rec := wire{
			ID: s.ID, Name: s.Name, Description: s.Description,
			Triggers: s.Triggers, AllowedTools: s.AllowedTools,
			AttachedCollections: s.AttachedCollections,
			Instructions:        s.Instructions, Disabled: s.Disabled,
			PlaybookText: pb,
		}
		if c.R.URL.Query().Get("view") != "form" {
			return rec, nil
		}
		// The Edit form's load, without the two lists the pickers
		// beside it own: the form saves the whole record it loaded
		// on every change, so a list here went back as it stood
		// when the row opened, undoing any chip flipped since.
		var out map[string]any
		raw, _ := json.Marshal(rec)
		_ = json.Unmarshal(raw, &out)
		delete(out, "allowed_tools")
		delete(out, "attached_collections")
		return out, nil
	}
	return nil, jsonapi.NotFound("no such skill")
}

// handleSkillCollections is the administrator's own collections, for the
// Skills editor's picker.
func (a *AdminApp) handleSkillCollections(c *jsonapi.Call) (any, error) {
	username, err := skillUser(c)
	if err != nil {
		return nil, err
	}
	udb := UserDB(a.orchestrateBase(), username)
	type entry struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	out := []entry{}
	if udb != nil {
		for _, col := range ListCollections(udb, username) {
			out = append(out, entry{ID: col.ID, Name: col.Name, Description: col.Description})
		}
	}
	return out, nil
}

// ownSkillNamedLike returns the user's other skill (own pool or published)
// that already answers to s's name, when s is a create or a rename. A save
// that keeps the stored name is not checked, so older data that already holds
// a duplicate can still be edited, and fixed.
func ownSkillNamedLike(db Database, user string, s SkillRecord) (SkillRecord, bool) {
	name := strings.TrimSpace(s.Name)
	mine := append(LoadSkills(db, user), PublishedSkillsBy(db, user)...)
	if s.ID != "" {
		for _, p := range mine {
			if p.ID == s.ID && strings.EqualFold(strings.TrimSpace(p.Name), name) {
				return SkillRecord{}, false
			}
		}
	}
	for _, p := range mine {
		if p.ID != s.ID && strings.EqualFold(strings.TrimSpace(p.Name), name) {
			return p, true
		}
	}
	return SkillRecord{}, false
}

// decodeSkillBody reads a skill from the posted body and the ?id= it was
// posted to. The editor sends the playbook as playbook_text, a JSON array in a
// textarea; an empty text clears it.
//
// The body is decoded ONTO the stored record when it names one (?id= or an
// "id" key), so a key the caller did not send keeps its stored value. The
// editor is a form plus two pickers, each sending only what it holds, and none
// of them holds the share list or the bundled tools; decoded into a blank
// record, every save dropped both.
func decodeSkillBody(raw map[string]json.RawMessage, queryID string, prior func(id string) (SkillRecord, bool)) (SkillRecord, error) {
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	if pt, ok := raw["playbook_text"]; ok {
		var text string
		if err := json.Unmarshal(pt, &text); err != nil {
			return SkillRecord{}, err
		}
		delete(raw, "playbook_text")
		if strings.TrimSpace(text) == "" {
			// null, not absent: onto a stored record, absent would keep the
			// rules the person just emptied the box to remove.
			raw["playbook"] = json.RawMessage("null")
		} else {
			var rules []PlaybookRule
			if err := json.Unmarshal([]byte(text), &rules); err != nil {
				return SkillRecord{}, fmt.Errorf("playbook is not a JSON array of rules: %w", err)
			}
			raw["playbook"] = json.RawMessage(text)
		}
	}
	id := strings.TrimSpace(queryID)
	if id == "" {
		var s string
		if v, ok := raw["id"]; ok && json.Unmarshal(v, &s) == nil {
			id = strings.TrimSpace(s)
		}
	}
	var body SkillRecord
	if id != "" {
		if p, ok := prior(id); ok {
			body = p
		}
		raw["id"], _ = json.Marshal(id)
	}
	merged, err := json.Marshal(raw)
	if err != nil {
		return SkillRecord{}, err
	}
	if err := json.Unmarshal(merged, &body); err != nil {
		return SkillRecord{}, err
	}
	return body, nil
}
