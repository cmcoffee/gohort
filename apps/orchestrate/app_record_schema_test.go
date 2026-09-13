package orchestrate

import (
	"strings"
	"testing"
)

func contactSections() []any {
	return []any{
		map[string]any{"kind": "form", "fields": []any{
			map[string]any{"field": "name"},
			map[string]any{"name": "email", "type": "Email"},
			map[string]any{"field": "age", "type": "number"},
		}},
		map[string]any{"kind": "table", "columns": []any{
			map[string]any{"field": "name"},
			map[string]any{"field": "added_on"},
		}, "edit_fields": []any{
			map[string]any{"field": "notes", "type": "textarea"},
		}},
	}
}

// The schema is derived from what forms write and tables read: a written field
// keeps its form type, a read-only column is "column", the key is "key".
func TestAppRecordFieldsDerivedFromSections(t *testing.T) {
	fields := appRecordFields(contactSections(), "id")
	want := map[string]string{"id": "key", "name": "text", "email": "email", "age": "number", "added_on": "column", "notes": "textarea"}
	if len(fields) != len(want) {
		t.Fatalf("fields = %v, want %v", fields, want)
	}
	for k, v := range want {
		if fields[k] != v {
			t.Errorf("%s = %q, want %q", k, fields[k], v)
		}
	}
	list := appFieldList(fields)
	if list[0] != "added_on: column" || list[len(list)-1] != "notes: textarea" {
		t.Fatalf("list not sorted: %v", list)
	}
}

// Dropping a field only strands records that carry a value for it; the key is
// never counted, and a field no row uses costs nothing to drop.
func TestAppStrandedFieldsCountsLiveValuesOnly(t *testing.T) {
	prev := appRecordFields(contactSections(), "id")
	next := map[string]string{"id": "key", "name": "text", "email": "email"} // dropped age, added_on, notes
	records := []map[string]any{
		{"id": "1", "name": "Ann", "age": 40, "notes": ""},
		{"id": "2", "name": "Bo", "age": "", "notes": "vip"},
		{"id": "3", "name": "Cy"},
	}
	stranded := appStrandedFields(prev, next, records)
	if stranded["age"] != 1 || stranded["notes"] != 1 {
		t.Fatalf("stranded = %v, want age:1 notes:1", stranded)
	}
	if _, ok := stranded["added_on"]; ok {
		t.Fatal("added_on has no live values and should not count")
	}
	if _, ok := stranded["id"]; ok {
		t.Fatal("the record key must never be reported")
	}
	msg := appStrandedFieldsMessage(stranded, "contacts")
	if !strings.Contains(msg, `"age" (1 record(s)`) || !strings.Contains(msg, "confirm_rewrite") {
		t.Fatalf("message = %q", msg)
	}
	if appStrandedFieldsMessage(nil, "contacts") != "" {
		t.Fatal("nothing stranded, nothing to say")
	}
}

// Drift in the other direction is reported, not refused: rows carrying fields
// no section names any more.
func TestAppStoredFieldsOutsideSchema(t *testing.T) {
	fields := map[string]string{"id": "key", "name": "text"}
	got := appStoredFieldsOutsideSchema(fields, []map[string]any{{"id": "1", "name": "A", "location": "x"}, {"id": "2", "legacy": true}})
	if strings.Join(got, ",") != "legacy,location" {
		t.Fatalf("got %v", got)
	}
}

// A sample key no section names is the signature of a script that will pass
// the test and read nothing live; it is flagged with the real field list.
func TestAppSampleFieldWarnings(t *testing.T) {
	fields := appRecordFields(contactSections(), "id")
	warns := appSampleFieldWarnings(fields, []map[string]any{{"name": "Ann", "emial": "a@x"}, {"name": "Bo"}})
	if len(warns) != 1 || !strings.Contains(warns[0], `"emial"`) || !strings.Contains(warns[0], "email: email") {
		t.Fatalf("warns = %v", warns)
	}
	if appSampleFieldWarnings(fields, []map[string]any{{"name": "Ann"}}) != nil {
		t.Fatal("a sample using only known fields should not warn")
	}
	if appSampleFieldWarnings(nil, []map[string]any{{"x": 1}}) != nil {
		t.Fatal("no schema, no basis to warn")
	}
}
