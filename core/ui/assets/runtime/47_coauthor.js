
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

  // The document half (what is highlighted, a review in place, Apply, Undo)
  // and the pure pieces (coauthorLocate, coauthorReviewParts, coauthorLabel)
  // are core/editor's coauthor.js, loaded ahead of the runtime and shared with
  // app pages. What is here is the chat half: the chip, its quick actions, and
  // the send carrying the passage.

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
    // The pinned passage lives with the chat (its chip and send context);
    // the document follows and reviews against it.
    var pins = coauthorPinning(host, opts);
    var doc = coauthorTextDoc(textarea, {
      el: el, setValue: opts.setValue, showDocument: opts.showDocument,
      pinned: function() { return pins.pinned; }, pin: pins.pin, unpin: pins.unpin,
    });
    host.setAttribute('data-ui-coauthor', '');
    host.uiProposeText = doc.propose;

    return {
      pin: pins.pin, unpin: pins.unpin, propose: doc.propose,
      pinned: function() { return pins.pinned; },
      // destroy takes its page listener away and hands the host
      // back as it was, for a document that goes before the page does (one
      // in a dialog, an editor that closes and opens again).
      destroy: function() {
        doc.destroy();
        pins.release();
        if (host.uiProposeText === doc.propose) {
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
