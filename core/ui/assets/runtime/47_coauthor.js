
  // ---- Co-author: select a passage, ask about it, review what comes back ----
  //
  // A document a person writes with an agent beside it. They select part of it
  // the way they would to copy or cut (a drag, a double-click, the handles on a
  // phone) and a small bar offers Ask and the page's quick actions. Asking pins
  // the passage: a chip over the chat says what the next send is about, and the
  // send carries it. What the agent proposes comes back as a text_proposal
  // block: the passage it replaces and the replacement, reviewed as a diff of
  // that passage and applied, or not, by the person. Nothing changes until
  // they apply it, and their own Save keeps it.
  //
  // Generic: nothing here knows what the document is. A page names its quick
  // actions; what a passage means (a verse, a function, a clause) is the
  // agent's business. See docs/design/coauthor-proposals.md.

  // coauthorLocate is where find sits in doc: the occurrence nearest near (the
  // pinned passage's start, so a line that repeats resolves to the one that was
  // selected), or -1 when it is not there at all. An empty find is the whole
  // document, at 0.
  function coauthorLocate(doc, find, near) {
    doc = String(doc == null ? '' : doc);
    if (!find) return 0;
    near = near || 0;
    var best = -1, bestDist = Infinity;
    for (var i = doc.indexOf(find); i >= 0; i = doc.indexOf(find, i + 1)) {
      var d = Math.abs(i - near);
      if (d < bestDist) { best = i; bestDist = d; }
    }
    return best;
  }

  // coauthorReplace is doc with find (at at) replaced by replace, or replace
  // alone for a whole-document proposal.
  function coauthorReplace(doc, find, at, replace) {
    if (!find) return replace;
    return doc.slice(0, at) + replace + doc.slice(at + find.length);
  }

  // coauthorLabel names a pinned passage for its chip: its opening words and,
  // past one line, how many lines it spans.
  function coauthorLabel(text) {
    text = String(text || '');
    var lines = text.replace(/\n+$/, '').split('\n').length;
    var words = text.trim().split(/\s+/).slice(0, 6).join(' ');
    if (words.length > 40) words = words.slice(0, 40).trim();
    var opening = '“' + words + (text.trim().length > words.length ? '…' : '') + '”';
    return lines > 1 ? opening + ', ' + lines + ' lines' : opening;
  }

  // coauthorHost is the element holding both the document and the chat beside
  // it: the nearest ancestor of the document that contains a chat composer.
  // The chat reads its send context from an ancestor of its own, so the pinned
  // passage has to be stored on one they share.
  function coauthorHost(textarea) {
    for (var n = textarea.parentNode; n && n.querySelector; n = n.parentNode) {
      if (n.querySelector('[data-ui-composer]')) return n;
    }
    return textarea.parentNode;
  }

  // uiCoauthor(textarea, opts) makes textarea a co-authored document.
  //   host     the element holding the document and its chat (default: the
  //            nearest ancestor containing a chat panel)
  //   actions  [{label, prompt}]: quick actions on the selection bar. One
  //            pins the selection and sends its prompt at once.
  //   label    function(sel) naming a pinned passage for its chip
  //   setValue function(text) writing the document, for an editor that keeps
  //            more than the textarea in step; default writes the textarea
  //            and fires input, so a page's unsaved-changes tracking sees it
  //   showChat, showDocument  for a layout that shows the document and its
  //            chat one at a time (a phone): Ask brings the chat up to type
  //            in, and a proposal brings the document up to be reviewed
  // Returns {pin, unpin, pinned, propose}.
  window.uiCoauthor = function(textarea, opts) {
    opts = opts || {};
    var host = opts.host || coauthorHost(textarea);
    var pinned = null;   // {start, end, text}: what the next send is about
    var review = null;   // the open review panel
    var applying = false;
    var undoState = null;

    function composer() {
      var c = host.querySelector && host.querySelector('[data-ui-composer]');
      if (!c) {
        var all = document.querySelectorAll('[data-ui-composer]');
        if (all.length === 1) c = all[0];
      }
      return (c && c.uiComposer) || null;
    }
    function selection() {
      var s = textarea.selectionStart, e = textarea.selectionEnd;
      if (s == null || e == null || e <= s) return null;
      var text = textarea.value.slice(s, e);
      return text.trim() ? {start: s, end: e, text: text} : null;
    }
    function setValue(text) {
      applying = true;
      try {
        if (typeof opts.setValue === 'function') opts.setValue(text);
        else {
          textarea.value = text;
          textarea.dispatchEvent(new Event('input', {bubbles: true}));
        }
      } finally { applying = false; }
    }

    // --- The bar a selection shows -------------------------------------------
    var bar = el('div', {class: 'ui-coauthor-bar', role: 'toolbar', style: 'display:none'});
    // Pressing a button must not take the selection away before it is read.
    bar.addEventListener('mousedown', function(e) { e.preventDefault(); });
    document.body.appendChild(bar);
    function hideBar() { bar.style.display = 'none'; }
    function showBar(x, y) {
      var sel = selection();
      if (!sel) { hideBar(); return; }
      bar.innerHTML = '';
      bar.appendChild(el('button', {type: 'button', class: 'ui-row-btn ui-coauthor-ask',
        onclick: function() { ask(sel); }}, ['Ask']));
      (opts.actions || []).forEach(function(a) {
        if (!a || !a.label) return;
        bar.appendChild(el('button', {type: 'button', class: 'ui-row-btn',
          onclick: function() { act(sel, a); }}, [String(a.label)]));
      });
      bar.style.display = '';
      var w = bar.offsetWidth || 160, h = bar.offsetHeight || 36;
      var vw = window.innerWidth || 800;
      var left = Math.max(8, Math.min(x - w / 2, vw - w - 8));
      var top = y - h - 10;
      if (top < 8) top = y + 18; // no room above: below the pointer
      bar.style.left = left + 'px';
      bar.style.top = top + 'px';
    }
    function barAtPointer(e) {
      setTimeout(function() {
        var pt = e && e.changedTouches && e.changedTouches[0] ? e.changedTouches[0] : e;
        if (pt && pt.clientX != null) { showBar(pt.clientX, pt.clientY); return; }
        var r = textarea.getBoundingClientRect();
        showBar(r.left + r.width / 2, r.top + 8);
      }, 0);
    }
    textarea.addEventListener('mouseup', barAtPointer);
    textarea.addEventListener('touchend', barAtPointer);
    textarea.addEventListener('keyup', function(e) {
      if (e.shiftKey || e.key === 'Shift') barAtPointer(null);
      else if (!selection()) hideBar();
    });
    textarea.addEventListener('blur', function() { setTimeout(hideBar, 150); });
    window.addEventListener('scroll', hideBar, true);

    // --- Pinning ---------------------------------------------------------------
    function pin(sel) {
      pinned = {start: sel.start, end: sel.end, text: sel.text};
      var c = composer();
      var label = typeof opts.label === 'function' ? opts.label(pinned) : coauthorLabel(pinned.text);
      if (c) c.setChip(label, function() { pinned = null; });
    }
    function unpin() {
      pinned = null;
      var c = composer();
      if (c) c.clearChip();
    }
    function ask(sel) {
      pin(sel);
      hideBar();
      if (typeof opts.showChat === 'function') opts.showChat();
      var c = composer();
      if (c) c.focus();
    }
    function act(sel, a) {
      pin(sel);
      hideBar();
      var c = composer();
      if (!c) return;
      c.insertText(a.prompt || a.label);
      c.send();
    }

    // An edit moves the pinned passage, or removes it: follow it while it is
    // still there, let it go when it is not. Any edit also ends an Undo.
    textarea.addEventListener('input', function() {
      hideBar();
      if (applying) return;
      clearUndo();
      if (!pinned) return;
      var doc = textarea.value;
      if (doc.slice(pinned.start, pinned.end) === pinned.text) return;
      var at = coauthorLocate(doc, pinned.text, pinned.start);
      if (at < 0) { unpin(); return; }
      pinned.start = at;
      pinned.end = at + pinned.text.length;
    });

    // The send carries the pinned passage beside whatever the host already
    // sends (an editor's file, a form's fields).
    var prior = host.uiSendContext;
    host.setAttribute('data-ui-send-context', '');
    host.uiSendContext = function() {
      var base = typeof prior === 'function' ? (prior() || {}) : {};
      if (!pinned) return base;
      return Object.assign({}, base, {selection: {text: pinned.text, start: pinned.start, end: pinned.end}});
    };

    // --- Review ----------------------------------------------------------------
    function closeReview() {
      if (review && review.parentNode) review.parentNode.removeChild(review);
      review = null;
    }
    function clearUndo() {
      if (undoState && undoState.bar && undoState.bar.parentNode) undoState.bar.parentNode.removeChild(undoState.bar);
      undoState = null;
    }
    function place(node) {
      textarea.parentNode.insertBefore(node, textarea.nextSibling);
    }
    function showPassage(at, len) {
      try {
        textarea.focus();
        textarea.setSelectionRange(at, at + len);
      } catch (e) { /* a hidden textarea cannot take a selection */ }
    }
    function openReview(p, at) {
      closeReview();
      clearUndo();
      var find = p.find, repl = p.replace;
      var oldText = find || textarea.value;
      var stats = typeof window.editorDiffStats === 'function' ? window.editorDiffStats(oldText, repl) : null;
      var note = el('div', {class: 'ui-coauthor-review-note', style: 'display:none'});
      var applyBtn = el('button', {type: 'button', class: 'ui-row-btn ui-coauthor-apply',
        onclick: function() { apply(p, at, note, applyBtn); }}, ['Apply']);
      review = el('div', {class: 'ui-coauthor-review'}, [
        el('div', {class: 'ui-coauthor-review-h'}, [
          el('span', {class: 'ui-coauthor-review-title'}, [p.summary || (find ? 'Proposed change to the selected passage' : 'Proposed change')]),
          stats ? el('span', {class: 'ui-coauthor-review-stats'}, ['+' + stats.add + ' −' + stats.remove]) : null,
          applyBtn,
          el('button', {type: 'button', class: 'ui-row-btn', onclick: closeReview}, ['Reject']),
        ]),
        note,
        typeof window.editorDiffRender === 'function'
          ? el('div', {class: 'ui-coauthor-review-body', html: window.editorDiffRender(oldText, repl)})
          : el('pre', {class: 'ui-coauthor-review-body'}, [repl]),
      ]);
      place(review);
      if (find) showPassage(at, find.length);
    }
    // Applied against the document as it is NOW: the passage is found again,
    // and if it is gone (edited since the proposal came) nothing is written.
    function apply(p, near, note, applyBtn) {
      var doc = textarea.value;
      var at = coauthorLocate(doc, p.find, near);
      if (p.find && at < 0) {
        note.textContent = 'That passage has changed since this was proposed, so it was not applied. Ask again.';
        note.style.display = '';
        applyBtn.disabled = true;
        return;
      }
      var next = coauthorReplace(doc, p.find, at, p.replace);
      setValue(next);
      closeReview();
      unpin();
      if (p.find) showPassage(at, p.replace.length);
      undoState = {before: doc, after: next};
      undoState.bar = el('div', {class: 'ui-coauthor-undo'}, [
        el('span', {}, ['Applied.']),
        el('button', {type: 'button', class: 'ui-row-btn', onclick: function() {
          var u = undoState;
          clearUndo();
          if (u && textarea.value === u.after) setValue(u.before);
        }}, ['Undo']),
      ]);
      place(undoState.bar);
    }

    // propose opens a proposal ({find, replace, summary}) for review. A find
    // that is not in the document is refused: {ok: false, reason}.
    function propose(p) {
      p = p || {};
      p = {find: p.find == null ? '' : String(p.find), replace: p.replace == null ? '' : String(p.replace),
        summary: p.summary ? String(p.summary) : ''};
      var at = coauthorLocate(textarea.value, p.find, pinned ? pinned.start : 0);
      if (p.find && at < 0) {
        return {ok: false, reason: 'That passage is no longer in the document: it changed since. Ask again.'};
      }
      if (typeof opts.showDocument === 'function') opts.showDocument();
      openReview(p, at);
      return {ok: true};
    }
    host.setAttribute('data-ui-coauthor', '');
    host.uiProposeText = propose;

    return {
      pin: pin, unpin: unpin, propose: propose,
      pinned: function() { return pinned; },
      // destroy takes the bar and its page listener away, for a document
      // that goes before the page does (one in a dialog).
      destroy: function() {
        hideBar();
        if (bar.parentNode) bar.parentNode.removeChild(bar);
        if (window.removeEventListener) window.removeEventListener('scroll', hideBar, true);
      },
    };
  };

  // text_proposal is an agent's proposed change to the co-authored document
  // its chat sits beside (agenthost.ProposeEditTool): data.find the passage it
  // replaces (empty: the whole document), data.replace what goes there, Text
  // a one-line summary. Live, the review opens at once; after a reload the
  // card stays, and Review opens it again against the document as it is then.
  window.uiRegisterBlockRenderer('text_proposal', function(d, ctx) {
    var data = (d && d.data) || {};
    var find = data.find || '', repl = data.replace || '';
    var card = el('div', {class: 'ui-coauthor-proposal'});
    var note = el('div', {class: 'ui-coauthor-proposal-note', style: 'display:none'});
    function say(text) { note.textContent = text; note.style.display = ''; }
    function target() {
      var h = card.closest && card.closest('[data-ui-coauthor]');
      if (!h) {
        var all = document.querySelectorAll('[data-ui-coauthor]');
        if (all.length === 1) h = all[0];
      }
      return h;
    }
    function review() {
      var h = target();
      if (!h || typeof h.uiProposeText !== 'function') { say('Open the document this is for to review it.'); return; }
      var r = h.uiProposeText({find: find, replace: repl, summary: d.text || ''});
      if (r && r.ok === false) say(r.reason);
    }
    var lines = find ? find.replace(/\n+$/, '').split('\n').length : 0;
    card.appendChild(el('div', {class: 'ui-coauthor-proposal-h'}, [d.text || 'Proposed change']));
    card.appendChild(el('div', {class: 'ui-coauthor-proposal-meta'},
      [find ? 'Replaces ' + lines + (lines === 1 ? ' line' : ' lines') : 'The whole document']));
    card.appendChild(el('button', {class: 'ui-row-btn', type: 'button', onclick: review}, ['Review']));
    card.appendChild(note);
    if (ctx && ctx.live) setTimeout(review, 0);
    return {wrap: card};
  });
