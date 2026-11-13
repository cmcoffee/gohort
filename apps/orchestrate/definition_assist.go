// ✨ Get assistance on a machine's or a pipeline's editor.
//
// It replaces the "Describe a change" form those pages had, which was one
// shot: a sentence in, the whole definition redrafted and SAVED, and the
// first look at what it did was after it had done it. Here the worker talks
// it through first (what a step does, why a run went the way it did, what a
// change would mean), and a change it agrees to is DRAFTED and shown, step by
// step against what is there, before anything is saved.
//
// Three calls, because drafting a revision takes up to a minute and the
// conversation's reply cannot wait on it:
//
//	POST .../assist  {message, history} → {reply, change?}   the conversation
//	POST .../propose {description}      → {proposal, from, changed, changes, checklist}
//	POST .../revise  {proposal, from}   → saved, the version before kept for Undo
//
// Apply starts from the definition AS STORED and takes only what a revision
// may change (name, description, and the steps or stages). The drafter knows
// nothing else, so taking its whole answer, as the form used to, saved a
// definition without its sharing, its deny list, its flags: every revision
// quietly dropped them. And the proposal comes back from the page, so taking
// only those parts is also what keeps a posted proposal from touching who the
// definition is shared with.
//
// One ✨ per page, as on the agent editor: the per-step and per-stage helpers
// are gone, and a step is reached through this one ("rewrite the triage
// step's instructions").

package orchestrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// defChange is one entry in a proposal's review: a part of the definition
// as it is and as the change would leave it, shown as a line diff.
type defChange struct {
	Label   string `json:"label"`
	Current string `json:"current"`
	Value   string `json:"value"`
}

// definitionFingerprint identifies a definition's content, so applying a
// proposal can tell the definition changed after it was drafted (an edit in
// another tab, a second proposal applied) and refuse rather than overwrite.
func definitionFingerprint(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

// definitionJSON is a part of a definition as indented JSON, for a review
// entry whose diff should read line by line. "" for nothing.
func definitionJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil || string(b) == "null" {
		return ""
	}
	return string(b)
}

// definitionAssistPrompt is the conversation's system prompt: the definition
// as it stands, what is still wrong with it, and how to answer.
func definitionAssistPrompt(noun string, current any, problems []string) string {
	var b strings.Builder
	b.WriteString("You are helping someone with ONE " + noun + " they own: explaining it, and working out changes to it.\n\n")
	b.WriteString("## The " + noun + " as it stands\n\n")
	b.WriteString(definitionJSON(current) + "\n")
	if len(problems) > 0 {
		b.WriteString("\n## What is still wrong with it\n\n")
		for _, p := range problems {
			b.WriteString("- " + p + "\n")
		}
	}
	b.WriteString(`
## How to answer

Reply with a JSON object and nothing else:

{"reply": "what you say to the person", "change": ""}

- "reply" answers them: what a part does, why, what a change would mean. Keep it short.
- "change" is empty unless they asked for a change, or agreed to one you suggested. Then it says
  the change to make, in plain words, specific enough for someone editing the ` + noun + ` to act on:
  which steps, what to add, remove or rewire, and what instructions should say. One change, whole.
- A change is DRAFTED from what you write there and shown to them before anything is saved, so
  never say it has been made. Say what you are proposing.
- Only what a ` + noun + ` describes: its name, its description and its steps. Who it is shared
  with and its other settings are not yours to change.`)
	return b.String()
}

// parseDefinitionAssist reads the conversation's answer: the reply, and the
// change it proposes, if any. An answer that is not the JSON asked for is
// the reply as it stands, with no change, rather than an error: the person
// asked something and was answered.
func parseDefinitionAssist(raw string) (reply, change string) {
	text := strings.TrimSpace(raw)
	if fenced := stripJSONFence(text); fenced != "" {
		text = fenced
	}
	var out struct {
		Reply  string `json:"reply"`
		Change string `json:"change"`
	}
	if obj := extractJSONObject(text); obj != "" && json.Unmarshal([]byte(obj), &out) == nil && (out.Reply != "" || out.Change != "") {
		return strings.TrimSpace(out.Reply), strings.TrimSpace(out.Change)
	}
	return strings.TrimSpace(raw), ""
}

// definitionAssist is one page's dialog: where its endpoints are, and its
// words.
type definitionAssist struct {
	Noun        string   `json:"noun"`
	Base        string   `json:"base"`
	Intro       string   `json:"intro"`
	Placeholder string   `json:"placeholder"`
	Starters    []string `json:"starters"`
}

// js mounts the dialog's button on the page.
func (d definitionAssist) js() string {
	cfg, _ := json.Marshal(d)
	return "(function(){ function go(){ if(!window.orchDefinitionAssist){ setTimeout(go, 50); return; } window.orchDefinitionAssist(" +
		string(cfg) + "); } go(); })();"
}

// definitionAssistCSS styles the dialog.
const definitionAssistCSS = `
@media (pointer: coarse){#definition-assist{min-height:var(--btn-h-sm, 2.25rem)}}
.da-chat{display:flex;flex-direction:column;height:min(44vh, 25rem)}
.da-chat .ui-assist-right{flex:1 1 auto;min-height:0}
.da-review{max-height:36vh;overflow-y:auto;margin-top:.6rem}
.da-review:empty{display:none}
.da-head{font-weight:600;margin:.2rem 0 .4rem}
.da-summary{margin:.2rem 0 .5rem 1.1rem;padding:0}
.da-change{border:1px solid var(--border);border-radius:6px;padding:.5rem .6rem;margin:.4rem 0;background:var(--bg-2)}
.da-change-label{font-weight:600;margin-bottom:.3rem}
.da-change .editor-diff{margin:0;max-height:14rem}
.da-val{font-size:var(--fs-sm, .85rem);white-space:pre-wrap;max-height:9rem;overflow:auto;padding:.4rem;background:var(--bg-1);border:1px solid var(--border);border-radius:4px}
.da-warn{color:var(--text-mute);font-size:var(--fs-sm, .85rem)}
.da-drafting{display:flex;gap:.6rem;align-items:center}
.da-error{color:var(--danger, #c33)}
@media (max-width: 760px){.da-chat{height:50vh}}
`

// definitionAssistJS is the dialog: the shared conversation pane, and below
// it the change being drafted (moving, with its time and a Stop) and then
// shown for review. Apply saves it; Undo on the page puts the version before
// it back.
const definitionAssistJS = `(function(){
  if (window.orchDefinitionAssist) return;
  function el(t, a, kids) {
    var n = document.createElement(t); a = a || {};
    for (var k in a) { if (k === 'class') n.className = a[k]; else if (k === 'text') n.textContent = a[k]; else n.setAttribute(k, a[k]); }
    (kids || []).forEach(function(c) { n.appendChild(c); });
    return n;
  }
  var frames = '⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏';
  var opener = null;
  window.orchDefinitionAssist = function(cfg) {
    function open(ask) {
      var proposal = null, ctl = null, tick = null;
      function stopTick() { if (tick) { clearInterval(tick); tick = null; } }
      window.uiOpenModal({
        title: '✨ Get assistance with this ' + cfg.noun,
        subtitle: 'Ask about it, or for a change to any part of it. A change is drafted and shown to you first; nothing is saved until you apply it.',
        width: '780px',
        // However the dialog goes (Close, Escape, a click outside), a draft
        // still running is stopped with it.
        onClose: function() { if (ctl) ctl.abort(); stopTick(); },
        mount: function(body, api) {
          var review = el('div', {class: 'da-review'});
          var apply = el('button', {type: 'button', class: 'ui-row-btn primary', text: 'Apply this change'});
          apply.style.display = 'none';

          // Drafting runs a model over the whole definition and can take a
          // minute: it moves, says how long, and can be stopped.
          function draft(change) {
            if (ctl) ctl.abort();
            stopTick();
            review.innerHTML = ''; proposal = null; apply.style.display = 'none';
            var line = el('span', {text: frames.charAt(0) + ' Drafting the change'});
            var stop = el('button', {type: 'button', class: 'ui-row-btn', text: 'Stop'});
            review.appendChild(el('div', {class: 'da-drafting'}, [line, stop]));
            var from = Date.now(), f = 0, mine = ctl = new AbortController();
            stop.onclick = function() { mine.abort(); };
            tick = setInterval(function() {
              var s = Math.round((Date.now() - from) / 1000);
              line.textContent = frames.charAt(f++ % frames.length) + ' Drafting the change' + (s >= 3 ? ', ' + s + 's' : '');
            }, 100);
            fetch(cfg.base + '/propose', {method: 'POST', headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({description: change}), signal: mine.signal})
              .then(function(r) {
                if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
                return r.json();
              })
              .then(function(d) { if (ctl !== mine) return; ctl = null; stopTick(); show(d, change); })
              .catch(function(e) {
                if (ctl !== mine) return;
                ctl = null; stopTick();
                review.innerHTML = '';
                review.appendChild(el('div', {class: 'da-error', text: e && e.name === 'AbortError'
                  ? 'Stopped. Nothing was changed.'
                  : 'The change could not be drafted: ' + ((e && e.message) || e)}));
              });
          }

          function show(d, change) {
            review.innerHTML = '';
            var changes = (d && d.changes) || [];
            review.appendChild(el('div', {class: 'da-head', text: 'Proposed: ' + change}));
            if (!changes.length) {
              review.appendChild(el('div', {class: 'da-warn', text: 'The draft came back the same as what is there, so there is nothing to apply.'}));
              return;
            }
            proposal = d;
            var sum = el('ul', {class: 'da-summary'});
            (d.changed || []).forEach(function(s) { sum.appendChild(el('li', {text: s})); });
            review.appendChild(sum);
            for (var i = 0; i < changes.length; i++) {
              var c = changes[i], box = el('div', {class: 'da-change'});
              box.appendChild(el('div', {class: 'da-change-label', text: c.label}));
              if (typeof window.editorDiffRender === 'function') {
                var dd = el('div'); dd.innerHTML = window.editorDiffRender(c.current || '', c.value || '');
                box.appendChild(dd);
              } else box.appendChild(el('div', {class: 'da-val', text: c.value || '(removed)'}));
              review.appendChild(box);
            }
            if (d.checklist && d.checklist.length) {
              review.appendChild(el('div', {class: 'da-warn', text: 'Still to fix after this change: ' + d.checklist.join('; ')}));
            }
            apply.style.display = '';
          }

          apply.onclick = function() {
            if (!proposal) return;
            apply.disabled = true; apply.textContent = 'Applying...';
            fetch(cfg.base + '/revise', {method: 'POST', headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({proposal: proposal.proposal, from: proposal.from})})
              .then(function(r) { if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); }); location.reload(); })
              .catch(function(e) { apply.disabled = false; apply.textContent = 'Apply this change'; window.uiAlert('Could not apply: ' + ((e && e.message) || e)); });
          };

          var chat = window.uiAssistChat({
            web: true,
            intro: cfg.intro,
            placeholders: {write: cfg.placeholder},
            starters: cfg.starters || [],
            send: window.uiAssistPost(cfg.base + '/assist'),
            // A reply that proposes a change has it drafted below; one that
            // only answers leaves what is shown there alone.
            onResult: function(res) {
              if (res && res.change) draft(res.change);
              return '';
            }
          });
          body.appendChild(el('div', {class: 'da-chat'}, [chat.right]));
          body.appendChild(review);
          // In the footer beside the dialog's own Close.
          api.addAction(apply);
          if (ask) chat.say(ask); else chat.focus();
        }
      });
    }
    opener = open;
    var b = el('button', {id: 'definition-assist', type: 'button', class: 'ui-form-assist', text: '✨ Get assistance',
      title: 'Talk about the whole ' + cfg.noun + ' and review the changes it proposes'});
    b.onclick = function() { open(''); };
    var tries = 0;
    (function mount() {
      if (document.getElementById('definition-assist')) return;
      var slot = document.querySelector('.ui-section .ui-section-h-r');
      if (slot) { slot.insertBefore(b, slot.firstChild); return; }
      if (tries++ < 180) requestAnimationFrame(mount);
    })();
  };
  // Opens the dialog with a request already sent: a finding that names its
  // own fix.
  window.orchDefinitionAssistOpen = function(ask) { if (opener) opener(ask || ''); };
})();`
