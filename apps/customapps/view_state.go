package customapps

// Where each person left an app: its page's localStorage, kept on the server.
//
// An app's page runs in an isolated frame with no origin, where the browser's
// own localStorage cannot exist, so the frame used to get an in-memory stand-in
// that forgot everything on reload: the tab, the step, the selection, a
// half-typed form. Now the stand-in is backed here, per person and per app.
// The page is served with the person's saved map already inside the frame
// (viewStateScript), so localStorage reads work from its first line, and the
// frame sends what changed back to view-state a quarter second after a change
// and as the page closes. Changes travel as a merge, not the whole map, so two
// frames on one page do not overwrite each other.
//
// It is the person's view of the app, not the app's data: records hold what
// the app is about, this holds where they were in it. Capped, and cleared by
// the page's Reset view link, so a saved state that breaks the page cannot
// trap anyone in it.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

const (
	viewStateTable = "app_view_state"
	// viewStateMax is the most one person's saved view of one app may hold,
	// keys and values together.
	viewStateMax = 256 << 10
)

// loadViewState is the person's saved view of the app, never nil.
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

// viewStateChange is what the frame sends: cleared first, then removed, then
// set, the order its calls were made in when one batch holds all three.
type viewStateChange struct {
	Clear  bool              `json:"clear"`
	Remove []string          `json:"remove"`
	Set    map[string]string `json:"set"`
}

// handleViewState saves a change to the person's view of the app (POST), or
// returns it (GET).
func (T *CustomApps) handleViewState(w http.ResponseWriter, r *http.Request, appdb Database, spec AppSpec) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, loadViewState(appdb, spec.Slug))
		return
	case http.MethodPost:
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var ch viewStateChange
	if err := json.NewDecoder(io.LimitReader(r.Body, viewStateMax*2)).Decode(&ch); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
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
		Debug("[customapps] %s: view state of %d bytes refused (limit %d)", spec.Slug, n, viewStateMax)
		http.Error(w, fmt.Sprintf("the page's saved state would be %d KB, over the %d KB an app may keep for one person: keep records in app.records, and only where the person was in localStorage", n>>10, viewStateMax>>10), http.StatusRequestEntityTooLarge)
		return
	}
	if len(state) == 0 {
		appdb.Unset(viewStateTable, spec.Slug)
	} else {
		appdb.Set(viewStateTable, spec.Slug, state)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleViewReset forgets the person's view of the app and opens it fresh.
func (T *CustomApps) handleViewReset(w http.ResponseWriter, r *http.Request, appdb Database, spec AppSpec) {
	appdb.Unset(viewStateTable, spec.Slug)
	http.Redirect(w, r, T.WebPath()+"/"+spec.Slug+"/", http.StatusFound)
}

// viewStateScript is the frame's localStorage, holding state, saving its
// changes to view-state a quarter second after the first, and at once as the
// page hides. Soon, because the save as the page hides goes through the page
// around the frame, which is closing too and often never sends it: a quick
// save is what survives a reload.
func viewStateScript(state map[string]string) string {
	b, _ := json.Marshal(state) // escapes <, > and & for a script body
	js := strings.ReplaceAll(string(b), " ", ` `)
	js = strings.ReplaceAll(js, " ", ` `)
	return `<script>(function(){var d=` + js + `,pend={},rm={},clr=false,t=null;` +
		`function own(k){return Object.prototype.hasOwnProperty.call(d,k);}` +
		`function flush(keep){if(t){clearTimeout(t);t=null;}var ks=Object.keys(rm);if(!clr&&!ks.length&&!Object.keys(pend).length)return;` +
		`var b={clear:clr,remove:ks,set:pend};pend={};rm={};clr=false;` +
		`try{fetch("view-state",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(b),keepalive:!!keep,quiet:true}).catch(function(){});}catch(e){}}` +
		`function later(){if(!t)t=setTimeout(function(){t=null;flush(false);},250);}` +
		`var s={getItem:function(k){k=String(k);return own(k)?d[k]:null;},` +
		`setItem:function(k,v){k=String(k);v=String(v);if(own(k)&&d[k]===v)return;d[k]=v;pend[k]=v;delete rm[k];later();},` +
		`removeItem:function(k){k=String(k);if(!own(k))return;delete d[k];delete pend[k];rm[k]=1;later();},` +
		`clear:function(){d={};pend={};rm={};clr=true;later();},` +
		`key:function(i){var ks=Object.keys(d);return i<ks.length?ks[i]:null;},` +
		`get length(){return Object.keys(d).length;}};` +
		`try{Object.defineProperty(window,"localStorage",{value:s,configurable:true});}catch(e){}` +
		`addEventListener("pagehide",function(){flush(true);});` +
		`addEventListener("visibilitychange",function(){if(document.visibilityState==="hidden")flush(true);});` +
		`})();</script>`
}

// withViewState puts the person's saved view into every isolated frame on the
// page, after the page helper, and offers Reset view when there is one.
func (T *CustomApps) withViewState(page json.RawMessage, appdb Database, spec AppSpec) json.RawMessage {
	state := loadViewState(appdb, spec.Slug)
	script := viewStateScript(state)
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
				t["isolate_prelude"] = pre + script
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
	if len(state) > 0 {
		out = withNavLink(out, "Reset view", T.WebPath()+"/"+spec.Slug+"/_view/reset")
	}
	return out
}
