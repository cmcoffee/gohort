package orchestrate

// Agent categories: the owner's grouping in the picker, one category however
// it was typed, ignored on a sub-agent, and settable from the editor and by
// Builder.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestThePickerListsAnAgentUnderItsCategory(t *testing.T) {
	agents := []AgentRecord{
		{ID: "seed-builder", Name: "Builder", Category: "Medical"},
		{ID: "a-nurse", Name: "Nurse", Category: "Medical"},
		{ID: "a-pharm", Name: "Pharmacist", Category: "medical "},
		{ID: "a-diet", Name: "Dietitian", Category: "Medical"},
		{ID: "a-law", Name: "Lawyer", Category: "Legal"},
		{ID: "a-chat", Name: "Chatty", MemoryMode: memoryModeChatbot},
		{ID: "a-plain", Name: "Plain"},
		{ID: "a-kid", Name: "Kid", OwnedBy: "a-nurse", Category: "Pediatrics"},
	}
	opts, _, subs := agentPickerOptions(agents)
	var order []string
	group := map[string]string{}
	for _, o := range opts {
		group[o.Value] = o.Group
		if len(order) == 0 || order[len(order)-1] != o.Group {
			order = append(order, o.Group)
		}
	}
	// The built-in keeps its place; one Medical group in the majority
	// spelling, whatever case or spacing each agent used.
	want := map[string]string{
		"seed-builder": "Built-in", "a-nurse": "Medical", "a-pharm": "Medical", "a-diet": "Medical",
		"a-law": "Legal", "a-chat": "Conversation Agents", "a-plain": "Specialized Agents",
	}
	for id, g := range want {
		if group[id] != g {
			t.Errorf("%s listed under %q, want %q", id, group[id], g)
		}
	}
	if got := strings.Join(order, " | "); got != "Built-in | Legal | Medical | Conversation Agents | Specialized Agents" {
		t.Errorf("group order = %s", got)
	}
	// A sub-agent stays in its parent's specialist picker; its category is
	// not a group of its own.
	if _, listed := group["a-kid"]; listed || len(subs["a-nurse"]) != 1 {
		t.Errorf("sub-agent: listed=%v subs=%v, want only under its parent", listed, subs["a-nurse"])
	}
	if got := strings.Join(categoryNames(agents), ","); got != "Legal,Medical" {
		t.Errorf("categoryNames = %q, want Legal,Medical (no sub-agent's, one spelling each)", got)
	}
}

func TestTheEditorAndBuilderSetACategory(t *testing.T) {
	app, udb := accessFixture(t, AgentRecord{ID: "a-main", Name: "Main", Category: "Medical"})
	for _, a := range []AgentRecord{
		{ID: "a-child", Name: "Child", Owner: "alice", OrchestratorPrompt: "p", OwnedBy: "a-main"},
		{ID: "a-other", Name: "Other", Owner: "alice", OrchestratorPrompt: "p"},
	} {
		if _, err := saveAgent(udb, a); err != nil {
			t.Fatal(err)
		}
	}
	render := func(id string) string {
		w := httptest.NewRecorder()
		app.renderAgentEditor(w, asUser(httptest.NewRequest("GET", "/agent/"+id, nil), "alice"), "alice", udb, id)
		return w.Body.String()
	}
	page := render("a-other")
	at := strings.Index(page, `"field":"category"`)
	if at < 0 {
		t.Fatal("the editor has no category field")
	}
	field := page[at:]
	if next := strings.Index(field[1:], `"field":`); next > 0 {
		field = field[:next+1]
	}
	if !strings.Contains(field, `"suggestions":["Medical"]`) {
		t.Errorf("the category field does not suggest the owner's categories: %s", field)
	}
	if strings.Contains(render("a-child"), `"field":"category"`) {
		t.Error("a sub-agent's editor offers a category it would ignore")
	}

	// The editor saves it by PATCH.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPatch, "/api/agents?id=a-other", bytes.NewBufferString(`{"category":"Medical"}`))
	routed(app)(w, as(t, r, "alice"))
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH category: %d %s", w.Code, w.Body.String())
	}
	if got, _ := loadAgent(udb, "a-other"); got.Category != "Medical" {
		t.Fatalf("PATCH did not save the category: %q", got.Category)
	}

	// Builder sets it on update, spacing tidied; create does not advertise it.
	sess := consolidateSession(udb)
	if _, err := (updateAgentTool{}).RunWithSession(map[string]any{"id": "a-other", "category": "  Legal   Aid "}, sess); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadAgent(udb, "a-other"); got.Category != "Legal Aid" {
		t.Errorf("update_agent category = %q, want \"Legal Aid\"", got.Category)
	}
	if _, ok := agentMutationParams(false)["category"]; ok {
		t.Error("create_agent advertises category; it is update-only to keep the catalog in budget")
	}
	if _, ok := agentMutationParams(true)["category"]; !ok {
		t.Error("update_agent does not take category")
	}
}
