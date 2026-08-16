// Watching a machine run, without attaching it to anything.
//
// The gap this closes is the reason machines are hard to understand: a
// machine is a BEHAVIOUR and the editor only ever showed its
// configuration. You author twelve fields per step and then open a chat
// and hope. Every concept here — a step that waits versus one that hands
// on, routing on a field, what a step passes forward — is about what
// happens across turns, and nothing showed a turn happening.
//
// So: type a message, see which steps ran, why it moved, what each one
// handed on, and where it stopped. One click, no agent to attach, no
// session to start. That teaches transient-versus-resident better than
// any amount of help text, because it is the thing itself rather than a
// description of it.
//
// It is a REAL run of the machine's own driver — AdvanceMachine, the
// same function a live turn calls — not a simulation. Two honest
// differences, both stated in the reply:
//
//   - No tools. The dry run has no agent, so a step that would search or
//     read gets nothing back. It shows the SHAPE of the traversal, not
//     the quality of the answers.
//   - It stops AT the resident step rather than running it. Where a
//     conversation lands is the question here; what it would say is the
//     agent's job, and answering it would need the agent's whole context.

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

// tryTimeout bounds a dry run. Each transient hop is a model call, and
// the cap on hops is small, so this is generous — a run that outlives it
// has something wrong with it rather than being slow.
const tryTimeout = 3 * time.Minute

// handleMachineTry runs one message through the machine and reports the
// path it took.
//
//	POST /api/machines/{id}/try  {"message": "..."}
func (T *OrchestrateApp) handleMachineTry(w http.ResponseWriter, r *http.Request, udb Database, user string, def MachineDef) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Message string `json:"message"`
		// Cursor is the previous try's position, round-tripped through
		// the browser. This is what makes the rehearsal MULTI-TURN — a
		// second message resumes the parked step, which is the only way
		// a guard, a re-entry or a one-turn handoff can ever be watched:
		// all of them exist only across turns. Client-held rather than
		// stored because a rehearsal should die with the page, and the
		// cursor is the user's own data about their own machine.
		Cursor *MachineCursor `json:"cursor"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	msg := strings.TrimSpace(body.Message)
	if msg == "" {
		http.Error(w, "type a message to run through it", http.StatusBadRequest)
		return
	}
	// A machine that cannot run should say why HERE rather than failing
	// somewhere downstream with the same information in a worse place.
	if probs := def.Problems(); len(probs) > 0 {
		writeJSON(w, map[string]any{
			"blocked":   true,
			"checklist": probs,
			"note":      "This machine will not run yet. Fix the list above and try again.",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), tryTimeout)
	defer cancel()

	cur := body.Cursor
	if cur == nil {
		cur = &MachineCursor{}
	}
	priorHops := len(cur.Log)
	var notes []string
	// No catalog: a dry run has no agent, so there are no tools to offer.
	// Said in the reply rather than hidden, because a step that would
	// have searched will look thin and the reason should not be a mystery.
	// The real driver, resumed cursor and all — so on a second message a
	// resident step's guard is judged exactly as a live turn would judge
	// it. The one honest difference from live: the guard runs on the
	// same tool-less worker the transient steps use.
	landed, err := T.AdvanceMachine(ctx, def, cur, MachineTurn{
		Input: msg,
		User:  user,
		Now:   time.Now().In(UserLocation(user)).Format("Mon, January 2, 2006 at 3:04 PM MST"),
	}, T.PhaseWorker(nil),
		func(kind, detail string) { notes = append(notes, kind+": "+detail) })
	if err != nil {
		writeJSON(w, map[string]any{
			"failed": err.Error(),
			"path":   tryPath(cur, priorHops),
			"notes":  notes,
			"cursor": cur,
		})
		return
	}
	Log("[orchestrate.machines] user=%q dry-ran %q → landed in %s after %d hop(s)",
		user, def.Name, landed.Name, len(cur.Log))

	writeJSON(w, map[string]any{
		"landed":      landed.Name,
		"landed_desc": strings.TrimSpace(landed.Desc),
		"path":        tryPath(cur, priorHops),
		"handed_on":   tryState(cur),
		"notes":       notes,
		"cursor":      cur,
		// Stated every time. Somebody reading a thin answer should know
		// which of the two reasons it is.
		"caveat": "A dry run has no tools and does not run the step it lands in — it shows where a turn would GO, not what it would say.",
	})
}

// tryPath renders the traversal in the words the editor uses — only the
// hops THIS message caused, because earlier turns already reported
// theirs. prior is clamped: the log is capped (maxPhaseLog) and an
// index taken before a trim would otherwise reach past it.
func tryPath(cur *MachineCursor, prior int) []map[string]any {
	if prior > len(cur.Log) {
		prior = len(cur.Log)
	}
	out := make([]map[string]any, 0, len(cur.Log)-prior)
	for _, h := range cur.Log[prior:] {
		out = append(out, map[string]any{"from": h.From, "to": h.To, "why": h.Why})
	}
	return out
}

// tryState is what each step handed on — the blackboard, which is the
// part a person cannot otherwise see at all.
func tryState(cur *MachineCursor) map[string]any {
	out := map[string]any{}
	for phase, res := range cur.State {
		entry := map[string]any{}
		for k, v := range res.Fields {
			entry[k] = v
		}
		// A step with no declared fields still produced text, and seeing
		// it is how somebody works out whether the step did its job.
		if len(entry) == 0 && strings.TrimSpace(res.Text) != "" {
			entry["(reply)"] = truncate(res.Text, 600)
		}
		out[phase] = entry
	}
	return out
}

// --- the panel ---------------------------------------------------------

// machineTryPanel is the surface for the endpoint above: a box, a
// button, and somewhere for the answer to land.
//
// It sits with the picture rather than at the top of the page. The
// picture is the machine's shape and this is the machine's behaviour, and
// they answer the same question one after the other: what IS this thing.
func machineTryPanel(def MachineDef) ui.Component {
	return ui.Stack{Children: []ui.Component{
		ui.Card{HTML: `<div style="display:flex;gap:0.5rem;align-items:center;flex-wrap:wrap">` +
			`<input id="machine-try-msg" type="text" style="flex:1;min-width:16rem" ` +
			`placeholder="` + HTMLEscape(tryPlaceholder(def)) + `" ` +
			`aria-label="A message to run through this machine">` +
			`</div>` +
			`<div id="machine-try-out" style="margin-top:0.75rem"></div>`},
		ui.Toolbar{Actions: []ui.ToolbarAction{{
			Label:   "Run it",
			Title:   "Send that message through the machine and show the path it takes",
			Method:  "client",
			URL:     "machine_try",
			Variant: "primary",
		}}},
	}}
}

// tryPlaceholder suggests a message in the machine's own terms, so the
// box is not a blank invitation to guess what it wants.
func tryPlaceholder(def MachineDef) string {
	if d := strings.TrimSpace(def.Description); d != "" {
		return "Something a person would open with. " + firstSentence(d)
	}
	return "Something a person would open with"
}

// firstSentence trims a description down to its opening claim.
func firstSentence(s string) string {
	if i := strings.IndexAny(s, ".!?"); i > 0 {
		return s[:i+1]
	}
	return s
}

// machineTryJS runs the message and draws the traversal.
//
// It builds the result with textContent rather than markup: every string
// in that reply came out of a model, and a dry run that could inject into
// its own author's editor would be a strange thing to ship.
//
// Everything it draws is what the endpoint said, in the words the editor
// uses. The path is the point — a person reads "triage → hunch → verify,
// and stopped there because the conversation waits in verify" and has
// learned transient-versus-resident without a paragraph about it.
const machineTryJS = `function(ctx) {
  var id  = new URLSearchParams(window.location.search).get('id');
  var box = document.getElementById('machine-try-msg');
  var out = document.getElementById('machine-try-out');
  if (!id || !box || !out) return;
  var msg = (box.value || '').trim();
  if (!msg) { box.focus(); return; }

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

  function render(d, turn) {
    if (d.cursor) out.dataset.cursor = JSON.stringify(d.cursor);
    turn.textContent = '';
    turn.appendChild(el('div', 'Turn ' + turnNo + ' — “' + msg + '”',
      'font-size:0.8rem;color:var(--text-mute);margin-bottom:0.25rem'));
    if (d.blocked) {
      turn.appendChild(el('div', d.note, 'font-weight:600'));
      turn.appendChild(list(d.checklist || []));
      return;
    }
    if (d.failed) {
      turn.appendChild(el('div', 'It stopped: ' + d.failed, 'font-weight:600;color:var(--danger)'));
    } else {
      var where = 'It ended in ' + d.landed;
      if (d.landed_desc) where += ' — ' + d.landed_desc;
      turn.appendChild(el('div', where, 'font-weight:600'));
    }

    var path = d.path || [];
    if (path.length) {
      turn.appendChild(el('div', 'The path it took', HEAD));
      turn.appendChild(list(path.map(function(h) {
        return h.from + ' → ' + h.to + (h.why ? '  (' + h.why + ')' : '');
      })));
    } else if (!d.failed) {
      turn.appendChild(el('div', turnNo > 1
        ? 'It stayed where it was parked — nothing judged this message as a new job.'
        : 'It went nowhere: the machine starts in a step the conversation waits in, so the first turn lands there directly.',
        MUTE + ';margin-top:0.5rem'));
    }

    var state = d.handed_on || {};
    var steps = Object.keys(state);
    if (steps.length) {
      turn.appendChild(el('div', 'The blackboard now holds', HEAD));
      steps.forEach(function(name) {
        turn.appendChild(el('div', name, 'font-weight:600;margin-top:0.4rem;font-size:0.85rem'));
        var fields = state[name] || {};
        var rows = Object.keys(fields).map(function(k) {
          var v = fields[k];
          if (v && typeof v === 'object') v = JSON.stringify(v);
          return k + ': ' + v;
        });
        turn.appendChild(rows.length ? list(rows, 'font-size:0.85rem')
                                     : el('div', '(nothing)', MUTE));
      });
    }

    if ((d.notes || []).length) {
      turn.appendChild(el('div', 'Decisions the framework took for you', HEAD));
      turn.appendChild(list(d.notes, 'font-size:0.85rem'));
    }
    if (d.caveat) turn.appendChild(el('div', d.caveat, MUTE + ';margin-top:0.75rem'));
  }

  var btn = ctx && ctx.button, label = btn && btn.textContent;
  if (btn) { btn.disabled = true; btn.textContent = 'Running…'; }
  // Each message gets its own block, appended — the rehearsal reads as
  // the conversation it is, and turn 4 firing a guard sits under the
  // three turns that led to it.
  var turn = el('div', null, 'margin-top:0.9rem;padding-top:0.6rem;border-top:1px solid var(--border)');
  turn.setAttribute('data-try-turn', String(turnNo));
  turn.appendChild(el('div', 'Running it. Each step it passes through is a model call, so this takes a moment.', MUTE));
  out.appendChild(turn);
  box.value = '';

  fetch('/orchestrate/api/machines/' + encodeURIComponent(id) + '/try', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({message: msg, cursor: cursor})
  }).then(function(r) {
    if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
    return r.json();
  }).then(function(d) { render(d, turn); }).catch(function(err) {
    turn.textContent = '';
    turn.appendChild(el('div', 'It could not run: ' + (err && err.message || err),
                       'font-weight:600;color:var(--danger)'));
  }).then(function() {
    if (btn) { btn.disabled = false; btn.textContent = label || 'Send'; }
    box.focus();
  });
}`

// machineTryResetJS forgets the rehearsal: cursor and transcript both,
// so the next message is turn one of a fresh conversation.
const machineTryResetJS = `function(ctx) {
  var out = document.getElementById('machine-try-out');
  if (!out) return;
  delete out.dataset.cursor;
  out.textContent = '';
  var box = document.getElementById('machine-try-msg');
  if (box) box.focus();
}`

// machineTryEnterJS makes Return in the box do what the button does.
// Typing a sentence and pressing enter is what everybody tries first,
// and a box that swallows it reads as broken.
//
// Delegated from the document rather than bound to the input: this runs
// in the page's init block, and the panel is built by the runtime after
// it, so a direct addEventListener would bind to nothing.
const machineTryEnterJS = `document.addEventListener('keydown', function(e) {
  var t = e.target;
  if (!t || t.id !== 'machine-try-msg' || e.key !== 'Enter') return;
  e.preventDefault();
  var fn = window.UIClientActions && window.UIClientActions['machine_try'];
  if (fn) fn({button: null});
});`
