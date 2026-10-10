package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/core/recipes"
	"github.com/cmcoffee/snugforge/kvlite"
)

// saveTemplateFixture: root is an administrator, bob is not, and root has a
// tool built (through the REST call template) and approved, to package.
func saveTemplateFixture(t *testing.T) Database {
	t.Helper()
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	db := pinRootDB(t)
	if _, err := recipes.Install(db, "rest-call", "root", map[string]string{
		"name": "acme_get_order", "description": "Look up one order.",
		"url": "https://api.acme.example/v1/orders/{id}",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ApprovePendingTempTool(db, "root", "acme_get_order"); err != nil {
		t.Fatal(err)
	}
	return db
}

func findTool(tools []AgentToolDef, name string) (AgentToolDef, bool) {
	for _, td := range tools {
		if td.Tool.Name == name {
			return td, true
		}
	}
	return AgentToolDef{}, false
}

// Only an administrator's Builder has it.
func TestSaveTemplateIsAnAdministratorsOnly(t *testing.T) {
	saveTemplateFixture(t)
	turn, sess := newAuthoringTestTurn(t)
	turn.user = "root"
	if _, ok := findTool(builderAuthoringTools(&ToolSession{Username: "root", DB: sess.DB}, turn), saveTemplateToolName); !ok {
		t.Error("an administrator's Builder cannot save a template")
	}
	turn.user = "bob"
	if _, ok := findTool(builderAuthoringTools(&ToolSession{Username: "bob", DB: sess.DB}, turn), saveTemplateToolName); ok {
		t.Error("a non-administrator's Builder can save a template")
	}
	// And the handler checks again, for a definition that reached someone
	// it should not have.
	out, err := saveTemplateToolDef("bob").Handler(context.Background(), map[string]any{"title": "x", "pieces": []any{}})
	if err != nil || !strings.Contains(out, "administrator") {
		t.Errorf("a non-administrator's call was not refused: %q %v", out, err)
	}
	out, err = saveTemplateToolDef("root").Handler(withNonOwnerRequester(context.Background(), "chat-1"), map[string]any{"title": "x", "pieces": []any{}})
	if err != nil || !strings.Contains(out, "administrator") {
		t.Errorf("a stranger on a channel reached it: %q %v", out, err)
	}
}

// Every call stops for the administrator: the web hook finds the question
// (it used to look only among an app's tools, so a framework tool's question
// was never asked), and there is no standing yes.
func TestSaveTemplateAlwaysAsksFirst(t *testing.T) {
	def := saveTemplateToolDef("root")
	if !def.Confirmation.Asks() || !def.Confirmation.NeverRemember {
		t.Fatalf("the tool does not ask, or offers always-allow: %+v", def.Confirmation)
	}
	turn := &chatTurn{user: "root"}
	if spec := turn.appToolConfirmation(saveTemplateToolName); !spec.Asks() {
		t.Error("the web confirm hook does not find save_template's question, so the call would run unasked")
	}
	if spec := turn.appToolConfirmation("tool_def"); spec.Asks() {
		t.Error("a framework tool that declared no question is now asked about")
	}
	if !IsReservedToolName(saveTemplateToolName) {
		t.Error("save_template is not reserved: a custom tool could take its name and its question")
	}
}

// A save packages the pieces, turns each value into a question, and installs
// nothing.
func TestSaveTemplatePackagesWhatWasBuilt(t *testing.T) {
	db := saveTemplateFixture(t)
	out, err := saveTemplateToolDef("root").Handler(context.Background(), map[string]any{
		"title": "Acme orders", "description": "Look up Acme orders.",
		"pieces":    []any{map[string]any{"type": "tool", "name": "acme_get_order"}},
		"questions": []any{map[string]any{"name": "site", "label": "Acme API address", "value": "https://api.acme.example", "kind": "url", "required": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "acme-orders") || !strings.Contains(out, "Nothing was installed") {
		t.Errorf("the reply does not say what was saved: %q", out)
	}
	rec, src, ok := recipes.Get(db, "acme-orders")
	if !ok || src != recipes.Imported {
		t.Fatalf("the template was not saved as one of this deployment's: %v %q", ok, src)
	}
	if len(rec.Questions) != 1 || rec.Questions[0].Name != "site" || !rec.Questions[0].Required {
		t.Errorf("questions: %+v", rec.Questions)
	}
	if len(rec.Bundle.Artifacts) != 1 || !strings.Contains(string(rec.Bundle.Artifacts[0].Recipe), "{{site}}/v1/orders/{id}") {
		t.Errorf("the address did not become the question: %s", rec.Bundle.Artifacts)
	}
	// It is a normal template: it adds elsewhere with the answer filled in.
	bundle, _, _, err := recipes.Fill(rec, map[string]string{"site": "https://api.other.example"})
	if err != nil || !strings.Contains(string(bundle.Artifacts[0].Recipe), "https://api.other.example/v1/orders/{id}") {
		t.Errorf("the saved template does not fill: %v", err)
	}
}

// What it cannot find, it names, so the next call is not a guess.
func TestSaveTemplateNamesWhatIsMissing(t *testing.T) {
	saveTemplateFixture(t)
	h := saveTemplateToolDef("root").Handler
	_, err := h(context.Background(), map[string]any{"title": "x", "pieces": []any{map[string]any{"type": "tool", "name": "nope"}}})
	if err == nil || !strings.Contains(err.Error(), "acme_get_order") {
		t.Errorf("a missing tool was not answered with the tools there are: %v", err)
	}
	_, err = h(context.Background(), map[string]any{"title": "x",
		"pieces":    []any{map[string]any{"type": "tool", "name": "acme_get_order"}},
		"questions": []any{map[string]any{"name": "site"}}})
	if err == nil || !strings.Contains(err.Error(), "value") {
		t.Errorf("a question with nothing to replace was accepted: %v", err)
	}
	if _, err := h(context.Background(), map[string]any{"title": "x"}); err == nil {
		t.Error("a template with no pieces was accepted")
	}
	// A tool still waiting for review is named as that, not as missing.
	if _, err := recipes.Install(RootDB, "rest-call", "root", map[string]string{
		"name": "acme_list_orders", "description": "List orders.", "url": "https://api.acme.example/v1/orders",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = h(context.Background(), map[string]any{"title": "x", "pieces": []any{map[string]any{"type": "tool", "name": "acme_list_orders"}}})
	if err == nil || !strings.Contains(err.Error(), "waiting for an administrator's approval") {
		t.Errorf("a pending tool was not named as pending: %v", err)
	}
}
