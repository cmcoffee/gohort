  // --- assist editor ----------------------------------------------------
  //
  // A modal workbench for drafting one value with the model: the text on
  // the left, a conversation on the right, and a version walk across the
  // iterations. Replaces the one-shot "type a hint, get a replacement"
  // flow for anything long enough to be worth revising rather than
  // regenerating.
  //
  // Domain-agnostic. The caller supplies a send() that owns the request
  // shape, so this file knows nothing about any app's endpoint.
  //
  // Versions are a plain list of snapshots, never truncated. The textarea
  // always edits the SELECTED version in place, and a model proposal
  // appends a new one and selects it. Walking back and editing therefore
  // cannot destroy a later draft, which matters because the only thing
  // worse than a bad suggestion is one that ate the good draft you had.

  // uiOpenAssist(opts)
  //   title     — modal title
  //   subtitle  — one-line context under it (the field's help, usually)
  //   initial   — the text to open with
  //   placeholder — composer placeholder
  //   send      — fn({message, draft, history}, done). Call
  //               done({reply, value}) on success — value omitted or
  //               empty means the model answered without proposing a
  //               change — or done(null, "error text") on failure.
  //   ask       — an opening request, sent automatically on open, as
  //               though the caller had typed it. For a workbench opened
  //               with a specific brief ("settle this finding", "make it
  //               shorter") rather than a blank invitation to draft:
  //               without it every such caller repeats the brief in a
  //               subtitle the person then has to retype.
  //   onAccept  — fn(text) when the user keeps the draft.
  window.uiOpenAssist = function(opts) {
    opts = opts || {};
    var versions = [String(opts.initial == null ? '' : opts.initial)];
    var idx = 0;
    var history = []; // [{role, content}] — the conversation so far
    var busy = false;

    var draft = el('textarea', {
      class: 'ui-assist-draft',
      placeholder: opts.placeholder || 'The draft appears here. Edit it directly, or ask for changes on the right.',
    });
    draft.value = versions[0];
    draft.addEventListener('input', function() { versions[idx] = draft.value; });

    var backBtn = el('button', {type: 'button', class: 'ui-assist-nav', title: 'Previous version'}, ['◀']);
    var fwdBtn = el('button', {type: 'button', class: 'ui-assist-nav', title: 'Next version'}, ['▶']);
    var verLabel = el('span', {class: 'ui-assist-ver'});

    function updateNav() {
      backBtn.disabled = idx <= 0;
      fwdBtn.disabled = idx >= versions.length - 1;
      verLabel.textContent = versions.length > 1
        ? 'v' + (idx + 1) + ' of ' + versions.length
        : 'original';
    }
    function showVersion(i) {
      if (i < 0 || i >= versions.length) return;
      idx = i;
      draft.value = versions[idx];
      updateNav();
    }
    backBtn.addEventListener('click', function() { showVersion(idx - 1); });
    fwdBtn.addEventListener('click', function() { showVersion(idx + 1); });
    updateNav();

    var log = el('div', {class: 'ui-assist-log'});
    function addMsg(role, text, isHTML) {
      var m = el('div', {class: 'ui-assist-msg ' + role});
      if (isHTML) m.innerHTML = text;
      else m.textContent = text;
      log.appendChild(m);
      log.scrollTop = log.scrollHeight;
      return m;
    }
    addMsg('note', versions[0].trim() === ''
      ? 'Nothing written yet. Describe what you want and it will draft one.'
      : 'Ask for a change to the draft on the left, or a question about it.');

    var composer = el('textarea', {
      class: 'ui-assist-input',
      rows: '2',
      placeholder: 'Tighten the second paragraph…',
    });
    var sendBtn = el('button', {type: 'button', class: 'ui-assist-send'}, ['Send']);

    function setBusy(on) {
      busy = on;
      sendBtn.disabled = on;
      composer.disabled = on;
    }

    function send() {
      if (busy) return;
      var text = (composer.value || '').trim();
      if (!text) return;
      composer.value = '';
      addMsg('user', text);
      // Snapshot the history BEFORE this turn — the server appends the
      // new message itself, so sending it in both places would duplicate.
      var priorHistory = history.slice();
      history.push({role: 'user', content: text});
      setBusy(true);
      var pending = addMsg('assistant pending', 'Thinking…');

      opts.send({message: text, draft: draft.value, history: priorHistory}, function(res, err) {
        pending.remove();
        setBusy(false);
        if (err || !res) {
          addMsg('error', 'Error: ' + (err || 'no response'));
          // Drop the turn that produced nothing so a retry doesn't ask
          // the model to account for a reply it never gave.
          history.pop();
          return;
        }
        var reply = String(res.reply == null ? '' : res.reply).trim();
        var value = res.value == null ? '' : String(res.value);
        if (value !== '' && value !== versions[idx]) {
          versions.push(value);
          showVersion(versions.length - 1);
          if (reply === '') reply = 'Updated the draft.';
        } else if (reply === '') {
          reply = '(no reply)';
        }
        addMsg('assistant', mdToHTML(reply), true);
        history.push({role: 'assistant', content: reply});
        composer.focus();
      });
    }
    sendBtn.addEventListener('click', send);
    composer.addEventListener('keydown', function(ev) {
      // Enter sends, Shift+Enter breaks the line: this is a chat box, and
      // multi-line requests are rare enough to cost a modifier.
      if (ev.key === 'Enter' && !ev.shiftKey) {
        ev.preventDefault();
        send();
      }
    });

    window.uiOpenModal({
      title: opts.title || 'Draft with assistance',
      subtitle: opts.subtitle || undefined,
      width: 'min(1180px, 96vw)',
      actions: [
        {label: 'Cancel'},
        {label: 'Use this draft', primary: true, onClick: function(api) {
          if (typeof opts.onAccept === 'function') opts.onAccept(draft.value);
          api.close();
        }},
      ],
      mount: function(body) {
        var wrap = el('div', {class: 'ui-assist'});

        var left = el('div', {class: 'ui-assist-left'});
        var navRow = el('div', {class: 'ui-assist-navrow'});
        navRow.appendChild(backBtn);
        navRow.appendChild(verLabel);
        navRow.appendChild(fwdBtn);
        left.appendChild(navRow);
        left.appendChild(draft);

        var right = el('div', {class: 'ui-assist-right'});
        right.appendChild(log);
        var composerRow = el('div', {class: 'ui-assist-composer'});
        composerRow.appendChild(composer);
        composerRow.appendChild(sendBtn);
        right.appendChild(composerRow);

        wrap.appendChild(left);
        wrap.appendChild(right);
        body.appendChild(wrap);
        setTimeout(function() {
          composer.focus();
          // The opening request, if the caller had one. Sent through the
          // same path a typed one takes, so it appears in the log, joins
          // the history, and produces a version like any other — the
          // person can walk back to the original from it.
          var ask = String(opts.ask == null ? '' : opts.ask).trim();
          if (ask) {
            composer.value = ask;
            send();
          }
        }, 0);
      },
    });
  };
