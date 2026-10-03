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

// Adding a tier's text starts it as a copy of what the tier reads now and
// stamps the tier's model; a model swap flags it; edit and remove work.
func TestPerTierText(t *testing.T) {
	SetPromptOverrideDB(&DBase{Store: kvlite.MemStore()})
	t.Cleanup(func() { SetPromptOverrideDB(nil) })
	prevW, prevL := LiveLLMs()
	SetLiveLLMs("llama.cpp/qwen", "gemini/flash")
	t.Cleanup(func() { SetLiveLLMs(prevW, prevL) })
	b := AllPromptBlocks()[0]
	SetPromptOverride(b.Key, "everyone reads this")
	app := &PromptsApp{}
	call := func(h http.HandlerFunc, method, url, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(method, url, strings.NewReader(body)))
		return rec
	}
	if rec := call(app.handleTierAdd, http.MethodPost, "/x", `{"block":"`+b.Key+`","tier":"worker"}`); rec.Code != 200 {
		t.Fatalf("add answered %d %s", rec.Code, rec.Body.String())
	}
	o, ok := prompts.PromptTierOverride(prompts.TierWorker, b.Key)
	if !ok || o.Text != "everyone reads this" || o.Model != "llama.cpp/qwen" {
		t.Fatalf("added %+v", o)
	}
	if rec := call(app.handleTierAdd, http.MethodPost, "/x", `{"block":"`+b.Key+`","tier":"both"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown tier answered %d", rec.Code)
	}
	ref := "worker|" + b.Key
	call(app.handleTierOne, http.MethodPost, "/x?ref="+ref, `{"text":"the worker's own words"}`)
	if got := prompts.EffectivePromptTextFor(prompts.TierWorker, b.Key, b.Text); got != "the worker's own words" {
		t.Fatalf("worker reads %q", got)
	}
	SetLiveLLMs("llama.cpp/qwen-next", "gemini/flash")
	var rows []struct {
		Ref   string `json:"ref"`
		Stale bool   `json:"stale"`
	}
	json.NewDecoder(call(app.handleTierList, http.MethodGet, "/x", "").Body).Decode(&rows)
	if len(rows) != 1 || rows[0].Ref != ref || !rows[0].Stale {
		t.Fatalf("rows = %+v", rows)
	}
	call(app.handleTierDrop, http.MethodPost, "/x?ref="+ref, "")
	if got := prompts.EffectivePromptTextFor(prompts.TierWorker, b.Key, b.Text); got != "everyone reads this" {
		t.Fatalf("after remove the worker reads %q", got)
	}
}

// The per-tier dialog names its row's field; {row_key} is never filled.
func TestTierSectionFillsFromTheRow(t *testing.T) {
	raw, _ := json.Marshal(tierTextSection())
	if strings.Contains(string(raw), "{row_key}") || !strings.Contains(string(raw), "ref={ref}") {
		t.Fatal("the per-tier edit dialog does not take its row from the row's ref")
	}
}

// A tier's text that names a placeholder the block does not fill is refused
// on every way in, since it would reach the model as a raw {name}; a fresh
// text is listed as not used yet.
func TestTierTextKeepsTheBlocksPlaceholders(t *testing.T) {
	SetPromptOverrideDB(&DBase{Store: kvlite.MemStore()})
	t.Cleanup(func() { SetPromptOverrideDB(nil) })
	var b PromptBlock
	for _, x := range AllPromptBlocks() {
		if strings.Contains(x.Text, "{rounds}") {
			b = x
		}
	}
	if b.Key == "" {
		t.Skip("no registered block has a {rounds} placeholder")
	}
	app := &PromptsApp{}
	call := func(h http.HandlerFunc, method, url, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(method, url, strings.NewReader(body)))
		return rec
	}
	if rec := call(app.handleTierAdd, http.MethodPost, "/x", `{"block":"`+b.Key+`","tier":"lead"}`); rec.Code != 200 {
		t.Fatalf("add answered %d %s", rec.Code, rec.Body.String())
	}
	ref := "lead|" + b.Key
	if rec := call(app.handleTierOne, http.MethodPost, "/x?ref="+ref, `{"text":"Stop after {limit} rounds."}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "{limit}") {
		t.Fatalf("an unfillable placeholder answered %d %s", rec.Code, rec.Body.String())
	}
	if err := ApplyTierEdit(prompts.TierLead, b.Key, "Stop after {limit} rounds."); err == nil {
		t.Fatal("a promotion took an unfillable placeholder")
	}
	if rec := call(app.handleTierOne, http.MethodPost, "/x?ref="+ref, `{"text":"At most {rounds} rounds, then answer."}`); rec.Code != 200 {
		t.Fatalf("a text keeping the placeholder answered %d %s", rec.Code, rec.Body.String())
	}
	var rows []struct {
		Ref    string `json:"ref"`
		Unused bool   `json:"unused"`
	}
	json.NewDecoder(call(app.handleTierList, http.MethodGet, "/x", "").Body).Decode(&rows)
	if len(rows) != 1 || !rows[0].Unused {
		t.Fatalf("rows = %+v", rows)
	}
}

// The editor's section on the admin tab has a title: an untitled section is
// "Section 1" in the tab's rail.
func TestTheEditorSectionIsNamed(t *testing.T) {
	if s := promptsAdminSection(); s.Title != EditorTitle || s.Group != AdminTab {
		t.Fatalf("editor section: title %q, group %q", s.Title, s.Group)
	}
}
