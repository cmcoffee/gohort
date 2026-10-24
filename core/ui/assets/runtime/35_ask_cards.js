  // Ask cards — the framework's clarifying-question control-tool blocks.
  //
  // `ui_ask` ({question, options, multi}) and `ui_ask_form` ({steps}) are the
  // wire contract the agent runner emits when an agent calls ask_user /
  // ask_user_form. Both shapes are generic and know no app.
  //
  // REGISTERED GLOBALLY, and that is the whole point of this file. These used to
  // be defined into pipeline_panel's LOCAL renderer map, which meant they
  // existed only where a PipelinePanel happened to be mounted. AgentLoopPanel
  // reads window.UIBlockRenderers and found nothing, so on every agent-loop
  // surface an ask arrived, matched no renderer, and fell through to a status
  // row rendering `d.text || d.title` — neither of which these blocks carry. The
  // user saw an empty bracketed type name and the turn sat parked on a question
  // nobody could answer. Reported twice from a writer app before it was found.
  //
  // Registered at IIFE time, guarded so a renderer already present wins. Apps
  // register their own richer variants from deferred / DOMContentLoaded
  // handlers, which run after this, so an app override still takes precedence.
  // PipelinePanel is unaffected: its local map is seeded from
  // window.UIBlockRenderers at mount time, which is later still.

    // uiAskPanelRoot finds the panel that owns this card: the nearest ancestor
  // that contains a composer. Walking UP from the card lands on the innermost
  // such container, which is the card's own panel — a sibling panel's composer
  // is only reachable from a higher common ancestor, so it cannot be picked.
  //
  // The old lookup was document-wide, so on a page with two chat panels the
  // answer went to whichever appeared first in the DOM, regardless of which
  // panel had asked. Falls back to the document, i.e. the old behavior, when
  // the card is not attached yet or its panel has no composer.
  function uiAskPanelRoot(node) {
    for (var n = node; n; n = n.parentElement) {
      if (n.querySelector && n.querySelector('.ui-agent-input')) return n;
    }
    return document;
  }

  // The answer is sent through the panel's own input row — the same send flow a
  // typed message takes — so the agent sees it as the next user turn. state is
  // what the card holds (picks, typed text): the panel stores it with the
  // message, so a redrawn thread fills the card in rather than drawing the
  // answer as a bubble of its own. A card being changed (.ui-ask-editing) asks
  // the panel to replace its earlier answer instead.
  //
  // A card that names an answer_url (data-answer-url) answers there instead: a
  // run with no chat to reply through, waiting on what the card asks.
  function uiAskSubmitAnswer(card, answer, state) {
    if (card.dataset && card.dataset.answerUrl) {
      fetch(card.dataset.answerUrl, {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({answer: answer, state: state}),
      }).then(function(r) {
        if (!r.ok) return r.text().then(function(t) { throw new Error(t || r.statusText); });
      }).catch(function(err) {
        uiAskLock(card, false);
        if (window.uiAlert) window.uiAlert('Your answer did not go through: ' + (err && err.message || err));
      });
      return true;
    }
    var root = uiAskPanelRoot(card);
    var inputArea = root.querySelector('.ui-agent-input');
    var sendBtn = root.querySelector('.ui-agent-input-row .ui-row-btn.primary');
    if (!inputArea || !sendBtn) {
      if (window.uiAlert) window.uiAlert('Could not find the chat input to submit your answer.');
      return false;
    }
    // A panel that handles answers itself takes it here, which is how an
    // answer reaches a thread whose composer is locked; otherwise it goes
    // through the Send button like a typed message.
    var edit = card.classList.contains('ui-ask-editing');
    var ev = new CustomEvent('ui-ask-answer', {detail: {answer: answer, state: state, card: card, edit: edit}, cancelable: true});
    if (!inputArea.dispatchEvent(ev)) return true;
    if (edit) return false; // only a panel can replace an answer
    inputArea.value = answer;
    sendBtn.click();
    return true;
  }

  function uiAskQuestionHTML(elm, text) {
    text = text || '';
    if (window.uiMdToHTML) { elm.innerHTML = window.uiMdToHTML(text); } else { elm.textContent = text; }
  }

  // uiAskIsCatchAll is an option that only means "something not listed". The
  // card draws its own Other row with a box to type in, so one the model wrote
  // as well was a second Other with no box: picking it told the agent nothing.
  function uiAskIsCatchAll(opt) {
    var s = String(opt || '').trim().replace(/[\s.:…]+$/, '').toLowerCase();
    return /^(other|others|something else|anything else)( \(.*\))?$/.test(s) || /^other\b.*(specify|describe|type)/.test(s);
  }

  function uiAskOptions(list) {
    return (list || []).map(function(s) { return String(s || '').trim(); })
      .filter(function(s) { return s.length > 0 && !uiAskIsCatchAll(s); });
  }

  // uiAskChoices draws a list of choices with an Other row last: a box of its
  // own and a line to type in, which ticks it. Single-select, picking Other
  // clears the rest, so the typed line is the answer; multi-select, it rides
  // along with the picks as a note. get/set read and restore {picks, other}.
  function uiAskChoices(name, opts, multi) {
    var box = el('div', {class: 'ui-ask-opts'});
    var inputs = [];
    opts.forEach(function(opt) {
      var row = el('label', {class: 'ui-ask-opt'});
      var inp = document.createElement('input');
      inp.type = multi ? 'checkbox' : 'radio';
      inp.name = name;
      inp.value = opt;
      row.appendChild(inp);
      row.appendChild(el('span', {}, [opt]));
      box.appendChild(row);
      inputs.push(inp);
    });
    var other = document.createElement('input');
    other.type = multi ? 'checkbox' : 'radio';
    other.name = name;
    other.value = '';
    var text = document.createElement('textarea');
    text.className = 'ui-ask-extra ui-ask-other-text';
    text.rows = 1;
    text.placeholder = multi ? 'Something not listed, or a note…' : 'Your own answer, or push back…';
    text.addEventListener('input', function() { if (text.value.trim()) other.checked = true; });
    other.addEventListener('change', function() { if (other.checked) text.focus(); });
    var lbl = el('label', {class: 'ui-ask-other-lbl'}, [other, el('span', {}, ['Other:'])]);
    box.appendChild(el('div', {class: 'ui-ask-opt ui-ask-other'}, [lbl, text]));
    return {
      node: box,
      focus: function() { text.focus(); },
      get: function() {
        return {
          picks: inputs.filter(function(i) { return i.checked; }).map(function(i) { return i.value; }),
          other: other.checked ? (text.value || '').trim() : '',
        };
      },
      set: function(st) {
        st = st || {};
        var picks = Array.isArray(st.picks) ? st.picks : [];
        inputs.forEach(function(i) { i.checked = picks.indexOf(i.value) >= 0; });
        text.value = st.other || '';
        other.checked = !!st.other;
      },
    };
  }

  // uiAskChoiceText is the answer a choice list gives, as the agent reads it:
  // "pick1, pick2. typed line".
  function uiAskChoiceText(st) {
    var parts = [];
    if (st && st.picks && st.picks.length) parts.push(st.picks.join(', '));
    if (st && st.other) parts.push(st.other);
    return parts.join('. ');
  }

  // uiAskLock switches an answered card's controls off, or back on while the
  // answer is being changed. Change answer and Cancel (.ui-ask-keep) stay live:
  // they are how an answered card opens again.
  function uiAskLock(wrap, locked) {
    wrap.classList.toggle('submitted', locked);
    wrap.querySelectorAll('input, textarea, select, button').forEach(function(x) {
      if (!x.classList.contains('ui-ask-keep')) x.disabled = locked;
    });
  }

  // uiAskActions is the card's button row: Submit, plus Change answer for an
  // answered card the panel linked to its answer (.ui-ask-editable, since
  // changing it replaces that message and everything after it) and Cancel
  // while it is being changed. CSS shows each in its state.
  function uiAskActions(wrap, get, set, onSubmit) {
    var submit = el('button', {class: 'ui-row-btn primary ui-ask-submit', type: 'button'}, ['Submit']);
    var change = el('button', {class: 'ui-row-btn ui-ask-keep ui-ask-change', type: 'button'}, ['Change answer']);
    var cancel = el('button', {class: 'ui-row-btn ui-ask-keep ui-ask-cancel', type: 'button'}, ['Cancel']);
    var before = null;
    change.addEventListener('click', function() {
      before = get();
      wrap.classList.add('ui-ask-editing');
      uiAskLock(wrap, false);
    });
    cancel.addEventListener('click', function() {
      if (before) set(before);
      wrap.classList.remove('ui-ask-editing');
      uiAskLock(wrap, true);
    });
    submit.addEventListener('click', function() {
      var st = get();
      var answer = onSubmit(st);
      if (!answer) return;
      if (!uiAskSubmitAnswer(wrap, answer, st)) return;
      wrap.classList.remove('ui-ask-editing');
      wrap.classList.add('ui-ask-filled');
      uiAskLock(wrap, true);
    });
    wrap.appendChild(el('div', {class: 'ui-ask-actions'}, [change, cancel, submit]));
  }

  // Both cards take an answer to show: ctx.answer on a chat redraw (the state
  // the answer was sent with), d.answer when the block carries it, or a later
  // block_meta {answer} when it arrived from elsewhere (another viewer, or the
  // run giving up waiting). Each fills the card in and locks it.
  function uiAskAnswered(wrap, set) {
    return function(rec, meta) {
      if (!meta || !meta.answer) return;
      set(meta.answer);
      wrap.classList.add('ui-ask-filled');
      uiAskLock(wrap, true);
    };
  }

  // uiAskCardWrap is the card both renderers start from, carrying the block's
  // answer_url when it answers to one.
  function uiAskCardWrap(d) {
    var wrap = el('div', {class: 'ui-ask-card'});
    if (d && d.answer_url) wrap.dataset.answerUrl = d.answer_url;
    return wrap;
  }
  if (!window.UIBlockRenderers.ui_ask) {
    window.uiRegisterBlockRenderer('ui_ask', function(d, ctx) {
      var wrap = uiAskCardWrap(d);
      var q = el('div', {class: 'ui-ask-q'});
      uiAskQuestionHTML(q, d.question);
      wrap.appendChild(q);
      var opts = uiAskOptions(d.options);
      var get, set, focus;
      if (opts.length) {
        var choices = uiAskChoices('ui-ask-' + (d.id || Math.random().toString(36).slice(2)), opts, !!d.multi);
        wrap.appendChild(choices.node);
        get = choices.get; set = choices.set; focus = choices.focus;
      } else {
        var extra = document.createElement('textarea');
        extra.className = 'ui-ask-extra';
        extra.rows = 3;
        extra.placeholder = 'Type your answer…';
        wrap.appendChild(extra);
        get = function() { return {picks: [], other: (extra.value || '').trim()}; };
        set = function(st) { extra.value = (st && st.other) || ''; };
        focus = function() { extra.focus(); };
      }
      uiAskActions(wrap, get, set, function(st) {
        var answer = uiAskChoiceText(st);
        if (!answer) focus();
        return answer;
      });
      var onMeta = uiAskAnswered(wrap, set);
      if (d.answer) onMeta(null, {answer: d.answer});
      else if (ctx && ctx.answer) { set(ctx.answer); wrap.classList.add('ui-ask-filled'); }
      return {wrap: wrap, body: null, onMeta: onMeta};
    });
  }

  if (!window.UIBlockRenderers.ui_ask_form) {
    window.uiRegisterBlockRenderer('ui_ask_form', function(d, ctx) {
      var wrap = uiAskCardWrap(d);
      var steps = (d.steps || []).filter(function(s){ return s && s.question; });
      if (!steps.length) {
        wrap.appendChild(el('div', {class: 'ui-ask-q'}, ['(form had no questions)']));
        return {wrap: wrap, body: null};
      }
      // An optional lead line over the fields: what the form is for.
      if (d.question) {
        var lead = el('div', {class: 'ui-ask-lead'});
        uiAskQuestionHTML(lead, d.question);
        wrap.appendChild(lead);
      }
      // Render every step at once as a labeled field with one Submit. Each
      // field reads and restores its own value: a choice step's {picks,
      // other}, a typed field's string.
      var fields = [];
      steps.forEach(function(step, i) {
        var fw = el('div', {class: 'ui-ask-field'});
        var lbl = el('div', {class: 'ui-ask-q'});
        uiAskQuestionHTML(lbl, (i + 1) + '. ' + step.question);
        fw.appendChild(lbl);
        var rawOpts = (step.options || []).map(function(s){ return String(s || '').trim(); }).filter(function(s){ return s.length > 0; });
        var t = step.type || (rawOpts.length ? 'choice' : 'text');
        // A chooser with nothing to choose renders a control the user cannot
        // answer through — an empty dropdown showing only its placeholder, or a
        // radio group with no rows. The server drops the type when it authors a
        // step that way, but blocks persisted before that fix still replay from
        // stored sessions, so degrade here too rather than render a dead field.
        if (!rawOpts.length && (t === 'select' || t === 'choice')) t = 'text';
        var input, f = {step: step};
        if (t === 'choice') {
          var choices = uiAskChoices('ui-askf-' + (d.id || '') + '-' + i, uiAskOptions(rawOpts), !!step.multi);
          input = choices.node;
          f.get = choices.get; f.set = choices.set;
          f.text = uiAskChoiceText;
        } else {
          if (t === 'textarea') {
            input = document.createElement('textarea'); input.className = 'ui-ask-extra'; input.rows = 3;
          } else if (t === 'select') {
            input = document.createElement('select'); input.className = 'ui-ask-input';
            input.appendChild(el('option', {value: ''}, ['(choose)']));
            rawOpts.forEach(function(o){ input.appendChild(el('option', {value: o}, [o])); });
          } else {
            input = document.createElement('input');
            input.type = (t === 'number') ? 'number' : (t === 'password' ? 'password' : 'text');
            input.className = 'ui-ask-input';
          }
          if (step.placeholder && t !== 'select') input.placeholder = step.placeholder;
          // A secret is never kept to be filled back in: it went to the agent
          // as the answer, and a redraw has no business showing it again.
          var secret = t === 'password';
          f.get = function() { return secret ? '' : (input.value || '').trim(); };
          f.set = function(v) { input.value = (typeof v === 'string') ? v : ''; };
          f.text = function() { return (input.value || '').trim(); };
        }
        fw.appendChild(input);
        wrap.appendChild(fw);
        fields.push(f);
      });
      var get = function() { return {values: fields.map(function(f) { return f.get(); })}; };
      var set = function(st) {
        var vals = (st && Array.isArray(st.values)) ? st.values : [];
        fields.forEach(function(f, i) { f.set(vals[i]); });
      };
      uiAskActions(wrap, get, set, function() {
        return fields.map(function(f, i) {
          return (i + 1) + '. ' + f.step.question + ' -> ' + (f.text(f.get()) || '(no answer)');
        }).join('\n');
      });
      var onMeta = uiAskAnswered(wrap, set);
      if (d.answer) onMeta(null, {answer: d.answer});
      else if (ctx && ctx.answer) { set(ctx.answer); wrap.classList.add('ui-ask-filled'); }
      return {wrap: wrap, body: null, onMeta: onMeta};
    });
  }

  // ui_notes_consumed — the framework's "the agent picked up your notes" signal.
  //
  // A note written mid-turn waits at the bottom of the conversation until the
  // runner drains the queue between rounds. The bubble carries data-note-id from
  // the moment the inject POST returns; this is what says those ids have been
  // read, so the note settles into ordinary history instead of going on looking
  // pending forever.
  //
  // Generic and here, rather than one copy per app, because the note queue is a
  // framework feature: the panel owns the composer, the bubble, the data-note-id
  // and the inject contract. Two apps had each written their own near-identical
  // renderer under their own block type, and each lived in that app's page head
  // — so an app that EMBEDS another app's chat received the server event with
  // nothing registered to hear it, and no note it drained was ever marked read.
  // Two copies of one behaviour, and the surface between them got neither.
  //
  // Same registration shape as the ask cards above: at IIFE time, guarded, so an
  // app that wants a richer version still wins.
  if (!window.UIBlockRenderers.ui_notes_consumed) {
    window.uiRegisterBlockRenderer('ui_notes_consumed', function(d, ctx) {
      var ids = d.ids || [];
      // The notes' text, when the server sent it: what to draw for a note this
      // page has no bubble for (written before the page arrived).
      var texts = {};
      (d.notes || []).forEach(function(n) { if (n && n.id) texts[n.id] = n.text || ''; });
      ids.forEach(function(noteID) {
        // CSS.escape because a note id is server-issued and need not be a bare
        // identifier; an unescaped one would either match nothing or throw.
        var esc = (window.CSS && CSS.escape ? CSS.escape(noteID) : noteID);
        if (!document.querySelector('[data-note-id="' + esc + '"]')) {
          if (ctx && ctx.addNote && texts[noteID]) ctx.addNote(noteID, texts[noteID]);
          return;
        }
        var bubble = document.querySelector('.ui-agent-interjection[data-note-id="' + esc + '"]');
        if (!bubble) return;
        bubble.classList.add('consumed');
        // A note cannot be both read and unread. Clearing the undelivered mark
        // matters on a re-attach, where the panel may have given up on a note
        // the server had in fact drained.
        bubble.classList.remove('ui-agent-interjection-undelivered');
        var note = bubble.querySelector('.ui-agent-interjection-note');
        if (note) note.remove();
      });
      var n = ids.length;
      if (!n) return null;
      var wrap = el('div', {class: 'ui-agent-act ui-agent-act-status'},
        [n + ' note' + (n === 1 ? '' : 's') + ' picked up by agent']);
      return {wrap: wrap, body: null, pane: 'activity'};
    });
  }
