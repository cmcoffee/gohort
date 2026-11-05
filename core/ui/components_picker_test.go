package ui

import (
	"encoding/json"
	"strings"
	"testing"
)

// A picker with levels hands them to the page as they were given, in order,
// beside the base level's name; the on/off flag stays for pickers that use it.
func TestAnACLPickerCarriesItsLevels(t *testing.T) {
	cp := ACLPicker(ACLPickerConfig{
		RecordSource: "/r", Field: "allowed_users", PostTo: "/r", Method: "PATCH",
		FlagOffLabel: "Read",
		Levels: []PickerLevel{
			{Field: "contributors", Label: "Add documents", Help: "adds"},
			{Field: "maintainers", Label: "Maintain"},
		},
	})
	b, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{`"flag_off_label":"Read"`,
		`"levels":[{"field":"contributors","label":"Add documents","help":"adds"},{"field":"maintainers","label":"Maintain"}]`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if flag, _ := json.Marshal(ACLPicker(ACLPickerConfig{FlagField: "writers", FlagLabel: "Write"})); strings.Contains(string(flag), `"levels"`) {
		t.Errorf("a flag picker grew levels: %s", flag)
	}
}

// Each level is its own field and a member is in exactly one: the page reads
// a member at the highest level that names them, and every save writes each
// level's field with the members at that level alone, so moving somebody
// down or taking them off leaves no name behind in another level.
func TestAPickerSavesEachMemberAtOneLevel(t *testing.T) {
	src := readRuntimeFile(t, "10_basics.js")
	for _, want := range []string{
		"levelOf[v] = Math.max(levelOf[v] || 0, i + 1)",
		"var at = selected.filter(function(v){ return (levelOf[v] || 0) === i + 1; });",
		"body[lv.field] = at;",
		"levelOf[v] = was;", // a failed save puts the level back
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the picker does not %q", want)
		}
	}
}
