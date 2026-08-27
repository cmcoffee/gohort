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
  // typed message takes — so the agent sees it as the next user turn.
  function uiAskSubmitAnswer(card, answer) {
    var root = uiAskPanelRoot(card);
    var inputArea = root.querySelector('.ui-agent-input');
    var sendBtn = root.querySelector('.ui-agent-input-row .ui-row-btn.primary');
    if (!inputArea || !sendBtn) {
      if (window.uiAlert) window.uiAlert('Could not find the chat input to submit your answer.');
      return false;
    }
    inputArea.value = answer;
    sendBtn.click();
    return true;
  }

  function uiAskQuestionHTML(elm, text) {
    text = text || '';
    if (window.uiMdToHTML) { elm.innerHTML = window.uiMdToHTML(text); } else { elm.textContent = text; }
  }

  if (!window.UIBlockRenderers.ui_ask) {
    window.uiRegisterBlockRenderer('ui_ask', function(d) {
      var wrap = el('div', {class: 'ui-ask-card'});
      var q = el('div', {class: 'ui-ask-q'});
      uiAskQuestionHTML(q, d.question);
      wrap.appendChild(q);
      var opts = (d.options || []).map(function(s){ return String(s || '').trim(); }).filter(function(s){ return s.length > 0; });
      var multi = !!d.multi;
      var inputs = [];
      if (opts.length) {
        var box = el('div', {class: 'ui-ask-opts'});
        opts.forEach(function(opt) {
          var row = el('label', {class: 'ui-ask-opt'});
          var inp = document.createElement('input');
          inp.type = multi ? 'checkbox' : 'radio';
          inp.name = 'ui-ask-' + (d.id || '');
          inp.value = opt;
          row.appendChild(inp);
          row.appendChild(el('span', {}, [opt]));
          box.appendChild(row);
          inputs.push(inp);
        });
        wrap.appendChild(box);
      }
      var extra = document.createElement('textarea');
      extra.className = 'ui-ask-extra';
      extra.rows = opts.length ? 2 : 3;
      extra.placeholder = opts.length ? 'Or type your own answer / push back…' : 'Type your answer…';
      wrap.appendChild(extra);
      var submit = el('button', {class: 'ui-row-btn primary', type: 'button'}, ['Submit']);
      wrap.appendChild(el('div', {class: 'ui-ask-actions'}, [submit]));
      submit.addEventListener('click', function() {
        var picked = inputs.filter(function(i){ return i.checked; }).map(function(i){ return i.value; });
        var note = (extra.value || '').trim();
        var parts = [];
        if (picked.length) parts.push(picked.join(', '));
        if (note) parts.push(note);
        var answer = parts.join('. ');
        if (!answer) { extra.focus(); return; }
        if (!uiAskSubmitAnswer(wrap, answer)) return;
        wrap.classList.add('submitted');
        inputs.forEach(function(i){ i.disabled = true; });
        extra.disabled = true;
        submit.disabled = true;
      });
      return {wrap: wrap, body: null};
    });
  }

  if (!window.UIBlockRenderers.ui_ask_form) {
    window.uiRegisterBlockRenderer('ui_ask_form', function(d) {
      var wrap = el('div', {class: 'ui-ask-card'});
      var steps = (d.steps || []).filter(function(s){ return s && s.question; });
      if (!steps.length) {
        wrap.appendChild(el('div', {class: 'ui-ask-q'}, ['(form had no questions)']));
        return {wrap: wrap, body: null};
      }
      // Render every step at once as a labeled field with one Submit (the
      // compact default; the Agency chat ships a step-through variant).
      var fields = [];
      steps.forEach(function(step, i) {
        var fw = el('div', {class: 'ui-ask-field'});
        var lbl = el('div', {class: 'ui-ask-q'});
        uiAskQuestionHTML(lbl, (i + 1) + '. ' + step.question);
        fw.appendChild(lbl);
        var opts = (step.options || []).map(function(s){ return String(s || '').trim(); }).filter(function(s){ return s.length > 0; });
        var t = step.type || (opts.length ? 'choice' : 'text');
        var input, getVal;
        if (t === 'textarea') {
          input = document.createElement('textarea'); input.className = 'ui-ask-extra'; input.rows = 3;
          if (step.placeholder) input.placeholder = step.placeholder;
          getVal = function(){ return (input.value || '').trim(); };
        } else if (t === 'select') {
          input = document.createElement('select'); input.className = 'ui-ask-input';
          input.appendChild(el('option', {value: ''}, ['— choose —']));
          opts.forEach(function(o){ input.appendChild(el('option', {value: o}, [o])); });
          getVal = function(){ return input.value || ''; };
        } else if (t === 'choice') {
          input = el('div', {class: 'ui-ask-opts'});
          var multi = !!step.multi;
          opts.forEach(function(o) {
            var row = el('label', {class: 'ui-ask-opt'});
            var inp = document.createElement('input');
            inp.type = multi ? 'checkbox' : 'radio';
            inp.name = 'ui-askf-' + (d.id || '') + '-' + i;
            inp.value = o;
            row.appendChild(inp);
            row.appendChild(el('span', {}, [o]));
            input.appendChild(row);
          });
          getVal = function(){ var p = []; input.querySelectorAll('input').forEach(function(x){ if (x.checked) p.push(x.value); }); return p.join(', '); };
        } else {
          input = document.createElement('input');
          input.type = (t === 'number') ? 'number' : (t === 'password' ? 'password' : 'text');
          input.className = 'ui-ask-input';
          if (step.placeholder) input.placeholder = step.placeholder;
          getVal = function(){ return (input.value || '').trim(); };
        }
        fw.appendChild(input);
        wrap.appendChild(fw);
        fields.push({step: step, getVal: getVal});
      });
      var submit = el('button', {class: 'ui-row-btn primary', type: 'button'}, ['Submit']);
      wrap.appendChild(el('div', {class: 'ui-ask-actions'}, [submit]));
      submit.addEventListener('click', function() {
        var lines = fields.map(function(f, i){ var v = f.getVal(); return (i + 1) + '. ' + f.step.question + ' -> ' + (v || '(no answer)'); });
        if (!uiAskSubmitAnswer(wrap, lines.join('\n'))) return;
        wrap.classList.add('submitted');
        wrap.querySelectorAll('input, textarea, select, button').forEach(function(x){ x.disabled = true; });
      });
      return {wrap: wrap, body: null};
    });
  }
