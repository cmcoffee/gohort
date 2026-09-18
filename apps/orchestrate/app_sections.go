package orchestrate

// Section ids — editing one section without re-sending the array.
//
// update replaces the sections array wholesale, so a section left out is a
// section deleted, and the guard for that (appDroppedFunctionSection) only
// covers the kinds whose loss is invisible. An author changing one table's
// columns had to reproduce every other section byte-for-byte, which is the
// same re-typing failure patch_html exists to prevent, one level up.
//
// So every stored section carries a stable id, assigned on first save from
// its kind and title ("form-new-entry", "table-2") and kept thereafter — an
// author-supplied id wins, and a re-sent section that still carries its id
// keeps it. Three actions address a section by id and compose the full update
// themselves, then hand it to the ordinary update path so every guard it has
// (functional-section loss, stranded record fields, html shrink, script
// syntax, the browser load) runs unchanged. Nothing here saves anything.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// ensureSectionIDs returns the sections array with every section carrying an
// id. Existing ids are kept; missing ones are derived from kind + title and
// made unique with a numeric suffix. The result is a fresh []any of fresh maps
// so the caller's input is not mutated.
func ensureSectionIDs(raw any) any {
	arr, ok := raw.([]any)
	if !ok {
		return raw
	}
	used := map[string]bool{}
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			if id := strings.TrimSpace(mapStr(m, "id")); id != "" {
				used[id] = true
			}
		}
	}
	out := make([]any, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		cp := make(map[string]any, len(m)+1)
		for k, v := range m {
			cp[k] = v
		}
		if id := strings.TrimSpace(mapStr(cp, "id")); id != "" {
			cp["id"] = id
		} else {
			cp["id"] = newSectionID(normalizeSection(cp), used)
		}
		used[mapStr(cp, "id")] = true
		out = append(out, cp)
	}
	return out
}

// ensureSectionIDsJSON is ensureSectionIDs over the stored Sections blob, for
// a get on a spec saved before ids existed. Returns the input untouched when
// it does not parse.
func ensureSectionIDsJSON(stored json.RawMessage) json.RawMessage {
	var raw any
	if json.Unmarshal(stored, &raw) != nil {
		return stored
	}
	b, err := json.Marshal(ensureSectionIDs(raw))
	if err != nil {
		return stored
	}
	return b
}

// newSectionID derives "kind" or "kind-title-slug", suffixed until unused.
func newSectionID(m map[string]any, used map[string]bool) string {
	base := slugify(firstNonEmptyStr(strings.TrimSpace(mapStr(m, "kind")), "section"))
	if title := slugify(strings.TrimSpace(mapStr(m, "title"))); title != "" && title != base {
		base += "-" + title
	}
	if !used[base] {
		return base
	}
	for n := 2; ; n++ {
		if id := fmt.Sprintf("%s-%d", base, n); !used[id] {
			return id
		}
	}
}

// findSectionByID returns the index of the section with the id, or -1.
func findSectionByID(sections []map[string]any, id string) int {
	id = strings.TrimSpace(id)
	if id == "" {
		return -1
	}
	for i, m := range sections {
		if strings.TrimSpace(mapStr(m, "id")) == id {
			return i
		}
	}
	return -1
}

// sectionIDList renders the ids an author can address, for a refusal.
func sectionIDList(sections []map[string]any) string {
	var ids []string
	for _, m := range sections {
		if id := strings.TrimSpace(mapStr(m, "id")); id != "" {
			ids = append(ids, id+" ("+strings.ToLower(strings.TrimSpace(mapStr(m, "kind")))+")")
		}
	}
	if len(ids) == 0 {
		return "none yet: the next save assigns them"
	}
	return strings.Join(ids, ", ")
}

// sectionArg reads the `section_def` object of update_section / add_section.
func sectionArg(args map[string]any) (map[string]any, error) {
	m, ok := args["section_def"].(map[string]any)
	if !ok || len(m) == 0 {
		return nil, errors.New("section_def is required: the section OBJECT ({kind, title, fields…}), the same shape one entry of `sections` takes")
	}
	m = normalizeSection(m)
	if strings.TrimSpace(mapStr(m, "kind")) == "" {
		return nil, errors.New("the section needs a `kind` (form | table | display | chart | actions | empty | chat | pipeline | workbench | html)")
	}
	return m, nil
}

// applySectionEdit loads the app, applies one mutation to its authoring
// sections, and runs the result through the ordinary update. The loaded
// sections always carry ids (a spec saved before they existed gets them
// here, and the update that follows stores them).
func (t *chatTurn) applySectionEdit(args map[string]any, reason string, mutate func(secs []map[string]any) ([]map[string]any, error)) (string, error) {
	key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, ok := LoadAppSpec(t.user, key)
	if !ok {
		return "", errors.New("no matching app: check the slug (app_def action=list)")
	}
	current, err := appAuthoringSections(spec)
	if err != nil {
		return "", err
	}
	withIDs, _ := ensureSectionIDs(toAnySlice(current)).([]any)
	current = appProposedSections(withIDs)
	next, err := mutate(current)
	if err != nil {
		return "", err
	}
	update := map[string]any{
		"id":       spec.Slug,
		"sections": toAnySlice(next),
		"_reason":  reason,
	}
	for _, k := range []string{"note", "confirm_rewrite"} {
		if v, ok := args[k]; ok {
			update[k] = v
		}
	}
	return t.appDefCreateOrUpdate(update, true)
}

func toAnySlice(secs []map[string]any) []any {
	out := make([]any, len(secs))
	for i := range secs {
		out[i] = secs[i]
	}
	return out
}

// appDefUpdateSection replaces one section by id. The id is kept on the
// replacement whatever the object carried, so the handle survives the edit.
func (t *chatTurn) appDefUpdateSection(args map[string]any) (string, error) {
	id := strings.TrimSpace(stringArg(args, "section_id"))
	if id == "" {
		return "", errors.New("section_id is required: the id of the section to replace (app_def action=get lists them)")
	}
	repl, err := sectionArg(args)
	if err != nil {
		return "", err
	}
	return t.applySectionEdit(args, "update_section "+id, func(secs []map[string]any) ([]map[string]any, error) {
		i := findSectionByID(secs, id)
		if i < 0 {
			return nil, fmt.Errorf("no section with id %q, this app has: %s", id, sectionIDList(secs))
		}
		repl["id"] = id
		out := append([]map[string]any{}, secs...)
		out[i] = repl
		return out, nil
	})
}

// appDefAddSection inserts one section: at the end, first ("start"), or after
// a named section.
func (t *chatTurn) appDefAddSection(args map[string]any) (string, error) {
	add, err := sectionArg(args)
	if err != nil {
		return "", err
	}
	after := strings.TrimSpace(firstNonEmptyStr(stringArg(args, "after"), stringArg(args, "section_id")))
	return t.applySectionEdit(args, "add_section", func(secs []map[string]any) ([]map[string]any, error) {
		if id := strings.TrimSpace(mapStr(add, "id")); id != "" && findSectionByID(secs, id) >= 0 {
			return nil, fmt.Errorf("a section with id %q already exists: use update_section to change it, or give the new one a different id", id)
		}
		out := append([]map[string]any{}, secs...)
		switch {
		case after == "":
			return append(out, add), nil
		case strings.EqualFold(after, "start"):
			return append([]map[string]any{add}, out...), nil
		}
		i := findSectionByID(out, after)
		if i < 0 {
			return nil, fmt.Errorf("no section with id %q to insert after, this app has: %s", after, sectionIDList(secs))
		}
		out = append(out[:i+1], append([]map[string]any{add}, out[i+1:]...)...)
		return out, nil
	})
}

// appDefRemoveSection drops one section by id. The update path's guards
// decide whether the drop is allowed (a functional section, a field the
// records still carry).
func (t *chatTurn) appDefRemoveSection(args map[string]any) (string, error) {
	id := strings.TrimSpace(stringArg(args, "section_id"))
	if id == "" {
		return "", errors.New("section_id is required: the id of the section to remove (app_def action=get lists them)")
	}
	return t.applySectionEdit(args, "remove_section "+id, func(secs []map[string]any) ([]map[string]any, error) {
		i := findSectionByID(secs, id)
		if i < 0 {
			return nil, fmt.Errorf("no section with id %q, this app has: %s", id, sectionIDList(secs))
		}
		if len(secs) == 1 {
			return nil, errors.New("that is the app's only section: an app needs at least one; delete the app instead, or update_section it into what you want")
		}
		out := append([]map[string]any{}, secs[:i]...)
		return append(out, secs[i+1:]...), nil
	})
}

// htmlSectionTarget is what pickHTMLSection is handed: an id from section_id
// when given, else the ordinal (or id) in section.
func htmlSectionTarget(args map[string]any) any {
	if id := strings.TrimSpace(stringArg(args, "section_id")); id != "" {
		return id
	}
	return args["section"]
}
