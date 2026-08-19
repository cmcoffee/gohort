// Starting a machine that RUNS.
//
// Unattended machines shipped without a door. The mode was reachable from
// Go, from the editor's toggle and from the `machine` tool, and there was
// no way to start one — which meant the whole thing was unexercised, and
// an unexercised runtime is a claim rather than a capability.
//
// This is the first door, and deliberately the smallest one that is
// honest: press a button, the run happens, you read what it produced.
//
// It is SYNCHRONOUS, which is the one thing to know about it. A run walks
// its steps with a model call each, so a long machine holds the request
// open; the timeout below is the ceiling. That is fine for the machines
// somebody is building and watching, and it is NOT the shape a
// twenty-two-step research run wants. The next door (a schedule, or a
// dispatch target) is where background execution belongs, and it can lean
// on the same RunUnattended underneath.
//
// The sibling of "Try it", and the difference is worth saying out loud:
// a rehearsal shows the PATH with no tools and stops at the step that
// waits. This runs the machine for real, with the caller's tools, to the
// step that finishes it.

package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// runTimeout bounds one unattended run started from the page.
//
// Generous, because this is where a machine with real work in it gets
// tried for the first time and a stingy ceiling would make an honest run
// look broken. It is still a ceiling: the request is open the whole time,
// and a run that outlives this wants the background door instead.
const runTimeout = 15 * time.Minute

// handleMachineRun starts an unattended run and returns what it produced.
//
//	POST /api/machines/{id}/run  {"input": "..."}
func (T *OrchestrateApp) handleMachineRun(w http.ResponseWriter, r *http.Request, udb Database, user string, def MachineDef) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !def.Unattended {
		http.Error(w, "this machine converses rather than runs: it has a step that waits for a person, and nobody is waiting here. "+
			"Use Try it to rehearse it, or attach it to an agent and talk to it.", http.StatusBadRequest)
		return
	}
	var body struct {
		Input string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	input := strings.TrimSpace(body.Input)
	if input == "" {
		http.Error(w, "say what this run is about", http.StatusBadRequest)
		return
	}
	// A machine that cannot run says why HERE rather than failing
	// downstream with the same information in a worse place.
	if probs := def.Problems(); len(probs) > 0 {
		writeJSON(w, map[string]any{
			"blocked":   true,
			"checklist": probs,
			"note":      "This machine will not run yet. Fix the list above and start it again.",
		})
		return
	}

	// The deployment check comes AFTER the two refusals above, and the
	// order is the point: "this machine converses" and "this machine has
	// four things wrong with it" are true whether or not a model is
	// configured, and answering either with "worker LLM not configured"
	// would send somebody to fix the wrong thing. Every step below IS a
	// model call, so the check still has to happen before one starts.
	if T.LLM == nil {
		http.Error(w, "worker LLM not configured", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), runTimeout)
	defer cancel()

	// The caller's own tool pool, which each step narrows with its own
	// Tools list (PhaseWorker does the narrowing). A step that names no
	// tools gets none, which is the transient-step rule everywhere else.
	sess := &ToolSession{Username: user, DB: AuthDB()}
	catalog, err := GetAgentToolsWithSession(sess, availableWorkerToolNames()...)
	if err != nil {
		// One unresolvable name should not cost the run its whole
		// catalog; carry on with what resolved and say so.
		Log("[orchestrate.machines] run %q: tool catalog partly unresolved for user=%q: %v", def.Name, user, err)
	}

	cur := &MachineCursor{}
	var notes []string
	final, out, err := T.RunUnattended(ctx, def, cur, MachineTurn{
		Input: input,
		User:  user,
		Now:   time.Now().In(UserLocation(user)).Format("Mon, January 2, 2006 at 3:04 PM MST"),
	}, T.PhaseWorker(catalog),
		func(kind, detail string) { notes = append(notes, kind+": "+detail) })

	res := map[string]any{
		"path":  tryPath(cur, 0),
		"state": tryState(cur),
		"notes": notes,
	}
	if err != nil {
		// The partial text comes back WITH the failure, because a run that
		// stopped at step nine still did nine steps of work and throwing
		// that away teaches nothing about why it stopped.
		res["failed"] = err.Error()
		res["output"] = out
		Log("[orchestrate.machines] user=%q ran %q: stopped after %d step(s): %v", user, def.Name, len(cur.Log)+1, err)
		writeJSON(w, res)
		return
	}
	Log("[orchestrate.machines] user=%q ran %q → finished at %s after %d step(s)", user, def.Name, final.Name, len(cur.Log)+1)
	res["finished"] = final.Name
	res["finished_desc"] = strings.TrimSpace(final.Desc)
	res["output"] = out
	writeJSON(w, res)
}

// machineRunPanel is the door itself: a box, a button, and whatever the
// run produced.
//
// Deliberately the same shape as the rehearsal's panel, because they are
// the same gesture with different stakes, and a second layout for the
// second one would only make somebody work out which is which.
func machineRunPanel(def MachineDef) ui.Component {
	return ui.Stack{Children: []ui.Component{
		ui.Card{HTML: `<div style="display:flex;gap:0.5rem;align-items:center;flex-wrap:wrap">` +
			`<input id="machine-run-input" type="text" style="flex:1;min-width:16rem" ` +
			`placeholder="` + HTMLEscape(runPlaceholder(def)) + `" ` +
			`aria-label="What this run is about">` +
			`</div>` +
			`<div id="machine-run-out" style="margin-top:0.75rem"></div>`},
		ui.Toolbar{Actions: []ui.ToolbarAction{{
			Label:   "Start the run",
			Title:   "Run this machine now, with your tools, to the step that finishes it",
			Method:  "client",
			URL:     "machine_run",
			Variant: "primary",
		}}},
	}}
}

// runPlaceholder asks for the run's subject in the machine's own terms.
func runPlaceholder(def MachineDef) string {
	if d := strings.TrimSpace(def.Description); d != "" {
		return "What this run is about. " + firstSentence(d)
	}
	return "What this run is about"
}

// machineRunJS posts the input and renders what came back.
//
// No streaming: the request is open for the whole run, so the button says
// it is working and the result arrives at the end. A background door with
// progress is the next one, not this one.
const machineRunJS = `function(ctx) {
  var id  = new URLSearchParams(window.location.search).get('id');
  var box = document.getElementById('machine-run-input');
  var out = document.getElementById('machine-run-out');
  if (!id || !box || !out) return;
  var input = (box.value || '').trim();
  if (!input) { box.focus(); return; }

  function el(tag, text, style) {
    var e = document.createElement(tag);
    if (text != null) e.textContent = text;
    if (style) e.setAttribute('style', style);
    return e;
  }
  var MUTE = 'color:var(--text-mute);font-size:0.82rem';
  var HEAD = 'font-weight:600;margin:0.75rem 0 0.25rem';

  function list(items, style) {
    var ul = el('ul', null, 'margin:0.25rem 0 0;padding-left:1.1rem;' + (style || ''));
    items.forEach(function(t) { ul.appendChild(el('li', t)); });
    return ul;
  }

  var btn = ctx && ctx.button;
  var label = btn ? btn.textContent : '';
  if (btn) { btn.disabled = true; btn.textContent = 'Running…'; }
  out.textContent = '';
  out.appendChild(el('div', 'Running. Every step is a model call, so this takes as long as the machine is long.', MUTE));

  fetch('/orchestrate/api/machines/' + encodeURIComponent(id) + '/run', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({input: input}),
  }).then(function(r) {
    if (!r.ok) { return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); }); }
    return r.json();
  }).then(function(d) {
    out.textContent = '';
    if (d.blocked) {
      out.appendChild(el('div', d.note, 'font-weight:600'));
      out.appendChild(list(d.checklist || []));
      return;
    }
    if (d.failed) {
      out.appendChild(el('div', 'It stopped: ' + d.failed, 'font-weight:600;color:var(--danger)'));
    } else {
      var where = 'Finished at ' + d.finished;
      if (d.finished_desc) where += ' — ' + d.finished_desc;
      out.appendChild(el('div', where, 'font-weight:600'));
    }
    if (d.output) {
      out.appendChild(el('div', 'What it produced', HEAD));
      out.appendChild(el('div', d.output, 'white-space:pre-wrap'));
    }
    if (d.path && d.path.length) {
      out.appendChild(el('div', 'The steps it took', HEAD));
      out.appendChild(list(d.path));
    }
    if (d.state && d.state.length) {
      out.appendChild(el('div', 'What it established', HEAD));
      out.appendChild(list(d.state, MUTE));
    }
    if (d.notes && d.notes.length) {
      out.appendChild(el('div', 'What the framework decided for you', HEAD));
      out.appendChild(list(d.notes, MUTE));
    }
  }).catch(function(err) {
    out.textContent = '';
    out.appendChild(el('div', 'It could not run: ' + err.message, 'font-weight:600;color:var(--danger)'));
  }).then(function() {
    if (btn) { btn.disabled = false; btn.textContent = label || 'Start the run'; }
  });
}`

// unattendedRunSection is the "Run it" section, present only when the
// machine can actually be run this way.
//
// An empty ui.Section renders as nothing, which is how a conversational
// machine simply does not have this door rather than having one that
// refuses. The alternative (a visible button that always says no) teaches
// somebody that the page lies to them.
func unattendedRunSection(def MachineDef) ui.Section {
	if !def.Unattended {
		return ui.Section{}
	}
	return ui.Section{
		Title: "Run it",
		Wide:  true,
		Subtitle: "Start it now, for real: your tools, every step run, ending at the step that hands off nowhere. " +
			"That step's result is the run's result. The request stays open while it works, so a long machine is a long wait.",
		Body: machineRunPanel(def),
	}
}
