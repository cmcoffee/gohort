package customapps

// What an app keeps for each person between opens, and only what it chooses
// to keep (docs/design/apps-feel-native.md, "Fresh starts").
//
// Every open of an app is a fresh run, reloads included, as with an app of
// its own: its page's localStorage lasts for that visit and no longer. It
// used to be kept on the server and put back on every open, on every device,
// so an app always opened where it was left whether or not it meant to, and
// a saved state that broke the page needed a Reset view to get out of. An
// app that wants a person to carry on where they left off offers it: it
// saves what it needs with app.state.set and reads it with app.state.get, and
// decides on load what to do with it ("Continue where you left off?"), or it
// keeps it in its records.
//
// app.state is per person and per app, on every device, capped, and listed
// on the app's Settings page with a Clear. It is the same table the old
// saved view used, so a view saved before this reads back through it.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/apppage"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/netgate"
	"github.com/cmcoffee/gohort/core/ui"
)

const (
	viewStateTable = "app_view_state"
	// viewStateMax is the most one person's app.state of one app may hold,
	// keys and values together.
	viewStateMax = 256 << 10
)

// loadViewState is what the app keeps for the person (app.state), never nil.
func loadViewState(appdb Database, slug string) map[string]string {
	out := map[string]string{}
	if appdb != nil {
		appdb.Get(viewStateTable, slug, &out)
	}
	if out == nil {
		out = map[string]string{}
	}
	return out
}

func viewStateSize(m map[string]string) int {
	n := 0
	for k, v := range m {
		n += len(k) + len(v)
	}
	return n
}

// viewStateChange is what app.state sends: cleared first, then removed, then
// set.
type viewStateChange struct {
	Clear  bool              `json:"clear"`
	Remove []string          `json:"remove"`
	Set    map[string]string `json:"set"`
}

// viewState is what the app keeps for the person.
func (T *CustomApps) viewState(a *appCall) (any, error) {
	return loadViewState(a.appdb, a.spec.Slug), nil
}

// saveViewState merges a change into what the app keeps for the person.
func (T *CustomApps) saveViewState(a *appCall, ch viewStateChange) (any, error) {
	appdb, spec := a.appdb, a.spec
	state := loadViewState(appdb, spec.Slug)
	if ch.Clear {
		state = map[string]string{}
	}
	for _, k := range ch.Remove {
		delete(state, k)
	}
	for k, v := range ch.Set {
		state[k] = v
	}
	if n := viewStateSize(state); n > viewStateMax {
		Debug("[customapps] %s: app.state of %d bytes refused (limit %d)", spec.Slug, n, viewStateMax)
		return nil, jsonapi.Status(http.StatusRequestEntityTooLarge, fmt.Sprintf("app.state would hold %d KB, over the %d KB an app may keep for one person: keep the app's data in app.records, and in app.state only what lets them carry on", n>>10, viewStateMax>>10))
	}
	if len(state) == 0 {
		appdb.Unset(viewStateTable, spec.Slug)
	} else {
		appdb.Set(viewStateTable, spec.Slug, state)
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleViewReset is the Settings page's Clear: GET asks first, saying what
// goes and what stays; POST forgets what the app keeps for the person.
func (T *CustomApps) handleViewReset(w http.ResponseWriter, r *http.Request, appdb Database, spec AppSpec) {
	base := T.WebPath() + "/" + spec.Slug
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodPost:
		// From this site's own page only: a page elsewhere cannot clear it.
		if netgate.CrossSite(r) {
			jsonapi.Fail(w, r, jsonapi.Forbidden("clear it from this site's own page"))
			return
		}
		appdb.Unset(viewStateTable, spec.Slug)
		jsonapi.Write(w, map[string]string{"message": "What " + spec.Name + " saved for you was cleared."})
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	what := spec.Name + " has saved nothing for you, so there is nothing to clear."
	if len(loadViewState(appdb, spec.Slug)) > 0 {
		what = "This clears what " + spec.Name + " saved so you could carry on where you left off, on every device. " +
			"The app's records are kept."
	}
	ui.Page{
		Title:     "Clear what it saved",
		ShowTitle: true,
		BackURL:   base + "/",
		MaxWidth:  "560px",
		Sections: []ui.Section{{
			Title:    "Clear what " + spec.Name + " saved for you?",
			Subtitle: what,
			Body: ui.FormPanel{
				PostURL:        base + "/_view/reset",
				Method:         "POST",
				SubmitLabel:    "Clear",
				RedirectURL:    base + "/",
				RedirectTarget: "_self",
			},
		}},
	}.ServeHTTP(w, r)
}

// freshStorageScript is the frame's localStorage for one visit: the frame has
// no origin, so the browser's own cannot exist there, and this stands in for
// it, starting empty and forgetting everything when the page goes. What an
// app wants kept between visits it keeps with app.state.
const freshStorageScript = `<script>(function(){var d={};` +
	`function own(k){return Object.prototype.hasOwnProperty.call(d,k);}` +
	`var s={getItem:function(k){k=String(k);return own(k)?d[k]:null;},` +
	`setItem:function(k,v){d[String(k)]=String(v);},` +
	`removeItem:function(k){delete d[String(k)];},` +
	`clear:function(){d={};},` +
	`key:function(i){var ks=Object.keys(d);return i<ks.length?ks[i]:null;},` +
	`get length(){return Object.keys(d).length;}};` +
	`try{Object.defineProperty(window,"localStorage",{value:s,configurable:true});}catch(e){}` +
	`})();</script>`

// withFreshStorage gives every isolated frame on the page its own empty
// localStorage, after the page helper.
func withFreshStorage(page json.RawMessage) json.RawMessage {
	var v any
	if json.Unmarshal(page, &v) != nil {
		return page
	}
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			if iso, _ := t["isolate"].(bool); iso {
				pre, _ := t["isolate_prelude"].(string)
				t["isolate_prelude"] = pre + freshStorageScript
			}
			for _, c := range t {
				walk(c)
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		}
	}
	walk(v)
	out, err := json.Marshal(v)
	if err != nil {
		return page
	}
	return out
}

// viewSection is the Settings page's "What it saved for you", when the app
// keeps something for the person with app.state; nil otherwise.
func viewSection(appdb Database, spec AppSpec, base string) *ui.Section {
	state := loadViewState(appdb, spec.Slug)
	if len(state) == 0 {
		return nil
	}
	return &ui.Section{
		Title:    "What it saved for you",
		Subtitle: fmt.Sprintf("%s keeps %d item(s) so you can carry on where you left off, on every device. Clear them to start it as if new; its records are kept.", spec.Name, len(state)),
		Body:     ui.Toolbar{Actions: []ui.ToolbarAction{{Label: "Clear", Method: "GET", URL: base + "/_view/reset"}}},
	}
}

// keepsBetweenVisits reports whether the app's own scripts (its page, and
// the script and page files it keeps as assets) still keep something in
// localStorage for a next visit and never use app.state: an app written
// before every open started fresh, which now forgets where it was left.
// My Apps says so to its owner, until it is changed.
func keepsBetweenVisits(spec AppSpec) bool {
	var b strings.Builder
	// The page's text, not its JSON: stored, "<script>" is
	// "\u003cscript\u003e", which runs a letter into the name looked for.
	pageStrings(spec.Page, &b)
	pageStrings(spec.UserPage, &b)
	names, _ := ListAppAssets(spec.Owner, spec.Slug)
	for _, name := range names {
		switch strings.ToLower(path.Ext(name)) {
		case ".js", ".mjs", ".html", ".htm":
			if data, _, err := ReadAppAsset(spec.Owner, spec.Slug, name); err == nil {
				b.WriteString("\n")
				b.Write(data)
			}
		}
	}
	return apppage.KeepsInLocalStorage(b.String())
}

// noteFreshStart marks a My Apps row whose app now opens fresh
// (keepsBetweenVisits), for Fix with Builder, and adds it to the row's note.
// Rows for whoever can change the app: its owner and its maintainers.
func noteFreshStart(row map[string]string, spec AppSpec) {
	if !keepsBetweenVisits(spec) {
		return
	}
	row["fresh_start"] = "1"
	if row["note"] != "" {
		row["note"] += " - " + freshStartNote
	} else {
		row["note"] = freshStartNote
	}
}

// pageStrings writes every string in a page's JSON to b, a line each.
func pageStrings(page json.RawMessage, b *strings.Builder) {
	var v any
	if len(page) == 0 || json.Unmarshal(page, &v) != nil {
		return
	}
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			b.WriteString(t + "\n")
		case map[string]any:
			for _, c := range t {
				walk(c)
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		}
	}
	walk(v)
}

const (
	// freshStartNote is My Apps' note on such an app.
	freshStartNote = "opens fresh now: Fix with Builder has it offer to carry on where you left off"
	// freshStartChange is what Fix with Builder asks for.
	freshStartChange = "Every open of an app now starts fresh, a reload included: localStorage lasts one visit. " +
		"This app keeps where the person was in localStorage, so it now forgets it. " +
		"Move what would let them carry on (the step, tab or item they were on, an unsent draft) to app.state.set, " +
		"and on load read it with app.state.get and offer it (\"Continue where you left off?\") rather than restoring it unasked. " +
		"Keep localStorage only for what matters within one visit, or drop it."
)
