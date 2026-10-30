// coauthor.js is a document a person writes with an agent beside it, the
// half that lives in the document: what they highlight in a textarea, a
// proposed change to a passage reviewed in the document's place as a diff,
// and applied, or not, by them, with an Undo.
//
// Global, like diff.js, and loaded after it: the core/ui runtime builds its
// co-authored editor (uiCoauthor) on it, and an app page's frame, where the
// runtime is not, builds app.coauthor on it. One copy, so a fix reaches both.
// Nothing here knows what the document is.

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

// coauthorLabel names a pinned passage: its opening words and, past one
// line, how many lines it spans.
function coauthorLabel(text) {
  text = String(text || '');
  var lines = text.replace(/\n+$/, '').split('\n').length;
  var words = text.trim().split(/\s+/).slice(0, 6).join(' ');
  if (words.length > 40) words = words.slice(0, 40).trim();
  var opening = '“' + words + (text.trim().length > words.length ? '…' : '') + '”';
  return lines > 1 ? opening + ', ' + lines + ' lines' : opening;
}

// coauthorEl builds an element: opts class, html, style, on<event> handlers
// and attributes; kids strings or nodes. The runtime hands coauthorTextDoc
// its own builder; a page without the runtime gets this one.
function coauthorEl(tag, opts, kids) {
  var n = document.createElement(tag);
  opts = opts || {};
  for (var k in opts) {
    var v = opts[k];
    if (v == null) continue;
    if (k === 'class') n.className = v;
    else if (k === 'html') n.innerHTML = v;
    else if (k === 'style') n.style.cssText = v;
    else if (k.indexOf('on') === 0 && typeof v === 'function') n.addEventListener(k.slice(2), v);
    else n.setAttribute(k, v);
  }
  (kids || []).forEach(function(c) {
    if (c == null) return;
    n.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
  });
  return n;
}

// coauthorTextDoc(textarea, opts) makes textarea a co-authored document.
//   onSelect  function(sel or null): the highlighted passage changed. sel is
//             {start, end, text}. A selection the person makes in the
//             document pins it; clicking back in with nothing selected, an
//             edit that removes it, or an applied change lets it go. Moving
//             to a chat box to type keeps it: only the document's own
//             selection counts.
//   pinned, pin, unpin  where the pinned passage is kept, for a caller that
//             keeps it itself (the runtime's chat chip); default: here, with
//             onSelect told
//   setValue  function(text) writing the document; default writes the
//             textarea and fires input, so a page's own save sees it
//   showDocument  called before a review opens, for a layout that shows the
//             document and its chat one at a time
//   el        the element builder (default coauthorEl)
// Returns {selection, unpin, propose, closeReview, destroy}. propose opens a
// proposal ({find, replace, summary}; find '' is the whole document) for
// review in the document's place, or refuses one whose passage is gone:
// {ok: false, reason}.
function coauthorTextDoc(textarea, opts) {
  opts = opts || {};
  var el = opts.el || coauthorEl;
  var own = null;
  var pinned = opts.pinned || function() { return own; };
  var pin = opts.pin || function(sel) { own = sel; if (opts.onSelect) opts.onSelect(sel); };
  var unpin = opts.unpin || function() { own = null; if (opts.onSelect) opts.onSelect(null); };
  var review = null;   // the open review panel
  var applying = false;
  var undoState = null;
  var hiddenDisplay = null;

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

  // A selection the page makes itself (a review showing its passage) is not
  // the person's and pins nothing, hence quietUntil.
  var quietUntil = 0;
  function followSelection() {
    if (Date.now() < quietUntil) return;
    var sel = selection();
    var p = pinned();
    if (sel) {
      if (!p || p.start !== sel.start || p.end !== sel.end) pin(sel);
    } else if (p) {
      unpin();
    }
  }
  function soon() { setTimeout(followSelection, 0); }
  // A phone's selection handles move the selection with no touch on the
  // textarea itself.
  function onSelectionChange() { if (document.activeElement === textarea) soon(); }
  // An edit moves the pinned passage, or removes it: follow it while it is
  // still there, let it go when it is not. Any edit also ends an Undo.
  function onInput() {
    if (applying) return;
    clearUndo();
    var p = pinned();
    if (!p) return;
    var doc = textarea.value;
    if (doc.slice(p.start, p.end) === p.text) return;
    var at = coauthorLocate(doc, p.text, p.start);
    if (at < 0) { unpin(); return; }
    p.start = at;
    p.end = at + p.text.length;
  }
  textarea.addEventListener('mouseup', soon);
  textarea.addEventListener('touchend', soon);
  textarea.addEventListener('keyup', soon);
  textarea.addEventListener('input', onInput);
  if (document.addEventListener) document.addEventListener('selectionchange', onSelectionChange);

  // The review takes the document's place while it is open: the textarea
  // hides, and the review stands where it was, at its height, so the change
  // is seen where it goes. Closing it brings the textarea back.
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
  function propose(p) {
    p = p || {};
    p = {find: p.find == null ? '' : String(p.find), replace: p.replace == null ? '' : String(p.replace),
      summary: p.summary ? String(p.summary) : ''};
    var cur = pinned();
    var at = coauthorLocate(textarea.value, p.find, cur ? cur.start : 0);
    if (p.find && at < 0) {
      return {ok: false, reason: 'That passage is no longer in the document: it changed since. Ask again.'};
    }
    if (typeof opts.showDocument === 'function') opts.showDocument();
    openReview(p, at);
    return {ok: true};
  }

  return {
    selection: function() { return pinned(); },
    unpin: function() { if (pinned()) unpin(); },
    propose: propose,
    closeReview: closeReview,
    destroy: function() {
      closeReview();
      clearUndo();
      textarea.removeEventListener('mouseup', soon);
      textarea.removeEventListener('touchend', soon);
      textarea.removeEventListener('keyup', soon);
      textarea.removeEventListener('input', onInput);
      if (document.removeEventListener) document.removeEventListener('selectionchange', onSelectionChange);
    },
  };
}
