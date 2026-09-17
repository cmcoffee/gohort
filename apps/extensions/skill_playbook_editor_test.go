package extensions

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// The form round-trips a rule: what GET flattens, a POST of the same values
// merges back to the same rule, and a nested arm travels through its toggle.
func TestPlaybookRuleFormRoundTrips(t *testing.T) {
	rule := PlaybookRule{When: []string{"stuck order"}, Fact: "queue_draining", How: "read the lag", Then: "look at the consumer",
		ElseRule: &PlaybookRule{Fact: "broker_up", How: "ping", Then: "restart consumer", Else: "page on-call"}}
	form := ruleForm(rule)
	if form["type"] != "bool" || form["else_is_rule"] != true || form["else_fact"] != "broker_up" || form["then_is_rule"] != false {
		t.Fatalf("flatten: %+v", form)
	}
	back := mergeRuleForm(PlaybookRule{}, form)
	if back.Fact != "queue_draining" || back.Then != "look at the consumer" || back.ElseRule == nil || back.ElseRule.Else != "page on-call" || back.Else != "" || len(back.When) != 1 {
		t.Fatalf("merge: %+v", back)
	}
	if p := back.Problems("rule 1", 1); len(p) != 0 {
		t.Fatalf("a round-tripped rule is sound: %v", p)
	}
	// Turning the toggle off drops the nested rule and keeps the prose arm.
	back = mergeRuleForm(back, map[string]any{"else_is_rule": false, "else": "look at the broker"})
	if back.ElseRule != nil || back.Else != "look at the broker" {
		t.Fatalf("toggle off: %+v", back)
	}
	// A partial post changes only what it carries.
	back = mergeRuleForm(back, map[string]any{"how": "read the lag over five minutes"})
	if back.How != "read the lag over five minutes" || back.Then != "look at the consumer" {
		t.Fatalf("partial merge: %+v", back)
	}
}

// A choice rule's arms are keyed by position on the form and by value on
// the rule, and changing the values in the same save keeps what still fits.
func TestPlaybookChoiceFormKeysCasesByValue(t *testing.T) {
	rule := PlaybookRule{Fact: "state", How: "check", Type: "choice", Values: []string{"up", "down"}, Cases: map[string]string{"up": "fine", "down": "bad"}}
	form := ruleForm(rule)
	if form["case_0"] != "fine" || form["case_1"] != "bad" {
		t.Fatalf("flatten: %+v", form)
	}
	back := mergeRuleForm(rule, map[string]any{"values": []any{"up", "down", "flapping"}, "case_2": "wait and re-check"})
	if len(back.Values) != 3 || back.Cases["up"] != "fine" || back.Cases["flapping"] != "wait and re-check" {
		t.Fatalf("merge: %+v", back)
	}
	// Switching kind clears the other kind's arms.
	back = mergeRuleForm(back, map[string]any{"type": "bool", "then": "x"})
	if back.Values != nil || back.Cases != nil || back.Then != "x" {
		t.Fatalf("kind switch: %+v", back)
	}
}

// The page has one section per rule with its sentence or its checklist,
// an add section, and every form posts to its own rule endpoint.
func TestPlaybookPageShape(t *testing.T) {
	skill := SkillRecord{ID: "s1", Name: "Orders", Playbook: []PlaybookRule{
		{Fact: "queue_draining", How: "read the lag", Then: "consumer", Else: "broker"},
		{Fact: "half_built", How: "not yet"},
	}}
	page := skillPlaybookPage(skill)
	if len(page.Sections) != 3 || !page.SectionNav {
		t.Fatalf("expected 2 rule sections + add, got %d", len(page.Sections))
	}
	if !strings.HasPrefix(page.Sections[0].Subtitle, "Whenever this skill is consulted, establish queue_draining") {
		t.Fatalf("a sound rule's subtitle is its sentence: %q", page.Sections[0].Subtitle)
	}
	if !strings.Contains(page.Sections[1].Subtitle, "Still missing") || !strings.Contains(page.Sections[1].Subtitle, "at least one arm") {
		t.Fatalf("an unfinished rule's subtitle is its checklist: %q", page.Sections[1].Subtitle)
	}
	fp, ok := page.Sections[1].Body.(ui.FormPanel)
	if !ok || fp.PostURL != "/extensions/api/skill-playbook?id=s1&rule=1" {
		t.Fatalf("each rule posts to its own endpoint: %+v", page.Sections[1].Body)
	}
	add, ok := page.Sections[2].Body.(ui.FormPanel)
	if !ok || add.PostURL != "/extensions/api/skill-playbook?id=s1&rule=add" || add.RedirectURL != "/extensions/skill-playbook?id=s1" {
		t.Fatalf("the add form appends and reloads: %+v", add)
	}
	// The page belongs to the Extensions app, not admin: a user edits their
	// own skills there.
	if page.BackURL != "/extensions" {
		t.Fatalf("the editor sits under Extensions, got %q", page.BackURL)
	}
}

// The app is named what it has always been called on screen, and its data
// bucket still is not — a bucket cannot be renamed in place, so the app
// follows the data. Renaming Name() without pinning StoreName would silently
// empty every user's credentials, tools and skills.
func TestExtensionsKeepsItsDataBucket(t *testing.T) {
	app := Extensions{}
	if app.Name() != "extensions" {
		t.Errorf("the app is called extensions, got %q", app.Name())
	}
	if app.StoreName() != "gateways" {
		t.Fatalf("the data bucket must stay %q, got %q", "gateways", app.StoreName())
	}
	if got := (&Extensions{}).WebPath(); got != "/extensions" {
		t.Errorf("path = %q", got)
	}
	if got := (&Extensions{}).WebName(); got != "Extensions" {
		t.Errorf("display name = %q", got)
	}
}

// Every URL the editor hands the browser is absolute.
//
// The hub links to /extensions with NO trailing slash, so a relative href on
// that page resolves against the site ROOT: "skill-playbook?id=x" became
// /skill-playbook?id=x and went nowhere. The column link, the form endpoints
// and the redirect are all absolute for that reason.
func TestPlaybookURLsAreAbsolute(t *testing.T) {
	if got := playbookEditorURL("s1"); got != "/extensions/skill-playbook?id=s1" {
		t.Fatalf("column link = %q", got)
	}
	page := skillPlaybookPage(SkillRecord{ID: "s1", Name: "Orders", Playbook: []PlaybookRule{
		{Fact: "q", How: "h", Then: "a", Else: "b"},
	}})
	for i, sec := range page.Sections {
		fp, ok := sec.Body.(ui.FormPanel)
		if !ok {
			continue
		}
		for _, u := range []string{fp.Source, fp.PostURL, fp.RedirectURL} {
			if u != "" && !strings.HasPrefix(u, "/extensions/") {
				t.Errorf("section %d has a relative URL %q — it will resolve against the wrong page", i, u)
			}
		}
	}
}

// The skill form edits the playbook in place, the way admin's does, and links
// to the visual editor beside it.
func TestSkillFormEditsThePlaybook(t *testing.T) {
	var text, link *ui.FormField
	for i, f := range userSkillFormFields() {
		switch f.Field {
		case "playbook_text":
			text = &userSkillFormFields()[i]
		case "playbook_link":
			link = &userSkillFormFields()[i]
		}
	}
	if text == nil || text.Type != "textarea" {
		t.Fatal("the rules must be editable in the form, as a textarea")
	}
	if link == nil || link.Type != "readonly" {
		t.Fatal("the form must show what the rules say, and where to edit them by question")
	}
	s := SkillRecord{ID: "s1", Playbook: []PlaybookRule{{Fact: "queue_draining", How: "h", Then: "consumer", Else: "broker"}}}
	if got := playbookText(s); !strings.Contains(got, `"fact": "queue_draining"`) {
		t.Fatalf("the textarea carries the rules as JSON, got %q", got)
	}
	if got := playbookText(SkillRecord{}); got != "" {
		t.Fatalf("no rules opens blank, not %q", got)
	}
	line := playbookEditorLine(s)
	if !strings.Contains(line, "establish queue_draining") || !strings.Contains(line, "/extensions/skill-playbook?id=s1") {
		t.Fatalf("the line says what the rules are and where the editor is, got %q", line)
	}
	if !strings.Contains(playbookEditorLine(SkillRecord{}), "No rules yet") {
		t.Fatal("an empty playbook says so")
	}
}

// The row carries a Playbook button beside Edit, and it navigates rather than
// posting. A GET button is the runtime's navigation button; anything else
// would fire a request at a page. Read from the source, the way this package
// already checks its own table wiring (tool_flag_pills_test.go).
func TestSkillRowHasAPlaybookButton(t *testing.T) {
	raw, err := os.ReadFile("extensions.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	src := string(raw)
	if !strings.Contains(src, `{Type: "button", Label: "Playbook", Method: "GET",`) {
		t.Error("no Playbook button on the skill row, or it is not a navigation button")
	}
	if !strings.Contains(src, `PostTo: "/extensions/skill-playbook?id={id}"`) {
		t.Error("the Playbook button must point at the editor, absolutely")
	}
	// The cell goes back to a plain count: one door, not a cell that is
	// secretly also a link.
	if strings.Contains(src, `{Field: "playbook", Label: "Playbook", Link:`) {
		t.Error("the column should be a count now; the button is the door")
	}
}
