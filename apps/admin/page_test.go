package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// adminPageSource is the admin page as one text: page.go plus every
// page_<area>.go builder and the web_assets.go blobs. The source-reading tests
// below used to open page.go alone, which held the whole page until it was cut
// into one file per tab area; a check that counts forms or looks for a help
// string has to see all of them or it reads a split as a regression.
func adminPageSource(t *testing.T) string {
	t.Helper()
	names, err := filepath.Glob("page*.go")
	if err != nil {
		t.Fatal(err)
	}
	names = append(names, "web_assets.go")
	sort.Strings(names)
	var all []byte
	for _, n := range names {
		if filepath.Ext(n) != ".go" || len(n) > 8 && n[len(n)-8:] == "_test.go" {
			continue
		}
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, b...)
		all = append(all, '\n')
	}
	return string(all)
}

// TestToggleLabel: the redundant 0=off suffix is stripped from a bool tunable's
// label (any spacing / casing), while a plain label and a non-matching
// parenthetical are left untouched.
func TestToggleLabel(t *testing.T) {
	cases := map[string]string{
		"Finding conflict detection (0 = off)":  "Finding conflict detection",
		"Automatic entity extraction (0 = off)": "Automatic entity extraction",
		"Something (0=off)":                     "Something",
		"Something (0 = OFF)":                   "Something",
		"Grounding gate: unsourced figures":     "Grounding gate: unsourced figures",
		"Recall min score (0 = off)":            "Recall min score", // helper strips; caller only applies it to bool knobs
		"Chunk size (chars)":                    "Chunk size (chars)",
	}
	for in, want := range cases {
		if got := toggleLabel(in); got != want {
			t.Errorf("toggleLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// No browser prompt on an administrator page: a list to pick from is asked
// through the shared modal (uiChoose), so it carries the app's chrome.
func TestAdminPagesAskThroughTheSharedModal(t *testing.T) {
	for _, src := range []string{configureBackendPickAction, connectorFormDef} {
		if strings.Contains(src, "window.prompt(") || strings.Contains(src, "window.confirm(") {
			t.Errorf("an administrator page still uses a browser dialog:\n%s", src[:200])
		}
	}
}

// An app's pane shows the knobs it has claimed as a form over the same keys
// the Tuning tab writes. The ?app= address is scoped in both directions: it
// reads only that app's knobs, and it saves and reverts only those.

func tuningTestApp(t *testing.T) (*AdminApp, func(method, url, body string) *httptest.ResponseRecorder) {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	SetTunablesDB(db)
	t.Cleanup(func() { SetTunablesDB(nil) })
	a := &AdminApp{db: db}
	mux := http.NewServeMux()
	a.registerSystemRoutes(mux)
	return a, func(method, url, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, url, strings.NewReader(body)))
		return w
	}
}

func init() {
	RegisterTunable(TunableSpec{Key: "tune_panetune_mine", Category: "Limits", Label: "Mine",
		App: "/panetune", Kind: KindInt, Default: 5, Min: 1, Max: 50})
	RegisterTunable(TunableSpec{Key: "tune_panetune_flag", Category: "Timeouts", Label: "Flag",
		App: "/panetune", Kind: KindBool, Default: 0, Min: 0, Max: 1})
	RegisterTunable(TunableSpec{Key: "tune_panetune_theirs", Category: "Limits", Label: "Theirs",
		App: "/otherpane", Kind: KindInt, Default: 5, Min: 1, Max: 50})
}

func TestAppTuningReadsOnlyItsOwnKnobs(t *testing.T) {
	_, call := tuningTestApp(t)
	w := call("GET", "/api/settings?app=/panetune", "")
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	if len(got) != 2 || got["tune_panetune_mine"] != float64(5) || got["tune_panetune_flag"] != false {
		t.Errorf("app view = %v, want exactly its two knobs at their effective values", got)
	}
}

// The scoped address must not quietly accept everything the unscoped one does:
// a URL that reads as "this app's settings" writing site settings is a promise
// it does not keep.
func TestAppTuningSavesOnlyItsOwnKnobs(t *testing.T) {
	a, call := tuningTestApp(t)
	call("PATCH", "/api/settings?app=/panetune",
		`{"tune_panetune_mine": 9, "tune_panetune_theirs": 9, "allow_signup": true}`)

	if TuneInt("tune_panetune_mine") != 9 {
		t.Errorf("own knob = %d, want 9", TuneInt("tune_panetune_mine"))
	}
	if TuneInt("tune_panetune_theirs") != 5 {
		t.Errorf("another app's knob was written through this app's pane: %d", TuneInt("tune_panetune_theirs"))
	}
	var signup bool
	if a.db.Get(WebTable, "allow_signup", &signup) {
		t.Error("a site setting was written through an app's pane")
	}
}

func TestAppTuningRevertsOnlyItsOwnKnobs(t *testing.T) {
	a, call := tuningTestApp(t)
	a.db.Set(WebTable, "tune_panetune_mine", float64(9))
	a.db.Set(WebTable, "tune_panetune_theirs", float64(9))
	InvalidateTunables()

	if c := call("POST", "/api/settings/reset-tunables?app=/panetune", "").Code; c/100 != 2 {
		t.Fatalf("revert: %d", c)
	}
	if TuneInt("tune_panetune_mine") != 5 {
		t.Errorf("own knob not reverted: %d", TuneInt("tune_panetune_mine"))
	}
	if TuneInt("tune_panetune_theirs") != 9 {
		t.Error("an app's revert reached another app's knob")
	}

	// An app that claims nothing reverts nothing. It must not fall through to
	// the unscoped revert, which clears every knob on the deployment.
	call("POST", "/api/settings/reset-tunables?app=/claims-nothing", "")
	if TuneInt("tune_panetune_theirs") != 9 {
		t.Error("a revert for an app with no knobs reverted everything")
	}
}

func TestAppTuningFormShape(t *testing.T) {
	if appTuningForm("/claims-nothing") != nil {
		t.Error("an app with no knobs should get no form")
	}
	f := appTuningForm("/panetune")
	if f == nil {
		t.Fatal("no form for an app with claimed knobs")
	}
	if f.Source != tuningSourceForApp("/panetune") {
		t.Errorf("source = %q", f.Source)
	}
	// Saving to api/settings would announce api/settings, which is what this
	// form listens for, and it would reload after each of its own edits.
	if f.PostURL != "" {
		t.Errorf("post url = %q: saves must go to the form's own scoped source", f.PostURL)
	}
	if len(f.RefreshOn) != 1 || f.RefreshOn[0] != "api/settings" {
		t.Errorf("refresh_on = %v, want the Tuning tab's source", f.RefreshOn)
	}
	// Grouped under the category names the Tuning tab uses.
	var headers []string
	for _, fl := range f.Fields {
		if fl.Type == "header" {
			headers = append(headers, fl.Label)
		}
	}
	if strings.Join(headers, ",") != "Limits,Timeouts" {
		t.Errorf("headers = %v", headers)
	}
}

// The Tuning tab's category form must reload when an app pane holding one of
// its knobs saves, or the two show different values for one key.
func TestTuningCategoryListensToAppPanes(t *testing.T) {
	RegisterApp(fakeWebApp{path: "/panetunelisted"})
	RegisterTunable(TunableSpec{Key: "tune_panetunelisted", Category: "PaneTuneCat", Label: "L",
		App: "/panetunelisted", Kind: KindInt, Default: 1, Min: 1, Max: 9})

	want := tuningSourceForApp("/panetunelisted")
	for _, sec := range buildTunableSections() {
		if sec.Title != "PaneTuneCat" {
			continue
		}
		fp, ok := sec.Body.(ui.FormPanel)
		if !ok {
			t.Fatalf("category body = %T", sec.Body)
		}
		for _, src := range fp.RefreshOn {
			if src == want {
				return
			}
		}
		t.Fatalf("category form refresh_on = %v, missing %q", fp.RefreshOn, want)
	}
	t.Fatal("category section not built")
}
