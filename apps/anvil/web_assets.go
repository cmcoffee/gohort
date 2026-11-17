// The browser half of Anvil: a diff card renderer and three client actions.
//
// All of it registers through the framework's extension points (ui.Head →
// uiRegisterBlockRenderer / uiRegisterClientAction) and all of it names Anvil,
// which is exactly why it lives here and not in core/ui. The only shared
// thing it reaches for is uiOpenModal, the framework's modal primitive.
//
// No backticks anywhere in these strings: they are Go raw string literals, so
// a backtick would terminate the literal. String concatenation instead of
// template literals throughout.
package anvil

// anvilCSS styles the diff card. It borrows the runtime's own CSS variables
// (--border, --bg-1, --text-mute, --accent) so the card inherits whatever
// theme the page is in rather than pinning colors that look wrong in one of
// them.
const anvilCSS = `
.anvil-edit {
  margin: 0.5rem 0; border: 1px solid var(--border); border-radius: 8px;
  background: var(--bg-1); overflow: hidden;
}
.anvil-edit-head {
  display: flex; align-items: center; gap: 0.6rem; padding: 0.5rem 0.75rem;
  cursor: pointer; user-select: none;
}
.anvil-edit-head:hover { background: var(--bg-2, rgba(127,127,127,0.06)); }
.anvil-edit-icon { font-size: 0.95rem; opacity: 0.75; }
.anvil-edit-path {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.82rem; font-weight: 600; flex: 1; min-width: 0;
  overflow: hidden; text-overflow: clip; animation: ui-cut-fade-x linear both; animation-timeline: scroll(self inline); white-space: nowrap;
}
.anvil-edit-stat { font-size: 0.74rem; color: var(--text-mute); white-space: nowrap; }
.anvil-edit-stat .anvil-add { color: #16a34a; }
.anvil-edit-stat .anvil-del { color: #dc2626; }
.anvil-edit-revert {
  font-size: 0.72rem; padding: 0.2rem 0.55rem; border-radius: var(--btn-radius, 6px);
  border: 1px solid var(--border); background: transparent; color: var(--text-mute);
  cursor: pointer;
}
.anvil-edit-revert:hover:not(:disabled) { color: #dc2626; border-color: #dc2626; }
.anvil-edit-revert:disabled { opacity: 0.5; cursor: default; }
.anvil-edit-body {
  display: none; border-top: 1px solid var(--border); max-height: 420px;
  overflow: auto; background: var(--bg-0, transparent);
}
.anvil-edit.open .anvil-edit-body { display: block; }
.anvil-edit-body pre {
  margin: 0; padding: 0.6rem 0.75rem; font-size: 0.76rem; line-height: 1.5;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
.anvil-edit-body .l-add { color: #16a34a; }
.anvil-edit-body .l-del { color: #dc2626; }
.anvil-edit-body .l-hunk { color: var(--text-mute); opacity: 0.8; }
.anvil-edit.reverted .anvil-edit-path { text-decoration: line-through; opacity: 0.6; }
.anvil-edit-each { margin-top: 6px; }
.anvil-edit-each summary { cursor: pointer; font-size: 0.78rem; color: var(--text-mute); }
.anvil-edit-each > .anvil-edit-stat { display: block; margin: 6px 0 2px; }
.anvil-plan-list { padding: 0.2rem 0.9rem 0.6rem; display: flex; flex-direction: column; gap: 0.25rem; }
.anvil-plan-step { display: flex; gap: 0.5rem; align-items: baseline; font-size: var(--fs-sm, 0.85rem); }
.anvil-plan-step.done { color: var(--text-mute); }
.anvil-plan-box { min-width: 1em; color: var(--accent); }
.anvil-plan-step.done .anvil-plan-box { color: #16a34a; }
.anvil-check-passed .anvil-edit-icon { color: #16a34a; opacity: 1; }
.anvil-check-failed .anvil-edit-icon { color: #dc2626; opacity: 1; }
.anvil-check-running .anvil-edit-icon { font-family: monospace; }
.anvil-note { font-size: 0.8rem; color: var(--text-mute); margin: 0.4rem 0; }
.anvil-section-h {
  font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.08em;
  color: var(--text-mute); font-weight: 600;
  margin: 0.9rem 0 0.4rem; padding-bottom: 0.25rem;
  border-bottom: 1px solid var(--border);
}
.anvil-section-h:first-child { margin-top: 0; }
.anvil-edit-body .l-note { color: var(--accent, #6366f1); font-weight: 600; }

/* The git chip, under the project picker in the sidebar. */
.anvil-git {
  display: flex; align-items: center; gap: 0.4rem; flex-wrap: wrap;
  margin: 0.35rem 0 0; padding: 0.3rem 0.45rem;
  font-size: 0.72rem; color: var(--text-mute);
  border: 1px solid var(--border); border-radius: 6px;
  background: var(--bg-1); cursor: pointer; user-select: none;
}
.anvil-git:hover { border-color: var(--accent, #6366f1); }
@media (pointer: coarse) { .anvil-git { min-height: var(--btn-h-sm, 2.25rem); } }
.anvil-git-branch {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-weight: 600; color: inherit;
  overflow: hidden; text-overflow: clip; animation: ui-cut-fade-x linear both; animation-timeline: scroll(self inline); white-space: nowrap; max-width: 14ch;
}
.anvil-git-dirty { color: #d97706; }
.anvil-git-clean { color: #16a34a; }
.anvil-git-none { font-style: italic; opacity: 0.75; }
.anvil-out {
  margin: 0; padding: 0.6rem 0.75rem; font-size: 0.76rem; line-height: 1.45;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  white-space: pre-wrap; word-break: break-word;
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 6px;
  max-height: 55vh; overflow: auto;
}
.anvil-row {
  display: flex; align-items: center; gap: 0.6rem; padding: 0.5rem 0;
  border-bottom: 1px solid var(--border);
}
.anvil-row:last-child { border-bottom: none; }
.anvil-row-main { flex: 1; min-width: 0; }
.anvil-row-title { font-size: 0.85rem; font-weight: 600; }
.anvil-row-sub { font-size: 0.75rem; color: var(--text-mute); }
.anvil-btn {
  font-size: 0.78rem; padding: 0.3rem 0.7rem; border-radius: 6px;
  border: 1px solid var(--border); background: transparent; cursor: pointer;
  color: inherit;
}
.anvil-btn:hover:not(:disabled) { border-color: var(--accent, #6366f1); }
.anvil-btn:disabled { opacity: 0.55; cursor: default; }
@media (pointer: coarse) { .anvil-btn, .anvil-edit-revert { min-height: var(--btn-h-sm, 2.25rem); } }
`

// anvilEditBlockJS renders a turn's writes to one file (edit_cards.go) as a
// collapsible diff card with a Revert button. A later write to the file in
// the same turn sends the card again, and it fills in place (onUpdate). With
// more than one write, the diff is the turn's whole change to the file and
// each write's own diff is listed below it. Revert puts back every write the
// card holds, newest first, through the same undo a message's Undo uses.
const anvilEditBlockJS = `function(d) {
  var wrap = document.createElement('div');
  wrap.className = 'anvil-edit';
  var ids = [];
  function fill(d) {
    var data = d.data || {};
    var open = wrap.classList.contains('open');
    wrap.textContent = '';
    wrap.className = 'anvil-edit' + (open ? ' open' : '');
    // Undo from a message finds the changes below it by these (planmode.go).
    ids = String(data.edit_id || '').split(',').filter(Boolean);
    wrap.dataset.editId = ids.join(',');

    var head = document.createElement('div');
    head.className = 'anvil-edit-head';
    var icon = document.createElement('span');
    icon.className = 'anvil-edit-icon';
    icon.textContent = data.kind === 'create' ? '+' : (data.kind === 'delete' ? '\u2212' : '\u270e');
    head.appendChild(icon);
    var path = document.createElement('span');
    path.className = 'anvil-edit-path';
    path.textContent = d.title || '';
    path.title = d.title || '';
    head.appendChild(path);
    var stat = document.createElement('span');
    stat.className = 'anvil-edit-stat';
    stat.textContent = d.text || '';
    head.appendChild(stat);

    // A card that was already answered comes back settled, with no controls.
    // Its stored life does not end when the button is clicked, so without this
    // every reload offers to revert an edit that is already back.
    if (d.resolved) {
      var settled = document.createElement('span');
      settled.className = 'anvil-edit-stat';
      settled.textContent = d.resolved;
      head.appendChild(settled);
      wrap.classList.add('reverted');
    }

    var revert = document.createElement('button');
    revert.className = 'anvil-edit-revert';
    revert.textContent = 'Revert';
    if (d.resolved) revert.style.display = 'none';
    revert.addEventListener('click', async function(ev) {
      ev.stopPropagation();
      var what = ids.length > 1 ? 'before these ' + ids.length + ' edits' : 'before this edit';
      if (!(await window.anvilConfirm('Restore ' + (d.title || 'this file') + ' to what it was ' + what + '?'))) return;
      revert.disabled = true;
      revert.textContent = 'Reverting...';
      window.anvilFetchJSON('api/undo', {method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({ids: ids})})
        .then(function(r) {
          if (((r && r.reverted) || []).length < ids.length) {
            revert.disabled = false;
            revert.textContent = 'Revert';
            anvilAlert((r && r.message) || 'Not every edit could be put back.');
          } else {
            wrap.anvilReverted();
          }
          if (window.anvilGitRefresh) window.anvilGitRefresh();
        })
        .catch(function(err) {
          revert.disabled = false;
          revert.textContent = 'Revert';
          anvilAlert(String(err.message || err));
        });
    });
    head.appendChild(revert);
    // anvilReverted settles the card once its change is back, here or by an
    // undo from a message.
    wrap.anvilReverted = function() {
      wrap.classList.add('reverted');
      revert.style.display = 'none';
      stat.textContent = 'reverted';
      // Settle it on the SERVER too, not just in this DOM. Skipping this is
      // how an answered card comes back armed on the next load.
      if (window.uiResolveBlock) {
        try { window.uiResolveBlock(d.id, 'Reverted: the file is back to what it was'); } catch (e) {}
      }
    };

    var body = document.createElement('div');
    body.className = 'anvil-edit-body';
    var pre = document.createElement('pre');
    if (data.diff) window.anvilPaintDiff(pre, data.diff);
    else pre.textContent = '(no diff recorded)';
    body.appendChild(pre);
    var each = [];
    try { each = data.edits ? JSON.parse(data.edits) : []; } catch (e) { each = []; }
    if (each.length > 1) {
      var list = document.createElement('details');
      list.className = 'anvil-edit-each';
      var sum = document.createElement('summary');
      sum.textContent = 'Each edit (' + each.length + ')';
      list.appendChild(sum);
      each.forEach(function(w, i) {
        var lab = document.createElement('div');
        lab.className = 'anvil-edit-stat';
        lab.textContent = 'Edit ' + (i + 1) + ' \u00b7 +' + (w.added || 0) + ' \u2212' + (w.removed || 0);
        list.appendChild(lab);
        var p = document.createElement('pre');
        if (w.diff) window.anvilPaintDiff(p, w.diff);
        list.appendChild(p);
      });
      body.appendChild(list);
    }
    head.addEventListener('click', function() { wrap.classList.toggle('open'); });
    wrap.appendChild(head);
    wrap.appendChild(body);
    // The tree just changed, so anything showing its state is now stale. This
    // is the whole reason the chip is event-driven rather than polled.
    if (window.anvilGitRefresh) window.anvilGitRefresh();
  }
  fill(d);
  return {wrap: wrap, body: null, onUpdate: fill};
}`

// anvilCheckBlockJS renders one run of the project's check: a spinner and
// the seconds while it runs, then passed or failed with its output. A failed
// check opens on its output; a passed one keeps it one click away. The live
// labels change their text node in place, never the element.
const anvilCheckBlockJS = `function(d) {
  var frames = ['\u280b','\u2819','\u2839','\u2838','\u283c','\u2834','\u2826','\u2827','\u2807','\u280f'];
  var wrap = document.createElement('div');
  // fill draws a run of the check into the card: on arrival, and again when
  // the same run is sent again with how it ended (the card updates in place).
  function fill(d) {
    if (wrap._timer) { clearInterval(wrap._timer); wrap._timer = null; }
    var data = d.data || {};
    var state = data.state || d.text || 'running';
    wrap.className = 'anvil-edit anvil-check anvil-check-' + state + (state === 'failed' ? ' open' : '');
    wrap.textContent = '';
    var head = document.createElement('div');
    head.className = 'anvil-edit-head';
    var icon = document.createElement('span');
    icon.className = 'anvil-edit-icon';
    var iconText = document.createTextNode(state === 'passed' ? '\u2713' : (state === 'failed' ? '\u2717' : frames[0]));
    icon.appendChild(iconText);
    head.appendChild(icon);
    var label = document.createElement('span');
    label.className = 'anvil-edit-path';
    var verb = state === 'passed' ? 'Check passed: ' : (state === 'failed' ? 'Check failed: ' : 'Checking: ');
    label.textContent = verb + (d.title || '');
    label.title = d.title || '';
    head.appendChild(label);
    var stat = document.createElement('span');
    stat.className = 'anvil-edit-stat';
    var statText = document.createTextNode('');
    stat.appendChild(statText);
    head.appendChild(stat);
    wrap.appendChild(head);
    if (state === 'running') {
      var started = Date.parse(data.started || '') || Date.now();
      var n = 0;
      wrap._timer = setInterval(function() {
        n++;
        if (n > 1 && !wrap.isConnected) { clearInterval(wrap._timer); wrap._timer = null; return; }
        iconText.nodeValue = frames[n % frames.length];
        var secs = Math.max(0, Math.round((Date.now() - started) / 1000));
        statText.nodeValue = secs >= 3 ? secs + 's' : '';
      }, 120);
    } else {
      statText.nodeValue = (data.took || '0') + 's';
    }
    if (data.output) {
      var body = document.createElement('div');
      body.className = 'anvil-edit-body';
      var pre = document.createElement('pre');
      pre.textContent = data.output;
      body.appendChild(pre);
      wrap.appendChild(body);
      head.addEventListener('click', function() { wrap.classList.toggle('open'); });
    }
  }
  fill(d);
  return {wrap: wrap, body: null, onUpdate: fill};
}`

// anvilStepSummariesJS says in words what each of Anvil's tools did, for the
// step lines the panel shows as a turn runs (AgentLoopPanel.ShowSteps). Each
// reads the call's arguments, and its output once it has one.
// anvilHabitsJS is Undo on each of your messages and "# remember this"
// (planmode.go).
const anvilHabitsJS = `
(function() {
  // The changes shown below a message and still in place.
  function changesAfter(bubble) {
    var cards = document.querySelectorAll('.anvil-edit:not(.reverted)');
    return Array.prototype.filter.call(cards, function(c) {
      return c.dataset.editId && (bubble.compareDocumentPosition(c) & Node.DOCUMENT_POSITION_FOLLOWING);
    });
  }
  async function undo(ctx) {
    var cards = changesAfter(ctx.bubble);
    if (!cards.length) { showToast('Nothing to undo: no change from this message on is still in place.'); return; }
    var count = cards.reduce(function(n, c) { return n + c.dataset.editId.split(',').length; }, 0);
    var n = count === 1 ? 'the change' : 'the ' + count + ' changes';
    if (!(await window.anvilConfirm('Put back ' + n + ' the assistant made from this message on? Each file goes back to what it was before.'))) return;
    var btn = ctx.button, label = btn ? btn.textContent : '';
    if (btn) { btn.disabled = true; btn.textContent = 'Undoing...'; }
    window.anvilFetchJSON('api/undo', {method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({ids: [].concat.apply([], cards.map(function(c) { return c.dataset.editId.split(','); }))})})
      .then(function(d) {
        var back = {};
        (d.reverted || []).forEach(function(id) { back[id] = true; });
        // A card holds a turn's writes to one file: settled once all are back.
        cards.forEach(function(c) {
          if (c.anvilReverted && c.dataset.editId.split(',').every(function(id) { return back[id]; })) c.anvilReverted();
        });
        if ((d.reverted || []).length < count) window.anvilAlert(d.message);
        else showToast(d.message);
        if (window.anvilGitRefresh) window.anvilGitRefresh();
      })
      .catch(function(err) { window.anvilAlert(String(err.message || err)); })
      .then(function() { if (btn) { btn.disabled = false; btn.textContent = label; } });
  }
  function register() {
    if (!window.uiRegisterBubbleAction) { setTimeout(register, 50); return; }
    window.uiRegisterBubbleAction({role: 'user', label: 'Undo',
      title: 'Put back every file the assistant changed from this message on', onclick: undo});
  }
  register();

  // "# the build needs CGO_ENABLED=0": one line starting with # is kept in
  // what the assistant knows about the project, not sent.
  if (window.uiRegisterSendInterceptor) {
    window.uiRegisterSendInterceptor(function(ctx) {
      var text = String(ctx.text || '');
      if (text.charAt(0) !== '#' || /\n/.test(text) || ctx.attachments) return false;
      var line = text.replace(/^#+/, '').trim();
      var project = window.anvilProject ? window.anvilProject() : '';
      if (!line || !project) return false;
      window.anvilFetchJSON('api/notes/add?project_id=' + encodeURIComponent(project), {method: 'POST',
        headers: {'Content-Type': 'application/json'}, body: JSON.stringify({text: line})})
        .then(function(d) { ctx.toast((d && d.message) || 'Remembered.'); })
        .catch(function(err) {
          if (ctx.input && !ctx.input.value) ctx.input.value = text;
          window.anvilAlert(String(err.message || err));
        });
      return true;
    });
  }
})();
`

const anvilStepSummariesJS = `
(function() {
  if (!window.uiRegisterStepSummary) return;
  var R = window.uiRegisterStepSummary;
  function first(out) { return String(out || '').split('\n')[0].trim(); }
  function quoted(s) { s = String(s || ''); return '"' + (s.length > 60 ? s.slice(0, 60) + '...' : s) + '"'; }
  function failed(out) { return /^\s*error:/i.test(String(out || '')); }
  R('read_file', function(a, out) {
    if (out == null) return 'Reading ' + (a.path || 'a file');
    if (failed(out)) return 'Could not read ' + (a.path || 'the file');
    var m = first(out).match(/\(lines? ([^)]*)\)/);
    return 'Read ' + (a.path || 'a file') + (m ? ' (lines ' + m[1] + ')' : '');
  });
  R('list_files', function(a, out) {
    var where = a.path ? a.path : 'the project';
    if (out == null) return 'Listing ' + where;
    return 'Listed ' + where;
  });
  R('search_code', function(a, out) {
    var what = 'Searched for ' + quoted(a.pattern) + (a.path_glob ? ' in ' + a.path_glob : '');
    if (out == null) return 'Searching for ' + quoted(a.pattern);
    if (/^No matches/.test(String(out))) return what + ': no matches';
    var hits = String(out).split('\n').filter(function(l) { return /^[^ ].*:\d+:/.test(l); }).length;
    return what + ': ' + hits + (hits === 1 ? ' hit' : ' hits');
  });
  R('edit_file', function(a, out) {
    if (out == null) return 'Editing ' + (a.path || 'a file');
    if (failed(out)) return 'Edit to ' + (a.path || 'a file') + ' refused';
    var m = first(out).match(/\(([^)]*)\)/);
    return 'Edited ' + (a.path || 'a file') + (m ? ' (' + m[1] + ')' : '');
  });
  R('create_file', function(a, out) {
    if (out == null) return 'Creating ' + (a.path || 'a file');
    return (failed(out) ? 'Could not create ' : 'Created ') + (a.path || 'a file');
  });
  R('delete_file', function(a, out) {
    if (out == null) return 'Deleting ' + (a.path || 'a file');
    return (failed(out) ? 'Could not delete ' : 'Deleted ') + (a.path || 'a file');
  });
  R('run', function(a, out) {
    if (out == null) return 'Running ' + quoted(a.command);
    if (/^Command failed/.test(String(out)) || failed(out)) return 'Failed: ' + quoted(a.command);
    return 'Ran ' + quoted(a.command);
  });
  R('check', function(a, out) {
    if (out == null) return 'Running the check';
    return first(out).replace(/:$/, '');
  });
  R('git', function(a, out) {
    var what = 'git ' + (a.action || '') + (a.path ? ' ' + a.path : '');
    return out == null ? what : what.trim();
  });
  R('plan', function(a, out) {
    if (out == null) return 'Planning';
    return first(out).split('. Next')[0].replace(/\.$/, '');
  });

  // Looking around shares one line, as in Claude Code ("Read 3 files,
  // searched twice"); edits, commands and the check keep a line each. Plan
  // updates go with it: the plan card shows where the plan stands.
  function times(n) { return n === 1 ? 'once' : n === 2 ? 'twice' : n + ' times'; }
  function count(steps, names) {
    return steps.filter(function(s) { return names.indexOf(s.name) >= 0; }).length;
  }
  function said(parts) {
    var line = parts.filter(Boolean).join(', ');
    return line ? line.charAt(0).toUpperCase() + line.slice(1) : '';
  }
  if (window.uiRegisterStepGroup) {
    window.uiRegisterStepGroup('look', ['read_file', 'list_files', 'search_code', 'git', 'plan'], function(steps) {
      var r = count(steps, ['read_file']), l = count(steps, ['list_files']), q = count(steps, ['search_code']), g = count(steps, ['git']);
      return said([
        r ? 'read ' + r + (r === 1 ? ' file' : ' files') : '',
        l ? 'listed ' + l + (l === 1 ? ' folder' : ' folders') : '',
        q ? 'searched ' + times(q) : '',
        g ? 'looked at git' + (g > 1 ? ' ' + times(g) : '') : '',
      ]) || 'Updated the plan';
    });
  }
  // A finished stretch of work's folded line, all it says ("Read 3 files,
  // changed 1"): what it read, looked through, changed and ran.
  if (window.uiRegisterTurnSummary) {
    window.uiRegisterTurnSummary(function(steps) {
      var r = count(steps, ['read_file']), l = count(steps, ['list_files']), q = count(steps, ['search_code']),
          g = count(steps, ['git']), e = count(steps, ['edit_file', 'create_file']),
          d = count(steps, ['delete_file']), c = count(steps, ['run']), k = count(steps, ['check']);
      return said([
        r ? 'read ' + r + (r === 1 ? ' file' : ' files') : '',
        l ? 'listed ' + l + (l === 1 ? ' folder' : ' folders') : '',
        q ? 'searched ' + times(q) : '',
        g ? 'looked at git' + (g > 1 ? ' ' + times(g) : '') : '',
        e ? 'changed ' + e + (e === 1 ? ' file' : ' files') : '',
        d ? 'deleted ' + d + (d === 1 ? ' file' : ' files') : '',
        c ? 'ran ' + c + (c === 1 ? ' command' : ' commands') : '',
        k ? 'ran the check' + (k > 1 ? ' ' + times(k) : '') : '',
      ]);
    });
  }
})();
`

// anvilPlanBlockJS renders the agent's plan: its steps as a checklist, each
// ticked as the agent finishes it.
const anvilPlanBlockJS = `function(d) {
  var wrap = document.createElement('div');
  // fill draws the plan into the card: on arrival, and again as steps are
  // ticked (the same plan is sent again; the card updates in place).
  function fill(d) {
    var data = d.data || {};
    var steps = [];
    try { steps = JSON.parse(data.steps || '[]'); } catch (_) {}
    wrap.className = 'anvil-edit anvil-plan open';
    wrap.textContent = '';
    var head = document.createElement('div');
    head.className = 'anvil-edit-head';
    var title = document.createElement('span');
    title.className = 'anvil-edit-path';
    title.textContent = 'Plan';
    head.appendChild(title);
    var done = steps.filter(function(s) { return s.done; }).length;
    var stat = document.createElement('span');
    stat.className = 'anvil-edit-stat';
    stat.textContent = done + ' of ' + steps.length + ' done';
    head.appendChild(stat);
    wrap.appendChild(head);
    var list = document.createElement('div');
    list.className = 'anvil-plan-list';
    steps.forEach(function(s) {
      var row = document.createElement('div');
      row.className = 'anvil-plan-step' + (s.done ? ' done' : '');
      var box = document.createElement('span');
      box.className = 'anvil-plan-box';
      box.textContent = s.done ? '\u2713' : '\u25cb';
      row.appendChild(box);
      var t = document.createElement('span');
      t.textContent = s.text || '';
      row.appendChild(t);
      list.appendChild(row);
    });
    wrap.appendChild(list);
  }
  fill(d);
  return {wrap: wrap, body: null, onUpdate: fill};
}`

// anvilCurrentProjectJS is the shared helper the client actions use to learn
// which project is selected. The picker is an AgentLoopPanel ExtraField, whose
// Name is both the POST key and the DOM id — so the answer the buttons act on
// is by construction the same one the next message will carry.
const anvilCurrentProjectJS = `
// anvilPickerEl finds the project <select> the panel renders for the
// project_id extra field.
//
// By id first, which is the framework's documented contract for a ChatField
// name. The class-based fallback is not defensiveness for its own sake: the
// id was MISSING from the runtime until it was fixed, every client action
// here silently read an empty string, and the only symptom was a JSON parse
// error in a modal several layers away. A picker that cannot be found should
// say so at the point it is not found.
window.anvilPickerEl = function() {
  var el = document.getElementById('project_id');
  if (el) return el;
  return document.querySelector('.ui-agent-extras select');
};
window.anvilProject = function() {
  var el = window.anvilPickerEl();
  return el ? el.value : '';
};
window.anvilProjectName = function() {
  var el = window.anvilPickerEl();
  if (!el || el.selectedIndex < 0) return 'this project';
  return el.options[el.selectedIndex].textContent;
};
// anvilRequireProject returns the open project id, or shows why it could not
// and returns ''. Callers stop on the empty string rather than issuing a
// request that can only fail.
window.anvilRequireProject = function(body) {
  var pid = window.anvilProject();
  if (!pid) {
    body.innerHTML = '';
    body.appendChild(window.anvilNote(
      'No project is selected. If the picker is missing from the sidebar, this page was served by an older build. Reload, and if it persists the project picker failed to render.'));
    return '';
  }
  return pid;
};
window.anvilRow = function(title, sub) {
  var row = document.createElement('div');
  row.className = 'anvil-row';
  var main = document.createElement('div');
  main.className = 'anvil-row-main';
  var t = document.createElement('div');
  t.className = 'anvil-row-title';
  t.textContent = title;
  main.appendChild(t);
  if (sub) {
    var s = document.createElement('div');
    s.className = 'anvil-row-sub';
    s.textContent = sub;
    main.appendChild(s);
  }
  row.appendChild(main);
  return row;
};
// anvilFetchJSON is fetch + JSON with the failure cases actually handled.
//
// Two of them, both of which bit already. A non-2xx response carries the
// server's own sentence under "error", and reporting THAT beats reporting
// whatever the parser made of the body. And a plain .then(r => r.json())
// treats an error object as a successful result, so an endpoint answering
// {"error": "..."} renders as an empty list — a failure that looks exactly
// like "there is nothing here".
// Never window.confirm in the desktop client: its shim cannot answer a
// synchronous browser dialog. It used to return true unconditionally, so
// "Remove the project and delete its checkout" ran on the click; it now refuses,
// which means a bare confirm() here would silently do nothing instead. uiConfirm
// is the framework dialog, backed by a real modal in both clients, and is async.
window.anvilConfirm = function(msg) {
  if (window.uiConfirm) return Promise.resolve(window.uiConfirm(msg));
  return Promise.resolve(window.confirm(msg));
};
// The framework's alert, so a failure reads like every other notice on the
// page; the browser's only where the runtime is missing.
window.anvilAlert = function(msg) {
  if (window.uiAlert) return window.uiAlert(msg);
  window.alert(msg);
};
window.anvilFetchJSON = function(url, opts) {
  return fetch(url, Object.assign({credentials: 'same-origin'}, opts || {}))
    .then(function(r) {
      return r.text().then(function(text) {
        var body = null;
        try { body = text ? JSON.parse(text) : null; } catch (e) { body = null; }
        if (!r.ok) {
          var msg = (body && body.error) ? body.error : (text || ('HTTP ' + r.status));
          throw new Error(msg);
        }
        if (body === null && text) throw new Error('the server sent something that is not JSON: ' + text.slice(0, 200));
        return body;
      });
    });
};
// anvilPaintDiff colorizes a unified diff into an element. Shared by the chat
// card and both halves of the Changes modal, so a diff looks the same
// wherever it is read — three implementations of this would drift into three
// slightly different ideas of what a hunk header looks like.
window.anvilPaintDiff = function(pre, diff) {
  pre.innerHTML = '';
  String(diff || '').split('\n').forEach(function(line) {
    var span = document.createElement('span');
    if (line.indexOf('@@') === 0) span.className = 'l-hunk';
    else if (line.indexOf('#') === 0) span.className = 'l-note';
    else if (line.indexOf('+') === 0 && line.indexOf('+++') !== 0) span.className = 'l-add';
    else if (line.indexOf('-') === 0 && line.indexOf('---') !== 0) span.className = 'l-del';
    span.textContent = line + '\n';
    pre.appendChild(span);
  });
};
window.anvilNote = function(text) {
  var p = document.createElement('p');
  p.className = 'anvil-note';
  p.textContent = text;
  return p;
};
`

// anvilRunJS opens the current project's operator-defined commands.
//
// A modal rather than a row of toolbar buttons: the toolbar renders once, the
// project picker changes underneath it, and a button labelled with another
// project's command is worse than no button at all.
const anvilRunJS = `function(ctx) {
  var m = window.uiOpenModal({title: 'Run', subtitle: window.anvilProjectName(), width: '680px'});
  var body = m.body;
  var pid = window.anvilRequireProject(body);
  if (!pid) return;
  var actions = (window.ANVIL_ACTIONS || {})[pid] || [];
  if (!actions.length) {
    body.appendChild(window.anvilNote('This project has no commands configured. An admin can add build, test or restart commands to it from Projects.'));
    return;
  }
  var out = document.createElement('pre');
  out.className = 'anvil-out';
  out.style.display = 'none';

  actions.forEach(function(a) {
    var row = window.anvilRow(a.label, a.detached ? 'Runs detached, so there is no result to report back' : '');
    var btn = document.createElement('button');
    btn.className = 'anvil-btn';
    btn.textContent = 'Run';
    btn.addEventListener('click', async function() {
      if (a.confirm && !(await window.anvilConfirm(a.confirm))) return;
      btn.disabled = true;
      btn.textContent = 'Running...';
      out.style.display = 'block';
      out.textContent = a.label + '...';
      window.anvilFetchJSON('api/project/' + encodeURIComponent(pid) + '/action/' + encodeURIComponent(a.id),
            {method: 'POST'})
        .then(function(res) {
          btn.disabled = false;
          btn.textContent = 'Run';
          if (res.gate_failed) {
            out.textContent = res.output || (res.gate_label + ' did not pass, so ' + a.label + ' did not run.');
            return;
          }
          out.textContent = (res.ok ? '' : a.label + ' failed.\n\n') + (res.output || '');
        })
        .catch(function(err) {
          btn.disabled = false;
          btn.textContent = 'Run';
          out.textContent = 'Could not run: ' + (err.message || err);
        });
    });
    row.appendChild(btn);
    body.appendChild(row);
  });
  body.appendChild(out);
}`

// anvilHistoryJS lists every edit recorded against the current project, so a
// change made three sessions ago is still revertable. The chat cards cover the
// current conversation; this covers everything else.
const anvilHistoryJS = `function(ctx) {
  var m = window.uiOpenModal({title: 'Changes', subtitle: window.anvilProjectName(), width: '820px'});
  var body = m.body;
  var pid = window.anvilRequireProject(body);
  if (!pid) return;
  body.innerHTML = '';

  function heading(text, hint) {
    var h = document.createElement('div');
    h.className = 'anvil-section-h';
    h.textContent = text;
    if (hint) h.title = hint;
    return h;
  }

  // Reusable expandable diff row. Both halves of this modal show a path with
  // a diff underneath it, so they share one.
  function diffRow(title, sub, loadDiff) {
    var wrap = document.createElement('div');
    wrap.className = 'anvil-edit';
    var head = document.createElement('div');
    head.className = 'anvil-edit-head';
    var path = document.createElement('span');
    path.className = 'anvil-edit-path';
    path.textContent = title;
    path.title = title;
    head.appendChild(path);
    var stat = document.createElement('span');
    stat.className = 'anvil-edit-stat';
    stat.textContent = sub || '';
    head.appendChild(stat);
    var bodyEl = document.createElement('div');
    bodyEl.className = 'anvil-edit-body';
    var pre = document.createElement('pre');
    bodyEl.appendChild(pre);
    var loaded = false;
    head.addEventListener('click', function() {
      wrap.classList.toggle('open');
      if (loaded || !wrap.classList.contains('open')) return;
      loaded = true;
      pre.textContent = 'Loading...';
      loadDiff(function(text, err) {
        pre.innerHTML = '';
        if (err) { pre.textContent = err; return; }
        window.anvilPaintDiff(pre, text || '(no textual difference)');
      });
    });
    wrap.appendChild(head);
    wrap.appendChild(bodyEl);
    return {wrap: wrap, head: head, stat: stat};
  }

  // --- what is different in the project right now --------------------------
  var treeBox = document.createElement('div');
  body.appendChild(treeBox);
  var journalBox = document.createElement('div');
  body.appendChild(journalBox);

  window.anvilFetchJSON('api/project/' + encodeURIComponent(pid) + '/worktree')
    .then(function(res) {
      if (!res || !res.is_repo) {
        treeBox.appendChild(heading('Working tree'));
        treeBox.appendChild(window.anvilNote('This project is not under git, so there is no working tree to compare.'));
        return;
      }
      var rows = res.changes || [];
      treeBox.appendChild(heading('Working tree (' + rows.length + ')',
        'Everything that differs from the last commit, including your own edits, not just the assistant\'s.'));
      if (!rows.length) {
        treeBox.appendChild(window.anvilNote('Clean: nothing differs from the last commit.'));
        return;
      }
      rows.forEach(function(ch) {
        var sub = ch.state + (ch.staged ? ' · staged' : '');
        var row = diffRow(ch.path, sub, function(done) {
          var url = 'api/project/' + encodeURIComponent(pid) + '/worktree?path=' + encodeURIComponent(ch.path) +
                    (ch.untracked ? '&untracked=1' : '');
          window.anvilFetchJSON(url)
            .then(function(d) { done(d && d.diff); })
            .catch(function(err) { done(null, String(err.message || err)); });
        });
        treeBox.appendChild(row.wrap);
      });
    })
    .catch(function(err) {
      treeBox.appendChild(heading('Working tree'));
      treeBox.appendChild(window.anvilNote('Could not read the working tree: ' + (err.message || err)));
    });

  // --- what Anvil itself did, and can undo ---------------------------------
  window.anvilFetchJSON('api/edits?project_id=' + encodeURIComponent(pid))
    .then(function(rows) {
      journalBox.appendChild(heading("Anvil's edits (" + ((rows && rows.length) || 0) + ')',
        'Edits made through this app, each revertable to exactly what was there before.'));
      if (!rows || !rows.length) {
        journalBox.appendChild(window.anvilNote('Anvil has not changed anything in this project yet.'));
        return;
      }
      rows.forEach(function(e) {
        var when = e.at ? new Date(e.at).toLocaleString() : '';
        var sub = when + ' · +' + (e.added || 0) + ' −' + (e.removed || 0);
        if (e.reverted) sub += ' · reverted';
        var row = diffRow(e.path, sub, function(done) { done(e.diff); });
        if (!e.reverted) {
          var btn = document.createElement('button');
          btn.className = 'anvil-edit-revert';
          btn.textContent = 'Revert';
          btn.addEventListener('click', async function(ev) {
            ev.stopPropagation();
            if (!(await window.anvilConfirm('Restore ' + e.path + ' to what it was before this edit?'))) return;
            btn.disabled = true;
            btn.textContent = 'Reverting...';
            window.anvilFetchJSON('api/edit/' + encodeURIComponent(e.id) + '/revert', {method: 'POST'})
              .then(function() {
                btn.textContent = 'Reverted';
                if (window.anvilGitRefresh) window.anvilGitRefresh();
              })
              .catch(function(err) {
                btn.disabled = false;
                btn.textContent = 'Revert';
                anvilAlert(String(err.message || err));
              });
          });
          row.head.appendChild(btn);
        }
        journalBox.appendChild(row.wrap);
      });
    })
    .catch(function(err) {
      journalBox.appendChild(heading("Anvil's edits"));
      journalBox.appendChild(window.anvilNote('Could not load the change history: ' + (err.message || err)));
    });
}`

// anvilPermissionsJS lists the standing approvals in force for the current
// project, and revokes them.
//
// This is the other half of the "Always allow" button. A grant handed out in
// one click during a task has to be visible and removable somewhere that
// isn't mid-conversation — otherwise it is a decision you can make instantly
// and never take back, which is not a trade worth offering.
const anvilPermissionsJS = `function(ctx) {
  var m = window.uiOpenModal({title: 'Permissions', subtitle: window.anvilProjectName(), width: '640px'});
  var body = m.body;
  var pid = window.anvilRequireProject(body);
  if (!pid) return;
  body.appendChild(window.anvilNote('Loading...'));

  function render(rows) {
    body.innerHTML = '';
    if (!rows || !rows.length) {
      body.appendChild(window.anvilNote('No standing approvals. Every command and delete asks first.'));
      return;
    }
    body.appendChild(window.anvilNote('These run without asking. Everything else still stops for you.'));
    rows.forEach(function(g) {
      var what = g.prefix ? (g.prefix + '...') : g.tool;
      var when = g.at ? new Date(g.at).toLocaleString() : '';
      var row = window.anvilRow(what, 'allowed ' + when);
      var btn = document.createElement('button');
      btn.className = 'anvil-btn';
      btn.textContent = 'Revoke';
      btn.addEventListener('click', function() {
        btn.disabled = true;
        btn.textContent = 'Revoking...';
        window.anvilFetchJSON('api/grant/' + encodeURIComponent(g.id) + '?project_id=' + encodeURIComponent(pid),
              {method: 'DELETE'})
          .then(function() { return window.anvilFetchJSON('api/grants?project_id=' + encodeURIComponent(pid)); })
          .then(render)
          .catch(function(err) {
            btn.disabled = false;
            btn.textContent = 'Revoke';
            anvilAlert('Could not revoke: ' + (err.message || err));
          });
      });
      row.appendChild(btn);
      body.appendChild(row);
    });
  }

  window.anvilFetchJSON('api/grants?project_id=' + encodeURIComponent(pid))
    .then(render)
    .catch(function(err) {
      body.innerHTML = '';
      body.appendChild(window.anvilNote('Could not load permissions: ' + (err.message || err)));
    });
}`

// anvilNotesJS shows what the assistant has written about this project, and
// lets the operator correct or clear it.
//
// The notes block renders nearest the top of the prompt and the model rewrites
// it on its own, unprompted and unreviewed, so a note that has gone stale
// steers every turn on this tree with nowhere to go and look. This is that
// place. A build command that stopped being true is worse than no memory at
// all, because it is stated with exactly the confidence of one that works.
const anvilNotesJS = `function(ctx) {
  var m = window.uiOpenModal({title: 'What Anvil learned', subtitle: window.anvilProjectName(), width: '680px'});
  var body = m.body;
  var pid = window.anvilRequireProject(body);
  if (!pid) return;
  body.appendChild(window.anvilNote('Loading...'));

  // Save and Clear sit in the footer beside Close, not under the long text
  // box where they scrolled away. render runs again after every save, so it
  // takes the last render's out of the footer before adding its own.
  var footerBtns = [];
  function clearFooter() {
    footerBtns.forEach(function(b) { b.remove(); });
    footerBtns = [];
  }

  function render(d) {
    body.innerHTML = '';
    clearFooter();
    if (d.from_seed) {
      body.appendChild(window.anvilNote('Anvil has not written anything about this project yet. Below is the starting shape it was given.'));
    } else {
      var when = d.updated_at ? new Date(d.updated_at).toLocaleString() : '';
      body.appendChild(window.anvilNote('Anvil keeps this in front of itself on every turn' + (when ? ', last rewritten ' + when : '') + '. Your project notes and the repository instructions still outrank it.'));
    }
    var ta = document.createElement('textarea');
    ta.className = 'anvil-notes-text';
    ta.rows = 14;
    ta.style.width = '100%';
    ta.value = d.text || '';
    body.appendChild(ta);

    var count = window.anvilNote('');
    function tick() {
      var n = (ta.value || '').length;
      count.textContent = n + ' / ' + d.cap + (n > d.cap ? ': over the cap, the tail is dropped on save' : '');
    }
    ta.addEventListener('input', tick);
    tick();
    body.appendChild(count);

    if (!d.can_edit) {
      ta.disabled = true;
      body.appendChild(window.anvilNote('You can open this project but not change it, so these are read-only here.'));
      return;
    }

    var clear = document.createElement('button');
    clear.className = 'ui-row-btn';
    clear.textContent = 'Clear';
    clear.title = 'Forget all of it and start again from the beginning shape';
    var save = document.createElement('button');
    save.className = 'ui-row-btn primary';
    save.textContent = 'Save';
    footerBtns.push(m.addAction(clear), m.addAction(save));

    function post(text, btn, busy) {
      save.disabled = true;
      clear.disabled = true;
      var was = btn.textContent;
      btn.textContent = busy;
      window.anvilFetchJSON('api/notes?project_id=' + encodeURIComponent(pid),
            {method: 'POST', body: JSON.stringify({text: text})})
        .then(function() { return window.anvilFetchJSON('api/notes?project_id=' + encodeURIComponent(pid)); })
        .then(render)
        .catch(function(err) {
          save.disabled = false;
          clear.disabled = false;
          btn.textContent = was;
          anvilAlert('Could not save: ' + (err.message || err));
        });
    }
    save.addEventListener('click', function() { post(ta.value, save, 'Saving...'); });
    clear.addEventListener('click', async function() {
      if (!(await window.anvilConfirm('Clear what Anvil has learned about this project? It starts again from the beginning shape.'))) return;
      post('', clear, 'Clearing...');
    });
  }

  window.anvilFetchJSON('api/notes?project_id=' + encodeURIComponent(pid))
    .then(render)
    .catch(function(err) {
      body.innerHTML = '';
      clearFooter();
      body.appendChild(window.anvilNote('Could not load the notes: ' + (err.message || err)));
    });
}`

// anvilGitChipJS renders the git line under the project picker and keeps it
// current.
//
// Anvil runs in a browser with no IDE beside it, so the two facts a terminal
// would have shown for free — which branch this is, and whether the tree is
// clean — have nowhere else to come from. Getting them wrong is expensive in a
// way that is specific to an agent editing files: "the agent changed six
// files" reads very differently depending on whether you were on main.
//
// Refreshed on arrival, when the project changes, when an edit card lands
// (the tree just moved), and on click. Deliberately NOT polled: the states it
// reports only change when something in this session changed them, so a timer
// would be a request every few seconds to re-learn what the page already knew.
const anvilGitChipJS = `
// Asked for in bursts (the page arriving, the picker settling, every edit
// card of a reopened session), each asking costs a git run, so a burst
// becomes one request, and one asked while another is out runs once after.
(function() {
  var timer = null, busy = false, again = false;
  function run() {
    if (busy) { again = true; return; }
    busy = true;
    anvilGitPaint(function() {
      busy = false;
      if (again) { again = false; run(); }
    });
  }
  window.anvilGitRefresh = function() {
    clearTimeout(timer);
    timer = setTimeout(run, 150);
  };
})();
function anvilGitPaint(done) {
  var host = document.querySelector('.ui-agent-extras');
  if (!host) return done();
  var chip = document.getElementById('anvil-git-chip');
  if (!chip) {
    chip = document.createElement('div');
    chip.id = 'anvil-git-chip';
    chip.className = 'anvil-git';
    // Clicking a counter that reads "12 changed" should show you the twelve.
    // It used to re-fetch and repaint the same text, which is indistinguishable
    // from a dead control — the one thing a cursor:pointer promises it is not.
    // Refreshing is not lost: it happens on arrival, on a project change, and
    // whenever an edit card lands, which is every moment it could go stale.
    chip.addEventListener('click', function() {
      var open = window.UIClientActions && window.UIClientActions['anvil_history'];
      if (typeof open === 'function') { open({}); return; }
      window.anvilGitRefresh();
    });
    host.appendChild(chip);
  }
  var pid = window.anvilProject();
  if (!pid) { chip.style.display = 'none'; return done(); }
  chip.style.display = '';

  function line(cls, text, title) {
    chip.innerHTML = '';
    var s = document.createElement('span');
    s.className = cls;
    s.textContent = text;
    if (title) chip.title = title;
    chip.appendChild(s);
    return chip;
  }
  line('anvil-git-none', 'checking...');

  window.anvilFetchJSON('api/project/' + encodeURIComponent(pid) + '/status')
    .then(function(st) {
      var g = (st && st.git) || {};
      if (!st || st.ready === false) {
        line('anvil-git-none', st && st.detail ? 'not ready' : 'unavailable', st && st.detail ? st.detail : '');
        return;
      }
      if (!g.is_repo) {
        line('anvil-git-none', 'not a git repo', st.path || '');
        return;
      }
      chip.innerHTML = '';
      var b = document.createElement('span');
      b.className = 'anvil-git-branch';
      b.textContent = g.branch || ('@' + (g.head || '?'));
      b.title = g.branch ? g.branch : 'detached head';
      chip.appendChild(b);

      var state = document.createElement('span');
      if (g.dirty) {
        state.className = 'anvil-git-dirty';
        state.textContent = '● ' + g.changed + ' changed';
      } else {
        state.className = 'anvil-git-clean';
        state.textContent = '✓ clean';
      }
      chip.appendChild(state);

      if (g.prefix) {
        var sub = document.createElement('span');
        sub.textContent = '· ' + g.prefix + '/';
        sub.title = 'This project is a subdirectory of the repository; status is scoped to it.';
        chip.appendChild(sub);
      }
      chip.title = (g.root || '') + (g.prefix ? ' → ' + g.prefix + '/' : '') + '. Click to see what changed';
    })
    .catch(function(err) {
      line('anvil-git-none', 'git status unavailable', String(err.message || err));
    })
    .then(done);
}

// Mount once the panel exists, then follow the picker.
setTimeout(function() {
  window.anvilGitRefresh();
  var picker = window.anvilPickerEl();
  if (picker) picker.addEventListener('change', function() { window.anvilGitRefresh(); });
}, 150);
`

// anvilDeepenJS fetches the history a shallow checkout was made without,
// from the project's own settings page.
const anvilDeepenJS = `function(ctx) {
  var id = ctx.action && ctx.action.data;
  var btn = ctx.button;
  if (!id) { anvilAlert('No project on this control.'); return; }
  btn.disabled = true;
  var was = btn.textContent;
  btn.textContent = 'Fetching...';
  window.anvilFetchJSON('api/project/' + encodeURIComponent(id) + '/deepen', {method: 'POST'})
    .then(function(res) {
      btn.textContent = 'History fetched';
      if (res && res.detail) btn.title = res.detail;
    })
    .catch(function(err) {
      btn.disabled = false;
      btn.textContent = was;
      anvilAlert('Could not fetch the history: ' + (err.message || err));
    });
}`

// anvilDeleteProjectJS removes a project from its settings page and returns
// to the list, since the page it was configuring no longer exists.
const anvilDeleteProjectJS = `function(ctx) {
  var id = ctx.action && ctx.action.data;
  if (!id) { anvilAlert('No project on this control.'); return; }
  ctx.button.disabled = true;
  ctx.button.textContent = 'Removing...';
  window.anvilFetchJSON('api/project/' + encodeURIComponent(id), {method: 'DELETE'})
    .then(function() { window.location.href = './'; })
    .catch(function(err) {
      ctx.button.disabled = false;
      ctx.button.textContent = 'Remove project';
      anvilAlert('Could not remove it: ' + (err.message || err));
    });
}`

// anvilSettingsJS opens the settings for the project currently on screen.
//
// Reads the live picker rather than a baked id: the toolbar renders once and
// the picker changes beneath it, so an id captured at render time would send
// you to configure whichever project happened to be first when the page
// loaded.
const anvilSettingsJS = `function(ctx) {
  var pid = window.anvilProject();
  if (!pid) {
    anvilAlert('No project is selected. Add one first, or pick one from the sidebar.');
    return;
  }
  window.location.href = 'project?id=' + encodeURIComponent(pid);
}`

// anvilDownloadJS saves a file the open project's {path} endpoint answers. A
// fetch rather than a link, so "nothing to download" is a notice on the page
// instead of a page of JSON in place of the workspace. When the answer says
// files are left uncommitted, the person is told they are not in it.
func anvilDownloadJS(path, busy string) string {
	return "function(ctx) {\n" +
		"  var pid = window.anvilProject();\n" +
		"  if (!pid) { anvilAlert('No project is selected.'); return; }\n" +
		"  var btn = ctx.button, label = btn ? btn.textContent : '';\n" +
		"  if (btn) { btn.disabled = true; btn.textContent = " + jsQuote(busy) + "; }\n" +
		"  var restore = function() { if (btn) { btn.disabled = false; btn.textContent = label; } };\n" +
		"  fetch('api/project/' + encodeURIComponent(pid) + '/' + " + jsQuote(path) + ", {credentials: 'same-origin'})\n" +
		anvilDownloadBodyJS
}

// anvilDownloadBodyJS is the part of anvilDownloadJS that does not vary.
const anvilDownloadBodyJS = `    .then(function(r) {
      if (!r.ok) {
        return r.text().then(function(t) {
          var msg = t;
          try { msg = JSON.parse(t).error || t; } catch (e) {}
          throw new Error(msg);
        });
      }
      var name = 'download';
      var m = /filename="([^"]+)"/.exec(r.headers.get('Content-Disposition') || '');
      if (m) name = m[1];
      var left = parseInt(r.headers.get('X-Anvil-Uncommitted') || '0', 10) || 0;
      return r.blob().then(function(b) {
        var a = document.createElement('a');
        a.href = URL.createObjectURL(b);
        a.download = name;
        document.body.appendChild(a);
        a.click();
        setTimeout(function() { URL.revokeObjectURL(a.href); a.remove(); }, 1000);
        if (left > 0) anvilAlert('Downloaded. ' + left + (left === 1 ? ' file differs' : ' files differ') + ' from the last commit and ' + (left === 1 ? 'is' : 'are') + ' not in it: commit first, or take them with Download, Changes as a patch.');
      });
    })
    .then(restore, function(err) { restore(); anvilAlert(err.message || String(err)); });
}`

// anvilGitDialogJS opens the open project's Commit or Publish form (key in
// window.anvilGitForms, which gitFormsJS sets) in a dialog.
func anvilGitDialogJS(key, title string) string {
	return "function(ctx) {\n" +
		"  var forms = window.anvilGitForms || {};\n" +
		"  var form = forms[" + jsQuote(key) + "];\n" +
		"  if (!form) { anvilAlert('No project is open.'); return; }\n" +
		"  window.uiOpenComponentDialog({title: " + jsQuote(title) + ", subtitle: window.anvilProjectName(), width: '560px',\n" +
		"    component: JSON.parse(JSON.stringify(form))});\n" +
		"}"
}

// anvilGitDoneJS redraws the git line after a commit or a push.
const anvilGitDoneJS = `function() { if (typeof window.anvilGitRefresh === 'function') window.anvilGitRefresh(); }`

// anvilAppPreviewJS builds the open app project's working copy and opens its
// preview in a new tab: only the person who opened it sees it.
const anvilAppPreviewJS = `function(ctx) {
  var pid = window.anvilProject();
  if (!pid) { anvilAlert('No project is selected.'); return; }
  var btn = ctx.button, label = btn ? btn.textContent : '';
  var tab = window.open('', '_blank');
  if (btn) { btn.disabled = true; btn.textContent = 'Building the preview...'; }
  var restore = function() { if (btn) { btn.disabled = false; btn.textContent = label; } };
  window.anvilFetchJSON('api/project/' + encodeURIComponent(pid) + '/preview', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: '{}'})
    .then(function(d) {
      restore();
      if (tab) tab.location.href = d.url; else window.location.href = d.url;
    })
    .catch(function(err) {
      restore();
      if (tab) tab.close();
      anvilAlert('The preview could not be built: ' + (err.message || err));
    });
}`

// anvilAppInstallJS puts the open app project's changes live.
const anvilAppInstallJS = `function(ctx) {
  var pid = window.anvilProject();
  if (!pid) { anvilAlert('No project is selected.'); return; }
  var btn = ctx.button, label = btn ? btn.textContent : '';
  window.anvilConfirm('Install your changes? Everyone using the app gets them once its checks pass; a shared app may need an administrator to approve them first.').then(function(ok) {
    if (!ok) return;
    if (btn) { btn.disabled = true; btn.textContent = 'Installing...'; }
    window.anvilFetchJSON('api/project/' + encodeURIComponent(pid) + '/install', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: '{}'})
      .then(function(d) {
        if (btn) { btn.disabled = false; btn.textContent = label; }
        anvilAlert(d.message || 'Installed.');
      })
      .catch(function(err) {
        if (btn) { btn.disabled = false; btn.textContent = label; }
        anvilAlert('Not installed: ' + (err.message || err));
      });
  });
}`
