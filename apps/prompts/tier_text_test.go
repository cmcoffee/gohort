package prompts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/prompts"
	"github.com/cmcoffee/snugforge/kvlite"
)

func variantWorld(t *testing.T) (*PromptsApp, PromptBlock) {
	t.Helper()
	SetPromptOverrideDB(&DBase{Store: kvlite.MemStore()})
	t.Cleanup(func() { SetPromptOverrideDB(nil) })
	prevW, prevL := LiveLLMs()
	SetLiveLLMs("local/qwen at http://host.test/v1/", "cloud/flash")
	t.Cleanup(func() { SetLiveLLMs(prevW, prevL) })
	// An admin signed in: the handlers read who is asking.
	root := &DBase{Store: kvlite.MemStore()}
	prevAuth := AuthDB
	AuthDB = func() Database { return root }
	t.Cleanup(func() { AuthDB = prevAuth })
	AuthSetUser(root, "admin", "pw", true)
	adminCookie = AuthCreateSession(root, "admin")
	app := &PromptsApp{}
	app.DB = root.Bucket("prompts")
	return app, AllPromptBlocks()[0]
}

var adminCookie string

func asAdmin(r *http.Request) *http.Request {
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: adminCookie})
	return r
}

func loadVariant(t *testing.T, app *PromptsApp, key, variant string) (body, note string) {
	t.Helper()
	rec := httptest.NewRecorder()
	app.handleLoad(rec, asAdmin(httptest.NewRequest(http.MethodGet, "/x?id="+key+"&variant="+variant, nil)))
	var out struct {
		Body string `json:"Body"`
		Note string `json:"variant_note"`
	}
	json.NewDecoder(rec.Body).Decode(&out)
	return out.Body, out.Note
}

func saveVariant(t *testing.T, app *PromptsApp, key, variant, body string) int {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"ID": key, "Body": body, "variant": variant})
	rec := httptest.NewRecorder()
	app.handleSave(rec, asAdmin(httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(string(raw)))))
	return rec.Code
}

// The editor shows each block as both models read it and as each model's
// own: a model with none reads the shared text, a save to its version gives
// it its own (stamped with its model, written by hand), and saving the
// shared text back removes the split.
func TestEachModelHasItsOwnVersionInTheEditor(t *testing.T) {
	app, b := variantWorld(t)
	SetPromptOverride(b.Key, "everyone reads this")

	if body, note := loadVariant(t, app, b.Key, "worker"); body != "everyone reads this" || !strings.Contains(note, "Unchanged for the worker") {
		t.Fatalf("worker before: %q / %q", body, note)
	}
	if code := saveVariant(t, app, b.Key, "worker", "the worker's own words"); code != 200 {
		t.Fatalf("save answered %d", code)
	}
	o, ok := prompts.PromptTierOverride("worker", b.Key)
	if !ok || o.Text != "the worker's own words" || o.Model != "local/qwen at http://host.test/v1/" || o.Via != "edit" {
		t.Fatalf("worker override = %+v", o)
	}
	if got, _ := PromptOverride(b.Key); got != "everyone reads this" {
		t.Fatalf("the shared text moved: %q", got)
	}
	if body, note := loadVariant(t, app, b.Key, "worker"); body != "the worker's own words" || !strings.Contains(note, "own wording: by hand") || !strings.Contains(note, "for local/qwen.") {
		t.Fatalf("worker after: %q / %q", body, note)
	}
	if _, note := loadVariant(t, app, b.Key, "all"); !strings.Contains(note, "except the worker") {
		t.Fatalf("shared note = %q", note)
	}
	if code := saveVariant(t, app, b.Key, "worker", "everyone reads this"); code != 200 {
		t.Fatalf("save back answered %d", code)
	}
	if _, ok := prompts.PromptTierOverride("worker", b.Key); ok {
		t.Fatal("saving the shared text back left the split")
	}
}

// Optimize's wording says so, and says when the model behind the tier has
// changed since it was written.
func TestTheEditorSaysWhoWroteAModelsWording(t *testing.T) {
	app, b := variantWorld(t)
	if err := ApplyTierEdit("lead", b.Key, "lead words"); err != nil {
		t.Fatal(err)
	}
	if _, note := loadVariant(t, app, b.Key, "lead"); !strings.Contains(note, "own wording: from Optimize") || !strings.Contains(note, "for cloud/flash.") {
		t.Fatalf("lead note = %q", note)
	}
	SetLiveLLMs("local/qwen", "cloud/pro")
	if _, note := loadVariant(t, app, b.Key, "lead"); !strings.Contains(note, "now runs cloud/pro") {
		t.Fatalf("stale note = %q", note)
	}
}

// A model's wording that names a placeholder the block does not fill is
// refused: it would reach the model as a raw {name}.
func TestAModelsWordingKeepsTheBlocksPlaceholders(t *testing.T) {
	app, _ := variantWorld(t)
	var b PromptBlock
	for _, x := range AllPromptBlocks() {
		if strings.Contains(x.Text, "{rounds}") {
			b = x
		}
	}
	if b.Key == "" {
		t.Skip("no registered block has a {rounds} placeholder")
	}
	if code := saveVariant(t, app, b.Key, "lead", "Stop after {limit} rounds."); code != http.StatusBadRequest {
		t.Fatalf("an unfillable placeholder answered %d", code)
	}
	if err := ApplyTierEdit(prompts.TierLead, b.Key, "Stop after {limit} rounds."); err == nil {
		t.Fatal("Optimize could write an unfillable placeholder")
	}
	if code := saveVariant(t, app, b.Key, "lead", "At most {rounds} rounds, then answer."); code != 200 {
		t.Fatalf("a text keeping the placeholder answered %d", code)
	}
}

// The editor has a tab per model, the worker's first, and its section on the
// admin tab has a title: untitled, the tab's rail calls it "Section 1".
func TestTheEditorOffersEachModel(t *testing.T) {
	ed := promptsEditor()
	if len(ed.Variants) != 2 || ed.Variants[0].Value != "worker" || ed.Variants[1].Value != "lead" || ed.VariantNoteField != "variant_note" {
		t.Fatalf("variants = %+v", ed.Variants)
	}
	if s := promptsAdminSection(); s.Title != EditorTitle || s.Group != AdminTab {
		t.Fatalf("editor section: title %q, group %q", s.Title, s.Group)
	}
}

// The block list marks which models read wording of their own.
func TestTheListMarksEachModelsOwnWording(t *testing.T) {
	app, b := variantWorld(t)
	prompts.SetPromptTierOverrideBy("worker", b.Key, "the worker's own", "local/qwen", "tuned")
	rec := httptest.NewRecorder()
	app.handleList(rec, asAdmin(httptest.NewRequest(http.MethodGet, "/x", nil)))
	var rows []map[string]any
	json.NewDecoder(rec.Body).Decode(&rows)
	for _, r := range rows {
		badges, _ := r["Badges"].([]any)
		if r["ID"] == b.Key {
			if len(badges) != 1 || badges[0] != "worker" {
				t.Fatalf("the split block's badges = %v", r["Badges"])
			}
		} else if len(badges) != 0 {
			t.Fatalf("an unsplit block has badges: %v", r)
		}
	}
}

// The editor is on the LLMs tab, after Optimize, with a header of its own.
func TestTheEditorLivesOnTheLLMsTab(t *testing.T) {
	var found bool
	for _, e := range AdminSectionEntriesFor(httptest.NewRequest(http.MethodGet, "/admin", nil)) {
		if e.App != "/prompts" {
			continue
		}
		found = true
		if e.Section.Group != "LLMs" || e.Order <= 0 || e.Section.NoChrome || e.Section.Title != EditorTitle {
			t.Fatalf("editor entry = group %q, order %d, nochrome %v, title %q", e.Section.Group, e.Order, e.Section.NoChrome, e.Section.Title)
		}
	}
	if !found {
		t.Fatal("the editor registered no admin section")
	}
}

// Save for both makes the text what both models read, dropping each model's
// own wording; Revert to default puts both back on the shipped text. Each
// dropped wording is kept as a revision.
func TestSaveForBothAndRevertTreatBothModelsAlike(t *testing.T) {
	app, b := variantWorld(t)
	prompts.SetPromptTierOverrideBy("worker", b.Key, "worker words", "local/qwen", "tuned")
	prompts.SetPromptTierOverrideBy("lead", b.Key, "lead words", "cloud/flash", "edit")

	raw, _ := json.Marshal(map[string]any{"ID": b.Key, "Body": "both read this", "variant": "worker", "both": true})
	rec := httptest.NewRecorder()
	app.handleSave(rec, asAdmin(httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(string(raw)))))
	if rec.Code != 200 {
		t.Fatalf("save for both answered %d", rec.Code)
	}
	for _, tier := range []string{"worker", "lead"} {
		if _, own := prompts.PromptTierOverride(tier, b.Key); own {
			t.Fatalf("the %s kept its own wording", tier)
		}
		if body, note := loadVariant(t, app, b.Key, tier); body != "both read this" || !strings.Contains(note, "last saved for both") {
			t.Fatalf("%s reads %q / %q", tier, body, note)
		}
	}

	prompts.SetPromptTierOverrideBy("lead", b.Key, "lead again", "cloud/flash", "edit")
	rec = httptest.NewRecorder()
	app.handleRevert(rec, asAdmin(httptest.NewRequest(http.MethodPost, "/x?id="+b.Key, nil)))
	if _, own := prompts.PromptTierOverride("lead", b.Key); own {
		t.Fatal("revert left the lead's own wording")
	}
	if _, edited := PromptOverride(b.Key); edited {
		t.Fatal("revert left the edit for both")
	}
	if body, note := loadVariant(t, app, b.Key, "worker"); body != b.Text || !strings.Contains(note, "Unchanged for the worker: the shipped text.") {
		t.Fatalf("after revert the worker reads %q / %q", body, note)
	}
}
