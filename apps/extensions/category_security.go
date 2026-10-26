// A category's Security panel: set How it runs and Who can use it for every
// tool in the category at once.
//
// Setting it on the category overrides what each tool had on its own, which
// is the point (one decision for twenty tools) and the risk (one tool given
// to a particular agent on purpose quietly loses it). So the panel says first
// where the tools disagree, naming each one and what it has, and an override
// is a deliberate Apply rather than a side effect of a click.

package extensions

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// categoryMembers is the tools in a category, as the Categories list shows
// them (their own label, or the one gohort filed them under).
func categoryMembers(user, category string) []string {
	return userToolCategories(user)[strings.TrimSpace(category)]
}

// categoryOf is the category /api/category-security?name= names, and its
// tools.
func categoryOf(c *jsonapi.Call) ([]string, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	members := categoryMembers(c.User, name)
	if name == "" || len(members) == 0 {
		return nil, jsonapi.NotFound("no tools in that category")
	}
	return members, nil
}

// handleCategorySecurity is /api/category-security?name=<category>&section=run|who:
// How it runs for the whole category, or Who can use it.
func (T *Extensions) handleCategorySecurity(c *jsonapi.Call) (any, error) {
	members, err := categoryOf(c)
	if err != nil {
		return nil, err
	}
	c.W.Header().Set("Cache-Control", "no-store")
	db, user := AuthDB(), c.User
	switch c.R.URL.Query().Get("section") {
	case "run":
		return categoryRunState(db, user, members), nil
	case "who":
		return categoryWhoState(db, user, members)
	}
	return nil, jsonapi.NotFound("unknown section: use section=run or section=who")
}

// handleCategorySecuritySet applies the section's choice to every tool in the
// category. The body is the section's own: {target, on} for run, {mode,
// agents} for who.
func (T *Extensions) handleCategorySecuritySet(c *jsonapi.Call, raw json.RawMessage) (any, error) {
	members, err := categoryOf(c)
	if err != nil {
		return nil, err
	}
	db, user := AuthDB(), c.User
	switch c.R.URL.Query().Get("section") {
	case "run":
		var body struct {
			Target string `json:"target"`
			On     bool   `json:"on"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, jsonapi.BadRequest("bad body: " + err.Error())
		}
		return categoryRunSet(db, user, members, body.Target, body.On)
	case "who":
		var body struct {
			Mode   string   `json:"mode"`
			Agents []string `json:"agents"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, jsonapi.BadRequest("bad body: " + err.Error())
		}
		return categoryWhoSet(db, user, members, body.Mode, body.Agents)
	}
	return nil, jsonapi.NotFound("unknown section: use section=run or section=who")
}

// categoryRunState is How it runs for the whole category: a pill is on when
// every tool has it, dashed when only some do.
func categoryRunState(db Database, user string, members []string) map[string]any {
	type pill struct {
		Key     string `json:"key"`
		Label   string `json:"label"`
		On      bool   `json:"on"`
		Partial bool   `json:"partial,omitempty"`
	}
	unattended, ask := 0, 0
	for _, n := range members {
		if row, ok := UserToolByName(db, user, n); ok && row.Tool.NoUnattended {
			unattended++
		}
		if UserToolAsksInChat(db, user, "", n) {
			ask++
		}
	}
	state := func(have int) (bool, bool) { return have == len(members), have > 0 && have < len(members) }
	uOn, uPart := state(unattended)
	aOn, aPart := state(ask)
	note := "Sets it for all " + countTools(len(members)) + " in this category."
	if uPart || aPart {
		note += " A dashed one is on for some of them and not others; clicking it turns it on for all."
	}
	return map[string]any{"note": note, "items": []pill{
		{Key: "never_unattended", Label: "Never run unattended", On: uOn, Partial: uPart},
		{Key: "ask_first", Label: "Ask me before every call", On: aOn, Partial: aPart},
	}}
}

// categoryRunSet sets a How it runs pill on every tool in the category.
func categoryRunSet(db Database, user string, members []string, target string, on bool) (any, error) {
	var failed []string
	for _, n := range members {
		if _, err := toolRunApply(db, user, n, strings.TrimSpace(target), on); err != nil {
			failed = append(failed, n+" ("+err.Error()+")")
		}
	}
	if len(failed) > 0 {
		return nil, jsonapi.BadRequest("set for the others, but not: " + strings.Join(failed, "; "))
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// toolWhoSummary is one tool's Who can use it, in words.
func toolWhoSummary(state map[string]any) string {
	mode, _ := state["mode"].(string)
	switch mode {
	case toolWhoAll:
		return "All my agents"
	case toolWhoBound:
		return "Apps only"
	case toolWhoAgents:
		var names []string
		for _, a := range state["agents"].([]toolWhoAgent) {
			if a.On {
				names = append(names, a.Label)
			}
		}
		if len(names) == 0 {
			return "No agents"
		}
		return "Only " + strings.Join(names, ", ")
	}
	return "Not kept yet"
}

// categoryWhoState is Who can use it for the whole category: one setting when
// the tools agree, and when they do not, each tool and what it has.
func categoryWhoState(db Database, user string, members []string) (any, error) {
	prov, ok := ScopeProviderFor("tool")
	if !ok {
		return nil, jsonapi.Status(http.StatusServiceUnavailable, "tool scope unavailable")
	}
	type agentRow struct {
		ID      string `json:"id"`
		Label   string `json:"label"`
		On      bool   `json:"on"`
		Partial bool   `json:"partial,omitempty"`
		Under   string `json:"under,omitempty"`
	}
	type differ struct {
		Tool    string `json:"tool"`
		Setting string `json:"setting"`
	}
	var rows []agentRow
	index := map[string]int{}
	onCount := map[string]int{}
	bySetting := map[string][]string{}
	mode := ""
	sameMode := true
	for i, n := range members {
		st := toolWhoState(prov, db, user, n)
		m, _ := st["mode"].(string)
		if i == 0 {
			mode = m
		} else if m != mode {
			sameMode = false
		}
		bySetting[toolWhoSummary(st)] = append(bySetting[toolWhoSummary(st)], n)
		for _, a := range st["agents"].([]toolWhoAgent) {
			if _, seen := index[a.ID]; !seen {
				index[a.ID] = len(rows)
				rows = append(rows, agentRow{ID: a.ID, Label: a.Label, Under: a.Under})
			}
			if a.On && m == toolWhoAgents {
				onCount[a.ID]++
			}
		}
	}
	for i := range rows {
		c := onCount[rows[i].ID]
		rows[i].On = c == len(members)
		rows[i].Partial = c > 0 && c < len(members)
	}
	if rows == nil {
		rows = []agentRow{}
	}
	differs := []differ{}
	if len(bySetting) > 1 {
		for setting, tools := range bySetting {
			for _, tl := range tools {
				differs = append(differs, differ{Tool: tl, Setting: setting})
			}
		}
		sort.Slice(differs, func(i, j int) bool {
			if differs[i].Setting != differs[j].Setting {
				return differs[i].Setting < differs[j].Setting
			}
			return differs[i].Tool < differs[j].Tool
		})
		mode = "" // nothing one choice describes
	} else if !sameMode {
		mode = ""
	}
	return map[string]any{"mode": mode, "agents": rows, "count": len(members), "differ": differs}, nil
}

// categoryWhoSet sets every tool in the category to exactly one choice.
func categoryWhoSet(db Database, user string, members []string, choice string, agents []string) (any, error) {
	prov, ok := ScopeProviderFor("tool")
	if !ok {
		return nil, jsonapi.Status(http.StatusServiceUnavailable, "tool scope unavailable")
	}
	mode := strings.TrimSpace(choice)
	var failed []string
	for _, n := range members {
		var err error
		switch mode {
		case toolWhoAll, toolWhoBound:
			_, err = toolWhoPick(prov, db, user, n, mode)
		case toolWhoAgents:
			// Exactly the agents chosen here, not whatever this tool last
			// had: the category's choice replaces each tool's own.
			toolWhoRemember(prov, db, user, n)
			if len(agents) == 0 {
				_, err = toolWhoFlags(db, user, n, true, false)
			} else {
				_, err = toolWhoOnly(prov, db, user, n, agents)
			}
		default:
			return nil, jsonapi.BadRequest("unknown choice " + mode)
		}
		if err != nil {
			failed = append(failed, n+" ("+err.Error()+")")
		}
	}
	if len(failed) > 0 {
		return nil, jsonapi.BadRequest("set for the others, but not: " + strings.Join(failed, "; "))
	}
	return map[string]any{"ok": true, "message": "Set for all " + countTools(len(members)) + "."}, nil
}

func countTools(n int) string {
	if n == 1 {
		return "1 tool"
	}
	return strconv.Itoa(n) + " tools"
}

// categorySecurityJS drives a category's Security panel: How it runs as pills
// (dashed where the tools disagree), and Who can use it as one choice with an
// explicit Apply, preceded by the tools whose own setting differs. No
// backticks: this is a Go raw string.
const categorySecurityJS = `function(ctx){
  var r = (ctx && ctx.record) || {};
  var name = r.name;
  if(!name){ window.uiAlert && window.uiAlert('No category selected.'); return; }
  var reload = ctx && ctx.reload;
  var qs = 'name=' + encodeURIComponent(name);
  function heading(body, text){
    var h = document.createElement('div');
    h.style.cssText = 'font-size:var(--fs-xs, 0.78rem);color:var(--text-mute);text-transform:uppercase;letter-spacing:0.04em;margin:0.9rem 0 0.35rem';
    h.textContent = text;
    body.appendChild(h);
  }
  function jsonOrThrow(res){
    if(!res.ok) return res.text().then(function(t){ throw new Error(t || ('HTTP ' + res.status)); });
    return res.status === 204 ? {} : res.json();
  }
  function runPills(body){
    var host = document.createElement('div');
    body.appendChild(host);
    window.uiRenderScopePills(host, {
      load: function(){ return fetch('api/category-security?' + qs + '&section=run', {cache:'no-store'}).then(jsonOrThrow); },
      toggle: function(key, on){
        return fetch('api/category-security?' + qs + '&section=run', {method:'POST', headers:{'Content-Type':'application/json'},
          body: JSON.stringify({target: key, on: on})}).then(jsonOrThrow).then(function(){ if(reload) reload(); });
      }
    });
  }
  function who(body){
    var host = document.createElement('div');
    body.appendChild(host);
    var group = 'cat-who-' + Math.random().toString(36).slice(2);
    function render(d){
      host.innerHTML = '';
      var differ = d.differ || [];
      if (differ.length) {
        var warn = document.createElement('div');
        warn.style.cssText = 'border:1px solid var(--warning, #c9a227);border-radius:6px;padding:0.5rem 0.65rem;margin:0 0 0.6rem;font-size:var(--fs-sm, 0.85rem);line-height:1.45';
        var t = document.createElement('div');
        t.style.cssText = 'font-weight:600;margin-bottom:0.25rem';
        t.textContent = 'These tools are set differently. Applying a choice below replaces each one’s own setting:';
        warn.appendChild(t);
        differ.forEach(function(x){
          var line = document.createElement('div');
          line.textContent = x.tool + ': ' + x.setting;
          warn.appendChild(line);
        });
        host.appendChild(warn);
      }
      var picked = d.mode || '';
      var ticked = {};
      (d.agents || []).forEach(function(a){ if (a.on) ticked[a.id] = true; });
      var list = document.createElement('div');
      list.style.cssText = 'margin:0.2rem 0 0.4rem 1.6rem';
      function drawList(){
        list.innerHTML = '';
        list.style.display = picked === 'agents' ? '' : 'none';
        (d.agents || []).forEach(function(a){
          var row = document.createElement('label');
          row.style.cssText = 'display:flex;align-items:center;gap:0.5rem;margin:0.15rem 0;cursor:pointer' + (a.under ? ';margin-left:1.4rem' : '');
          var c = document.createElement('input');
          c.type = 'checkbox'; c.checked = !!ticked[a.id];
          c.indeterminate = !!a.partial && !ticked[a.id];
          c.addEventListener('change', function(){ if (c.checked) ticked[a.id] = true; else delete ticked[a.id]; });
          row.appendChild(c);
          row.appendChild(document.createTextNode((a.label || a.id) + (a.partial ? ' (some tools)' : '')));
          list.appendChild(row);
        });
      }
      [['all', 'All my agents'], ['agents', 'Only these agents'], ['bound', 'Apps only']].forEach(function(o){
        var row = document.createElement('label');
        row.style.cssText = 'display:flex;align-items:center;gap:0.5rem;margin:0.25rem 0;cursor:pointer';
        var rb = document.createElement('input');
        rb.type = 'radio'; rb.name = group; rb.value = o[0]; rb.checked = (picked === o[0]);
        rb.addEventListener('change', function(){ if (rb.checked) { picked = o[0]; drawList(); } });
        row.appendChild(rb);
        row.appendChild(document.createTextNode(o[1]));
        host.appendChild(row);
        if (o[0] === 'agents') host.appendChild(list);
      });
      drawList();
      var apply = document.createElement('button');
      apply.type = 'button';
      apply.className = 'ui-row-btn primary';
      apply.style.marginTop = '0.5rem';
      apply.textContent = 'Apply to all ' + d.count + ' tool' + (d.count === 1 ? '' : 's');
      apply.addEventListener('click', function(){
        if (!picked) { window.uiAlert && window.uiAlert('Choose who can use them first.'); return; }
        var go = function(){
          apply.disabled = true;
          fetch('api/category-security?' + qs + '&section=who', {method:'POST', headers:{'Content-Type':'application/json'},
            body: JSON.stringify({mode: picked, agents: Object.keys(ticked)})})
            .then(jsonOrThrow)
            .then(function(){ if(reload) reload(); load(); })
            .catch(function(e){ apply.disabled = false; window.uiAlert && window.uiAlert('Could not change them: ' + (e && e.message || e)); load(); });
        };
        if (differ.length && window.uiConfirm) {
          window.uiConfirm('Replace the settings of ' + differ.length + ' tool' + (differ.length === 1 ? '' : 's') + ' listed above with this choice?').then(function(ok){ if (ok) go(); });
        } else if (differ.length) {
          if (window.confirm('Replace the settings of the tools listed above with this choice?')) go();
        } else {
          go();
        }
      });
      host.appendChild(apply);
    }
    function load(){
      fetch('api/category-security?' + qs + '&section=who', {cache:'no-store'}).then(jsonOrThrow)
        .then(render, function(e){ host.textContent = 'Could not load: ' + (e && e.message || e); });
    }
    load();
  }
  window.uiOpenSimpleModal({
    title: 'Security: ' + name,
    width: '600px',
    mount: function(body){
      var intro = document.createElement('div');
      intro.style.cssText = 'color:var(--text-mute);font-size:var(--fs-sm, 0.85rem);line-height:1.45';
      intro.textContent = 'Set these for every tool in the category at once. A tool can still be set on its own afterwards, from its own Security.';
      body.appendChild(intro);
      heading(body, 'How it runs');
      runPills(body);
      heading(body, 'Who can use it');
      who(body);
    }
  });
}`
