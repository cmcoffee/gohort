package orchestrate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/peershare"
)

// maintainerFixture is an app owned by owner-a, maintained by maint-b, with a
// form, a table, a card carrying page code, a data source and a setting; and
// a Builder turn for each of owner-a, maint-b and other-c.
func maintainerFixture(t *testing.T) (owner, maint, other *chatTurn) {
	t.Helper()
	owner, _ = folderTestTurn(t)
	owner.user = "owner-a"
	if _, err := owner.appDefCreateOrUpdate(map[string]any{
		"name": "Tally",
		"sections": []any{
			map[string]any{"id": "entry", "kind": "form", "fields": []any{map[string]any{"field": "item"}}},
			map[string]any{"id": "list", "kind": "table", "title": "Items", "empty_text": "None yet.", "columns": []any{map[string]any{"field": "item"}}},
			map[string]any{"id": "banner", "kind": "card", "html": "<div onclick=\"go()\">Hi</div><script>function go(){}</script>"},
		},
		"data_sources": []any{map[string]any{"name": "now", "script": "print('{}')"}},
		"settings":     []any{map[string]any{"name": "units", "default": "F"}},
	}, false); err != nil {
		t.Fatal(err)
	}
	spec, ok := LoadAppSpec("owner-a", "tally")
	if !ok {
		t.Fatal("the fixture app was not saved")
	}
	spec.SharedWith, spec.Maintainers = []string{"maint-b"}, []string{"maint-b"}
	SaveAppSpec(spec)
	maint = &chatTurn{user: "maint-b", agent: owner.agent, changeAsk: approveEveryChange}
	other = &chatTurn{user: "other-c", agent: owner.agent, changeAsk: approveEveryChange}
	return owner, maint, other
}

func appDefCall(t *testing.T, turn *chatTurn, args map[string]any) (string, error) {
	t.Helper()
	return turn.appDefToolDef().Handler(context.Background(), args)
}

// A maintainer changes the app's sections in the owner's copy, the one there
// is, and the version replaced is kept under their name. Builder lists the
// app as theirs to maintain, for its owner.
func TestAMaintainerChangesASectionOfTheOwnersApp(t *testing.T) {
	_, maint, other := maintainerFixture(t)

	out, err := appDefCall(t, maint, map[string]any{"action": "list"})
	if err != nil || !strings.Contains(out, `"maintained_for":"owner-a"`) {
		t.Fatalf("the maintainer's list does not show the app as maintained for its owner: %q %v", out, err)
	}
	if out, err = appDefCall(t, maint, map[string]any{"action": "get", "id": "tally"}); err != nil || !strings.Contains(out, "Items") {
		t.Fatalf("the maintainer cannot read the app: %q %v", out, err)
	}

	out, err = appDefCall(t, maint, map[string]any{"action": "update_section", "id": "tally", "section_id": "list",
		"section_def": map[string]any{"kind": "table", "title": "Things", "empty_text": "None yet.", "columns": []any{map[string]any{"field": "item"}}}})
	if err != nil {
		t.Fatalf("update_section: %v", err)
	}
	spec, _ := LoadAppSpec("owner-a", "tally")
	if !strings.Contains(string(spec.Sections), "Things") {
		t.Fatalf("the change did not land in the owner's app: %s\n%s", spec.Sections, out)
	}
	if _, mine := LoadAppSpec("maint-b", "tally"); mine {
		t.Error("the maintainer got a copy of their own")
	}
	if revs := ListAppRevisions("owner-a", "tally"); len(revs) == 0 || !strings.Contains(revs[0].Reason, "by maint-b") {
		t.Errorf("the history does not name the maintainer: %+v", revs)
	}
	if len(spec.DataSources) != 1 || len(spec.Settings) != 1 || !spec.MaintainedBy("maint-b") {
		t.Error("a section edit changed what the owner keeps")
	}

	// Someone who maintains nothing reaches nothing.
	if _, err := appDefCall(t, other, map[string]any{"action": "update_section", "id": "tally", "section_id": "list",
		"section_def": map[string]any{"kind": "table", "title": "Mine", "columns": []any{map[string]any{"field": "item"}}}}); err == nil {
		t.Error("a non-maintainer changed the app")
	}
	// Taken off the maintainers, the maintainer reaches nothing either.
	spec.Maintainers = nil
	SaveAppSpec(spec)
	if _, err := appDefCall(t, maint, map[string]any{"action": "update_section", "id": "tally", "section_id": "list",
		"section_def": map[string]any{"kind": "table", "title": "After", "columns": []any{map[string]any{"field": "item"}}}}); err == nil {
		t.Error("a former maintainer still changed the app")
	}
}

// What stays the owner's: the scripts, the page code, the settings, the
// sharing, a delete. Each is refused by name, and the app is left as it was.
func TestAMaintainerCannotTouchWhatStaysTheOwners(t *testing.T) {
	_, maint, _ := maintainerFixture(t)
	before, _ := LoadAppSpec("owner-a", "tally")
	beforeJSON, _ := json.Marshal(before)

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"scripts", map[string]any{"action": "update", "id": "tally", "data_sources": []any{map[string]any{"name": "now", "script": "import os; print('{}')"}}}, "data_sources"},
		{"settings", map[string]any{"action": "update", "id": "tally", "settings": []any{}}, "settings"},
		{"the bound agent", map[string]any{"action": "update", "id": "tally", "agent_id": "someone"}, "agent_id"},
		{"page code by patch", map[string]any{"action": "patch_html", "id": "tally", "find": "Hi", "replace": "Bye"}, "NOT DONE"},
		{"page code by function", map[string]any{"action": "replace_function", "id": "tally", "function": "go", "replace": "function go(){ fetch('x') }"}, "NOT DONE"},
		{"delete", map[string]any{"action": "delete", "id": "tally"}, "NOT DONE"},
		{"revert", map[string]any{"action": "revert", "id": "tally"}, "NOT DONE"},
	}
	// Refused in words that say why and whose it is: an error that only says
	// "not found" leaves the maintainer guessing.
	for _, c := range cases {
		out, err := appDefCall(t, maint, c.args)
		if err != nil || !strings.Contains(out, c.want) || !strings.Contains(out, "owner-a") {
			t.Errorf("%s: not refused by name: %q %v", c.name, out, err)
		}
	}
	// Page code through a section: the card's HTML is the owner's.
	if _, err := appDefCall(t, maint, map[string]any{"action": "update_section", "id": "tally", "section_id": "banner",
		"section_def": map[string]any{"kind": "card", "html": "<script>steal()</script>"}}); err == nil || !strings.Contains(err.Error(), "page code") {
		t.Errorf("a maintainer changed the page code through a section: %v", err)
	}
	if _, err := appDefCall(t, maint, map[string]any{"action": "add_section", "id": "tally",
		"section_def": map[string]any{"kind": "html", "html": "<p>more</p>"}}); err == nil || !strings.Contains(err.Error(), "page code") {
		t.Errorf("a maintainer added page code through a section: %v", err)
	}
	after, _ := LoadAppSpec("owner-a", "tally")
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Errorf("a refused edit changed the app:\nbefore %s\nafter  %s", beforeJSON, afterJSON)
	}
}

// The save keeps a list: only the app's words, layout and sections come from
// the maintainer's version; everything else is the owner's as it was, and a
// section pointed at another pipeline is refused.
func TestAMaintainersSaveKeepsEverythingOffTheList(t *testing.T) {
	prior := AppSpec{Slug: "tally", Owner: "owner-a", Name: "Tally", AgentID: "a1", PipelineID: "p1",
		SharedWith: []string{"maint-b"}, Maintainers: []string{"maint-b"},
		DataSources: []AppDataSource{{Name: "now", Script: "print('{}')"}}, Settings: []AppSetting{{Name: "units"}},
		Sections: json.RawMessage(`[{"kind":"table","title":"Items"}]`)}
	next := prior
	next.Name, next.Desc, next.Sections = "Tally 2", "counts things", json.RawMessage(`[{"kind":"table","title":"Things"}]`)
	next.AgentID, next.PipelineID, next.Shared = "a2", "p2", true
	next.SharedWith, next.Maintainers = []string{"maint-b", "x"}, []string{"maint-b", "x"}
	next.DataSources = []AppDataSource{{Name: "now", Script: "import os"}}
	next.Settings, next.Libraries = nil, map[string]string{"engine": "x"}
	got, err := applyAppMaintainerEdit(prior, next)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Tally 2" || got.Desc != "counts things" || !strings.Contains(string(got.Sections), "Things") {
		t.Errorf("the words and sections were not taken: %+v", got)
	}
	if got.AgentID != "a1" || got.PipelineID != "p1" || got.Shared || len(got.SharedWith) != 1 || len(got.Maintainers) != 1 ||
		got.DataSources[0].Script != "print('{}')" || len(got.Settings) != 1 || got.Libraries != nil {
		t.Errorf("something off the list came from the maintainer: %+v", got)
	}
	piped := prior
	piped.Sections = json.RawMessage(`[{"kind":"pipeline","pipeline_id":"p9"}]`)
	if _, err := applyAppMaintainerEdit(prior, piped); err == nil {
		t.Error("a section pointed at another pipeline was kept")
	}
	same := prior
	same.Sections = json.RawMessage(`[{"kind":"pipeline","pipeline_id":"p1"}]`)
	if _, err := applyAppMaintainerEdit(prior, same); err != nil {
		t.Errorf("a section running the app's own pipeline was refused: %v", err)
	}
}

// The app's own list decides, not the index: an index row left behind (a
// write that missed the hook, a restore) reaches nothing.
func TestAStaleIndexRowMaintainsNothing(t *testing.T) {
	maintainerFixture(t)
	peershare.SetRecipients(RootDB, MaintainedAppsTable, "owner-a", "tally", []string{"maint-b", "other-c"})
	if _, ok := maintainedApp("other-c", "tally"); ok {
		t.Error("an index row the app's own list does not back made someone a maintainer")
	}
	if _, ok := maintainedApp("maint-b", "tally"); !ok {
		t.Error("the real maintainer lost the app")
	}
}

// Builder's starting form, opened from "Change with Builder" on an app the
// person maintains, has that app among "Which one?", or the preselected app
// would have nothing to land on.
func TestAMaintainedAppIsOnBuildersStartingForm(t *testing.T) {
	maintainerFixture(t)
	found := false
	for _, o := range authoringTargets(nil, "maint-b", "App") {
		if o.Value == "tally" {
			found = strings.Contains(o.Label, "maintained for owner-a")
		}
	}
	if !found {
		t.Errorf("the maintainer's form does not offer the app: %+v", authoringTargets(nil, "maint-b", "App"))
	}
	for _, o := range authoringTargets(nil, "other-c", "App") {
		if o.Value == "tally" {
			t.Error("someone who maintains nothing is offered the app")
		}
	}
}

// A maintainer adds and changes checks and removes none, so the check their
// change breaks cannot be cleared on the way.
func TestAMaintainerCannotRemoveACheck(t *testing.T) {
	prior := AppSpec{Slug: "tally", Owner: "owner-a"}
	_ = json.Unmarshal([]byte(`[{"name":"keeps the total","script":"now","expect":[{"exists":true}]}]`), &prior.Checks)
	next := prior
	next.Checks = nil
	if _, err := applyAppMaintainerEdit(prior, next); err == nil || !strings.Contains(err.Error(), "keeps the total") {
		t.Fatalf("a maintainer removed a check: %v", err)
	}
	more := prior
	_ = json.Unmarshal([]byte(`[{"name":"keeps the total","script":"now","expect":[{"exists":true}]},{"name":"another","script":"now","expect":[{"exists":true}]}]`), &more.Checks)
	if got, err := applyAppMaintainerEdit(prior, more); err != nil || len(got.Checks) != 2 {
		t.Errorf("a maintainer could not add a check: %v", err)
	}
}
