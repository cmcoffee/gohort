
  // ---- Co-author: select a passage, ask about it, review what comes back ----
  //
  // A document a person writes with an agent beside it. What they highlight in
  // it, the way they would to copy or cut (a drag, a double-click, the handles
  // on a phone), is what the conversation is about: the chat says so over its
  // box ("Highlighted: ...") with the page's quick actions beside it, and the
  // send carries the passage. What the agent proposes comes back as a text_proposal
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

  // coauthorReviewParts splits a proposal into what its review shows: the
  // document before the lines it touches, those lines as they are and as they
  // would be, and the document after. Whole lines, so the change is drawn in
  // place among its neighbours. A whole-document proposal is all middle.
  function coauthorReviewParts(doc, find, at, replace) {
    doc = String(doc == null ? '' : doc);
    if (!find) return {before: '', oldMid: doc, newMid: String(replace || ''), after: ''};
    var start = doc.lastIndexOf('\n', at - 1) + 1;
    var end = at + find.length;
    if (!(end > start && doc.charAt(end - 1) === '\n')) {
      var nl = doc.indexOf('\n', end);
      end = nl < 0 ? doc.length : nl + 1;
    }
    return {
      before: doc.slice(0, start),
      oldMid: doc.slice(start, end),
      newMid: doc.slice(start, at) + String(replace || '') + doc.slice(at + find.length, end),
      after: doc.slice(end),
    };
  }

  // coauthorReviewHTML draws those parts as one line diff of the whole
  // document: the lines around the change as they are, the change marked
  // where it goes. Only the touched lines are diffed, so a long file costs
  // no more than the passage.
  function coauthorReviewHTML(parts) {
    function lines(t) {
      t = String(t || '');
      return t ? t.replace(/\n$/, '').split('\n') : [];
    }
    function row(type, text) {
      var mark = type === 'add' ? '+' : type === 'remove' ? '-' : ' ';
      return '<div class="editor-diff-' + type + '"><span class="editor-diff-marker">' + mark +
        '</span><span class="editor-diff-text">' + window.editorDiffEscape(text) + '</span></div>';
    }
    var html = '<div class="editor-diff">';
    lines(parts.before).forEach(function(l) { html += row('equal', l); });
    window.editorDiffLines(parts.oldMid.replace(/\n$/, ''), parts.newMid.replace(/\n$/, '')).forEach(function(op) {
      html += row(op.type, op.text);
    });
    lines(parts.after).forEach(function(l) { html += row('equal', l); });
    return html + '</div>';
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

  // coauthorPinning is the half of a co-authored document the chat sees: the
  // passage pinned as what the next send is about, its chip over the chat's
  // box with the page's quick actions, and the send carrying it through the
  // host's send context. Shared by uiCoauthor (a textarea the person writes
  // in) and uiHighlightContext (a rendered document they read). pinned is
  // {text} and, in a textarea, {start, end}.
  function coauthorPinning(host, opts) {
    var p = {pinned: null};
    p.composer = function() {
      var c = host.querySelector && host.querySelector('[data-ui-composer]');
      if (!c) {
        var all = document.querySelectorAll('[data-ui-composer]');
        if (all.length === 1) c = all[0];
      }
      return (c && c.uiComposer) || null;
    };
    // A quick action sends its words about the highlighted passage at once.
    function act(a) {
      var c = p.composer();
      if (!c || !p.pinned) return;
      c.insertText(a.prompt || a.label);
      c.send();
    }
    p.pin = function(sel) {
      p.pinned = {start: sel.start, end: sel.end, text: sel.text};
      var c = p.composer();
      if (!c) return;
      var label = typeof opts.label === 'function' ? opts.label(p.pinned) : coauthorLabel(p.pinned.text);
      var actions = (opts.actions || []).filter(function(a) { return a && a.label; }).map(function(a) {
        return {label: String(a.label), onClick: function() { act(a); }};
      });
      c.setChip('Highlighted: ' + label, function() { p.pinned = null; }, {actions: actions});
    };
    p.unpin = function() {
      p.pinned = null;
      var c = p.composer();
      if (c) c.clearChip();
    };
    // The send carries the pinned passage beside whatever the host already
    // sends (an editor's file, a form's fields).
    var prior = host.uiSendContext;
    var hadSendContext = host.hasAttribute ? host.hasAttribute('data-ui-send-context') : true;
    host.setAttribute('data-ui-send-context', '');
    function sendContext() {
      var base = typeof prior === 'function' ? (prior() || {}) : {};
      if (!p.pinned) return base;
      var sel = {text: p.pinned.text};
      if (p.pinned.start != null) { sel.start = p.pinned.start; sel.end = p.pinned.end; }
      return Object.assign({}, base, {selection: sel});
    }
    host.uiSendContext = sendContext;
    // release lets the pin go and hands the host its send context back.
    p.release = function() {
      if (p.pinned) p.unpin();
      if (host.uiSendContext === sendContext) {
        host.uiSendContext = prior;
        if (!hadSendContext && host.removeAttribute) host.removeAttribute('data-ui-send-context');
      }
    };
    return p;
  }

  // uiCoauthor(textarea, opts) makes textarea a co-authored document.
  //   host     the element holding the document and its chat (default: the
  //            nearest ancestor containing a chat panel)
  //   actions  [{label, prompt}]: quick actions shown in the chat while a
  //            passage is highlighted. One sends its prompt about it at once.
  //   label    function(sel) naming a pinned passage for its chip
  //   setValue function(text) writing the document, for an editor that keeps
  //            more than the textarea in step; default writes the textarea
  //            and fires input, so a page's unsaved-changes tracking sees it
  //   showDocument  for a layout that shows the document and its chat one at
  //            a time (a phone): a proposal brings the document up to be
  //            reviewed
  // Returns {pin, unpin, pinned, propose, destroy}.
  window.uiCoauthor = function(textarea, opts) {
    opts = opts || {};
    var host = opts.host || coauthorHost(textarea);
    var review = null;   // the open review panel
    var applying = false;
    var undoState = null;

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

    // --- What is highlighted is what the conversation is about -----------------
    // A selection the person makes in the document pins it: the chat shows it
    // over its box, with the page's quick actions, and the next send carries
    // it. Clicking back into the document with nothing selected lets it go,
    // and the conversation is about the whole document again. Moving to the
    // chat to type keeps it: only the document's own selection counts. No bar
    // beside the selection: a phone puts its own Cut / Copy / Paste there, and
    // the place to say something about a passage is the chat.
    //
    // A selection the page makes itself (a review showing its passage) is not
    // the person's and pins nothing, hence quietUntil.
    var quietUntil = 0;
    function followSelection() {
      if (Date.now() < quietUntil) return;
      var sel = selection();
      var pinned = pins.pinned;
      if (sel) {
        if (!pinned || pinned.start !== sel.start || pinned.end !== sel.end) pins.pin(sel);
      } else if (pinned) {
        pins.unpin();
      }
    }
    function soon() { setTimeout(followSelection, 0); }
    textarea.addEventListener('mouseup', soon);
    textarea.addEventListener('touchend', soon);
    textarea.addEventListener('keyup', soon);
    // A phone's selection handles move the selection with no touch on the
    // textarea itself.
    function onSelectionChange() { if (document.activeElement === textarea) soon(); }
    if (document.addEventListener) document.addEventListener('selectionchange', onSelectionChange);

    // --- Pinning (coauthorPinning) -------------------------------------------
    var pins = coauthorPinning(host, opts);
    function unpin() { pins.unpin(); }

    // An edit moves the pinned passage, or removes it: follow it while it is
    // still there, let it go when it is not. Any edit also ends an Undo.
    textarea.addEventListener('input', function() {
      if (applying) return;
      clearUndo();
      var pinned = pins.pinned;
      if (!pinned) return;
      var doc = textarea.value;
      if (doc.slice(pinned.start, pinned.end) === pinned.text) return;
      var at = coauthorLocate(doc, pinned.text, pinned.start);
      if (at < 0) { unpin(); return; }
      pinned.start = at;
      pinned.end = at + pinned.text.length;
    });

    // --- Review ----------------------------------------------------------------
    // The review takes the document's place while it is open: the textarea
    // hides, and the review stands where it was, at its height, so the change
    // is seen where it goes. Closing it brings the textarea back.
    var hiddenDisplay = null;
    function closeReview() {
      if (review && review.parentNode) review.parentNode.removeChild(review);
      review = null;
      if (hiddenDisplay !== null) {
        textarea.style.display = hiddenDisplay;
        hiddenDisplay = null;
      }
    }
    function clearUndo() {
      if (undoState && undoState.bar && undoState.bar.parentNode) undoState.bar.parentNode.removeChild(undoState.bar);
      undoState = null;
    }
    function place(node) {
      textarea.parentNode.insertBefore(node, textarea.nextSibling);
    }
    function showPassage(at, len) {
      quietUntil = Date.now() + 400;
      try {
        textarea.focus();
        textarea.setSelectionRange(at, at + len);
      } catch (e) { /* a hidden textarea cannot take a selection */ }
    }
    function openReview(p, at) {
      closeReview();
      clearUndo();
      var find = p.find;
      var parts = coauthorReviewParts(textarea.value, find, at, p.replace);
      var drawn = typeof window.editorDiffLines === 'function' && typeof window.editorDiffEscape === 'function';
      var stats = typeof window.editorDiffStats === 'function'
        ? window.editorDiffStats(parts.oldMid.replace(/\n$/, ''), parts.newMid.replace(/\n$/, '')) : null;
      var note = el('div', {class: 'ui-coauthor-review-note', style: 'display:none'});
      var applyBtn = el('button', {type: 'button', class: 'ui-row-btn ui-coauthor-apply',
        onclick: function() { apply(p, at, note, applyBtn); }}, ['Apply']);
      review = el('div', {class: 'ui-coauthor-review ui-coauthor-review-inplace'}, [
        el('div', {class: 'ui-coauthor-review-h'}, [
          el('span', {class: 'ui-coauthor-review-title'}, [p.summary || (find ? 'Proposed change to the selected passage' : 'Proposed change')]),
          stats ? el('span', {class: 'ui-coauthor-review-stats'}, ['+' + stats.add + ' −' + stats.remove]) : null,
          applyBtn,
          el('button', {type: 'button', class: 'ui-row-btn', onclick: closeReview}, ['Reject']),
        ]),
        note,
        drawn
          ? el('div', {class: 'ui-coauthor-review-body', html: coauthorReviewHTML(parts)})
          : el('pre', {class: 'ui-coauthor-review-body'}, [p.replace]),
      ]);
      var height = textarea.offsetHeight || 0;
      textarea.parentNode.insertBefore(review, textarea);
      if (height) review.style.height = height + 'px';
      hiddenDisplay = textarea.style.display || '';
      textarea.style.display = 'none';
      // Scrolled to the change, in its surroundings.
      var change = review.querySelector && review.querySelector('.editor-diff-remove, .editor-diff-add');
      if (change && change.scrollIntoView) change.scrollIntoView({block: 'center'});
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
      var at = coauthorLocate(textarea.value, p.find, pins.pinned ? pins.pinned.start : 0);
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
      pin: pins.pin, unpin: unpin, propose: propose,
      pinned: function() { return pins.pinned; },
      // destroy takes its page listener away and hands the host
      // back as it was, for a document that goes before the page does (one
      // in a dialog, an editor that closes and opens again).
      destroy: function() {
        closeReview();
        clearUndo();
        pins.release();
        if (document.removeEventListener) document.removeEventListener('selectionchange', onSelectionChange);
        if (host.uiProposeText === propose) {
          host.uiProposeText = undefined;
          if (host.removeAttribute) host.removeAttribute('data-ui-coauthor');
        }
      },
    };
  };

  // uiHighlightContext(root, opts): what the person highlights inside root, a
  // document they read rather than type in (rendered HTML, a page of text),
  // is what the chat beside it is about. The same chip and quick actions as a
  // co-authored document, and the send carries {selection: {text}}. No
  // proposals: the page's agent changes the document its own way, knowing now
  // which passage the person means. A selection inside a textarea in root (an
  // editor opened over the document) counts as well.
  //   host     the element holding root and the chat (default: as uiCoauthor)
  //   actions  [{label, prompt}] shown in the chat while something is
  //            highlighted
  // Returns {pinned, unpin, destroy}.
  window.uiHighlightContext = function(root, opts) {
    opts = opts || {};
    var host = opts.host || coauthorHost(root);
    var pins = coauthorPinning(host, opts);
    function highlighted() {
      var a = document.activeElement;
      if (a && a.tagName === 'TEXTAREA' && root.contains && root.contains(a)) {
        var s = a.selectionStart, e = a.selectionEnd;
        return (s != null && e > s) ? a.value.slice(s, e) : '';
      }
      var sel = window.getSelection && window.getSelection();
      if (!sel || sel.isCollapsed || !sel.rangeCount) return '';
      var r = sel.getRangeAt(0);
      if (!root.contains || !root.contains(r.commonAncestorContainer)) return null; // elsewhere: not ours
      return String(sel.toString() || '');
    }
    function follow() {
      var text = highlighted();
      if (text === null) return;
      if (text.trim()) {
        if (!pins.pinned || pins.pinned.text !== text) pins.pin({text: text});
      } else if (pins.pinned) {
        pins.unpin();
      }
    }
    function soon() { setTimeout(follow, 0); }
    root.addEventListener('mouseup', soon);
    root.addEventListener('touchend', soon);
    root.addEventListener('keyup', soon);
    function onSelectionChange() {
      var a = document.activeElement;
      if (a && root.contains && root.contains(a)) { soon(); return; }
      var sel = window.getSelection && window.getSelection();
      if (sel && sel.rangeCount && root.contains && root.contains(sel.getRangeAt(0).commonAncestorContainer)) soon();
    }
    if (document.addEventListener) document.addEventListener('selectionchange', onSelectionChange);
    return {
      pinned: function() { return pins.pinned; },
      unpin: function() { pins.unpin(); },
      destroy: function() {
        pins.release();
        root.removeEventListener('mouseup', soon);
        root.removeEventListener('touchend', soon);
        root.removeEventListener('keyup', soon);
        if (document.removeEventListener) document.removeEventListener('selectionchange', onSelectionChange);
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
