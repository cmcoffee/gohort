package orchestrate

import (
	"encoding/json"
	"errors"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func (t *chatTurn) appDefList() (string, error) {
	specs := ListAppSpecs(t.user)
	if len(specs) == 0 {
		return "No apps yet. Author one with app_def(action=\"create\", name=…, sections=[…]).", nil
	}
	type row struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
		Desc string `json:"desc,omitempty"`
		URL  string `json:"url"`
	}
	out := make([]row, len(specs))
	for i, s := range specs {
		out[i] = row{Slug: s.Slug, Name: s.Name, Desc: s.Desc, URL: "/apps/" + s.Slug + "/"}
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (t *chatTurn) appDefGet(args map[string]any) (string, error) {
	key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, ok := LoadAppSpec(t.user, key)
	if !ok {
		return "", errors.New("no matching app — check the slug (app_def action=list)")
	}
	out := map[string]any{
		"slug":       spec.Slug,
		"name":       spec.Name,
		"desc":       spec.Desc,
		"record_key": spec.RecordKey,
		"agent_id":   spec.AgentID,
		// Echoed like agent_id so an author reading the app back can see what a
		// pipeline section is bound to — a get that omits a binding invites an
		// update that silently drops it.
		"pipeline_id": spec.PipelineID,
		"full_width":  spec.FullWidth,
		"url":         "/apps/" + spec.Slug + "/",
	}
	// Hand back the AUTHORING sections — the shape action=update accepts — not
	// the rendered page. Returning the page invited the obvious next move (feed
	// it back to update), which fails the section parser on every section, so
	// the author either gave up or re-wrote the app blind.
	switch {
	case len(spec.Sections) > 0:
		out["sections"] = json.RawMessage(spec.Sections)
	default:
		// Authored before sections were stored. Reconstruct from the rendered
		// page: exact where the authoring fields ARE the body's fields (an html
		// canvas, an empty state), best-effort otherwise. Say which, because a
		// best-effort section pasted into update would silently drop whatever
		// the reversal couldn't recover.
		secs, exact := authoringSectionsFromPage(spec.Page)
		if len(secs) == 0 {
			out["page"] = json.RawMessage(spec.Page)
			out["sections_note"] = "This app predates section storage and its page could not be reversed. `page` above is the RENDERED page — NOT valid input to action=update. Re-author the sections array from scratch (each section needs a `kind`); the next successful update stores it for real."
			break
		}
		out["sections"] = secs
		if exact {
			out["sections_note"] = "Reconstructed from the stored page (this app predates section storage) — lossless for these section kinds. Edit and pass back to action=update."
		} else {
			out["sections_note"] = "Reconstructed BEST-EFFORT from the stored page (this app predates section storage). Section kinds are right, but per-kind fields may be incomplete — check them against action=help before passing back to action=update, since update REPLACES the page with what you send."
		}
	}
	// Surface the logic seam so an update can inspect + revise it (scripts omitted
	// for size; names/caps/schedule are what you edit). schedule is the self-update
	// cadence — present here means the action fires unattended.
	if len(spec.DataSources) > 0 {
		ds := make([]map[string]any, len(spec.DataSources))
		for i, d := range spec.DataSources {
			ds[i] = map[string]any{"name": d.Name, "language": d.Language, "capabilities": d.Capabilities}
		}
		out["data_sources"] = ds
	}
	if len(spec.Actions) > 0 {
		acts := make([]map[string]any, len(spec.Actions))
		for i, a := range spec.Actions {
			m := map[string]any{"name": a.Name, "label": a.Label, "capabilities": a.Capabilities}
			if a.Confirm != "" {
				m["confirm"] = a.Confirm
			}
			if a.Schedule.Scheduled() {
				m["schedule"] = a.Schedule
			}
			acts[i] = m
		}
		out["actions"] = acts
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

// isFullHTMLDocument reports whether a blob is a whole page rather than a
// fragment — it opens with a doctype or an <html> tag, or carries its own
// <body>. Whole documents render in their own frame (see the html section);
// fragments splice into the page.
func isFullHTMLDocument(html string) bool {
	head := strings.ToLower(strings.TrimSpace(html))
	if len(head) > 2048 {
		head = head[:2048]
	}
	return strings.HasPrefix(head, "<!doctype") || strings.HasPrefix(head, "<html") || strings.Contains(head, "<body")
}

// bodyTypeToKind maps a RENDERED section body's component type back to the
// authoring section kind that produces it. Reverse of buildAppSection's switch.
var bodyTypeToKind = map[string]string{
	"card":             "html",
	"frame":            "html",
	"empty_state":      "empty",
	"form_panel":       "form",
	"modal_button":     "form",
	"table":            "table",
	"display_panel":    "display",
	"chart_panel":      "chart",
	"chat_panel":       "chat",
	"agent_loop_panel": "chat",
	"workbench_panel":  "workbench",
	"action_list":      "actions",
}

// exactReverseKinds are the kinds whose authoring fields ARE the rendered
// body's fields, so reversing loses nothing: an html canvas is its html, an
// empty state its icon/title/hint. Everything else reverses best-effort —
// the kind is certain, individual fields may not survive.
var exactReverseKinds = map[string]bool{"html": true, "empty": true}

// authoringSectionsFromPage turns a stored pageConfig back into the authoring
// sections array, for specs written before AppSpec.Sections existed. Returns
// the sections and whether every one of them reversed exactly. Callers must
// surface the exactness — update REPLACES the page with what it is handed, so
// a lossy reversal fed back would quietly drop fields.
func authoringSectionsFromPage(page json.RawMessage) ([]map[string]any, bool) {
	if len(page) == 0 {
		return nil, false
	}
	var pc struct {
		Sections []struct {
			Title    string         `json:"title"`
			Subtitle string         `json:"subtitle"`
			Body     map[string]any `json:"body"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(page, &pc); err != nil || len(pc.Sections) == 0 {
		return nil, false
	}
	// Plumbing the framework wires itself (endpoints, cache invalidation). It is
	// not authoring input and re-emitting it only invites confusion.
	plumbing := map[string]bool{"type": true, "source": true, "post_url": true, "invalidate": true}
	out := make([]map[string]any, 0, len(pc.Sections))
	exact := true
	for _, s := range pc.Sections {
		body := s.Body
		kind, ok := bodyTypeToKind[strings.TrimSpace(mapStr(body, "type"))]
		if !ok {
			return nil, false
		}
		sec := map[string]any{"kind": kind}
		if s.Title != "" {
			sec["title"] = s.Title
		}
		if s.Subtitle != "" {
			sec["subtitle"] = s.Subtitle
		}
		// A modal form renders as a ModalButton wrapping the form; the section's
		// own chrome moved onto the modal, so read the title back from there.
		if mapStr(body, "type") == "modal_button" {
			sec["modal"] = true
			if lbl := mapStr(body, "label"); lbl != "" {
				sec["submit_label"] = lbl
			}
			if ttl := mapStr(body, "title"); ttl != "" {
				sec["title"] = ttl
			}
			inner, _ := body["body"].(map[string]any)
			body = inner
		}
		for k, v := range body {
			if plumbing[k] || k == "" {
				continue
			}
			if _, taken := sec[k]; taken {
				continue
			}
			sec[k] = v
		}
		// A script-backed panel points at "data/<name>"; authoring names the
		// data source directly.
		if src := mapStr(body, "source"); strings.HasPrefix(src, "data/") {
			sec["source_script"] = strings.TrimPrefix(src, "data/")
		}
		if !exactReverseKinds[kind] {
			exact = false
		}
		out = append(out, sec)
	}
	return out, exact
}
