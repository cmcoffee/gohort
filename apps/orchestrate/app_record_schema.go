package orchestrate

// The record schema — derived, never declared.
//
// A custom app's records have a shape, and until now nothing wrote it down:
// the form section says which fields it writes, the table section says which
// it reads, and an author revising the app had to parse both to learn either.
// Worse, an update that renamed a field stranded every stored record silently
// — the new form wrote `city`, the old rows carried `location`, and the table
// showed blanks with nothing on the authoring side to say why.
//
// So the host computes the field list from the sections on every save and
// keeps it on the spec, and update checks the fields it is about to drop
// against the records that still carry them. Refusal names the field and the
// count; confirm_rewrite punches through, the same override the html wipe
// guard uses, because sometimes the rows really are disposable.

import (
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// appRecordFields derives the record schema from a sections array: form fields
// (their type), table columns and edit fields ("column" unless a form writes
// them), a workbench's body field, and the record key.
func appRecordFields(raw any, recordKey string) map[string]string {
	out := map[string]string{}
	if k := strings.TrimSpace(recordKey); k != "" {
		out[k] = "key"
	}
	arr, ok := raw.([]any)
	if !ok {
		return out
	}
	reads := func(list any) {
		items, _ := list.([]any)
		for _, it := range items {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			f := strings.TrimSpace(firstNonEmptyStr(mapStr(m, "field"), mapStr(m, "name")))
			if f != "" {
				if _, seen := out[f]; !seen {
					out[f] = "column"
				}
			}
		}
	}
	writes := func(list any) {
		items, _ := list.([]any)
		for _, it := range items {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			f := strings.TrimSpace(firstNonEmptyStr(mapStr(m, "field"), mapStr(m, "name")))
			if f == "" {
				continue
			}
			typ := firstNonEmptyStr(strings.ToLower(strings.TrimSpace(mapStr(m, "type"))), "text")
			if cur, seen := out[f]; !seen || cur == "column" {
				out[f] = typ
			}
		}
	}
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		m = normalizeSection(m)
		switch strings.ToLower(strings.TrimSpace(mapStr(m, "kind"))) {
		case "form":
			writes(m["fields"])
		case "table":
			reads(m["columns"])
			writes(m["edit_fields"])
		case "display":
			reads(m["pairs"])
			reads(m["fields"])
		case "workbench":
			body := firstNonEmptyStr(strings.TrimSpace(mapStr(m, "body_field")), "content")
			if _, seen := out[body]; !seen {
				out[body] = "text"
			}
			if t := strings.TrimSpace(mapStr(m, "title_field")); t != "" {
				if _, seen := out[t]; !seen {
					out[t] = "text"
				}
			}
		}
	}
	return out
}

// appFieldList renders a schema for a tool result: sorted, "name: type".
func appFieldList(fields map[string]string) []string {
	out := make([]string, 0, len(fields))
	for name, typ := range fields {
		out = append(out, name+": "+typ)
	}
	sort.Strings(out)
	return out
}

// appStoredRecords reads an app's live records from wherever they live — the
// shared per-user store, or the app's own file when PrivateDB is set. The
// script check used to read the shared store unconditionally, so a private-db
// app tested against an empty set even when it had rows.
func appStoredRecords(user string, spec AppSpec) []map[string]any {
	var db Database
	if spec.PrivateDB {
		db = OpenCustomAppDB(user, spec.Slug)
	} else {
		db = UserDB(RootDB, user)
	}
	if db == nil {
		return nil
	}
	tbl := "custom_records:" + spec.Slug
	var recs []map[string]any
	for _, k := range db.Keys(tbl) {
		var rec map[string]any
		if db.Get(tbl, k, &rec) {
			recs = append(recs, rec)
		}
	}
	return recs
}

// appStrandedFields reports, for each field in prev but not in next, how many
// of the records still carry a non-empty value for it. The record key is never
// counted — it cannot be dropped by a section change. A field with no live
// values is not stranded; dropping it costs nothing.
func appStrandedFields(prev, next map[string]string, records []map[string]any) map[string]int {
	out := map[string]int{}
	for name, typ := range prev {
		if typ == "key" {
			continue
		}
		if _, kept := next[name]; kept {
			continue
		}
		n := 0
		for _, r := range records {
			if v, ok := r[name]; ok && strings.TrimSpace(fmt.Sprint(v)) != "" {
				n++
			}
		}
		if n > 0 {
			out[name] = n
		}
	}
	return out
}

// appStrandedFieldsMessage renders the refusal. Sorted so the same drop reads
// the same way twice.
func appStrandedFieldsMessage(stranded map[string]int, slug string) string {
	if len(stranded) == 0 {
		return ""
	}
	names := make([]string, 0, len(stranded))
	for n := range stranded {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = fmt.Sprintf("%q (%d record(s) carry a value)", n, stranded[n])
	}
	return fmt.Sprintf("this update drops field(s) the stored records still use: %s. The rows keep the data but nothing will show or edit it, and a form that writes a renamed field leaves the old values behind. Keep the field (a table column is enough), migrate the records first, or pass confirm_rewrite:true if those values really are disposable (app_def action=get, id=%q shows the current fields)", strings.Join(parts, "; "), slug)
}

// appStoredFieldsOutsideSchema lists fields the live records carry that no
// section names — the drift in the other direction: a form once wrote them
// and no longer does, or an action wrote extras. Reported, never refused.
func appStoredFieldsOutsideSchema(fields map[string]string, records []map[string]any) []string {
	seen := map[string]bool{}
	for _, r := range records {
		for k := range r {
			if _, named := fields[k]; !named && !seen[k] {
				seen[k] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// appSampleFieldWarnings compares a sample's keys with the schema: a key no
// section names is usually a typo for the field the form actually writes, and
// a data source that reads the sample's spelling passes the test and fails
// live. One line per stray key.
func appSampleFieldWarnings(fields map[string]string, sample []map[string]any) []string {
	if len(fields) == 0 || len(sample) == 0 {
		return nil
	}
	stray := map[string]bool{}
	for _, rec := range sample {
		for k := range rec {
			if _, named := fields[k]; !named {
				stray[k] = true
			}
		}
	}
	if len(stray) == 0 {
		return nil
	}
	keys := make([]string, 0, len(stray))
	for k := range stray {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("WARN sample key %q is not a field any section writes or reads (fields: %s), a script that reads it will pass here and see nothing live.", k, strings.Join(appFieldList(fields), ", ")))
	}
	return out
}
