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
  //   mode      — 'discuss' talks it through before anything is written:
  //               the model asks, proposes, and returns text only once the
  //               person agrees (the server's reply contract does that;
  //               mode rides on every send so it knows which to use). Adds
  //               a "Write it now" button for when enough has been said.
  //               'write' revises on request: every turn may propose text.
  //               Either one pins the window to it.
  //   switchable — no mode pinned: the person picks, with a switch at the
  //               top, "Talk it through" or "Just write", and can change it
  //               mid-conversation. Opens on talking it through when there
  //               is nothing written and no opening request, else on
  //               writing. Only for a caller whose send() passes req.mode
  //               to an endpoint that honours it (AssistReplyContract).
  //               Without either: revise on request.
  window.uiOpenAssist = function(opts) {
    opts = opts || {};
    var versions = [String(opts.initial == null ? '' : opts.initial)];
    var switchable = !!opts.switchable && !opts.mode;
    var discuss = opts.mode === 'discuss' ||
      (switchable && versions[0].trim() === '' && !String(opts.ask == null ? '' : opts.ask).trim());
    var idx = 0;
    var history = []; // [{role, content}] — the conversation so far
    var busy = false;
    // A touch screen: the keyboard is not raised on its own, since it would
    // cover what was just said or written.
    var coarse = !!(window.matchMedia && window.matchMedia('(pointer: coarse)').matches);

    var draft = el('textarea', {
      class: 'ui-assist-draft',
      placeholder: opts.placeholder || 'The draft appears here. Edit it directly, or ask for changes in the conversation.',
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

    // On a narrow screen the two sides take turns, as tabs, rather than
    // stacking into two boxes too short to read. The composer stays under
    // both, so a change can be asked for while looking at the draft. On a
    // wide screen the tabs are hidden and both sides show, as before.
    var wrap = el('div', {class: 'ui-assist'});
    var chatTab = el('button', {type: 'button', class: 'ui-tab', role: 'tab'}, ['Conversation']);
    var draftTab = el('button', {type: 'button', class: 'ui-tab', role: 'tab'}, ['Draft']);
    function showSide(side) {
      wrap.classList.toggle('ui-assist-on-chat', side === 'chat');
      wrap.classList.toggle('ui-assist-on-draft', side === 'draft');
      chatTab.classList.toggle('active', side === 'chat');
      draftTab.classList.toggle('active', side === 'draft');
      chatTab.setAttribute('aria-selected', side === 'chat' ? 'true' : 'false');
      draftTab.setAttribute('aria-selected', side === 'draft' ? 'true' : 'false');
      if (side === 'draft') draftTab.classList.remove('ui-assist-tab-new');
    }
    chatTab.addEventListener('click', function() { showSide('chat'); });
    draftTab.addEventListener('click', function() { showSide('draft'); });
    // Talking it through starts on the conversation; revising text that is
    // already there starts on that text.
    showSide(discuss || versions[0].trim() === '' ? 'chat' : 'draft');
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
    var discussNote = 'It asks first, then says what it will write. Nothing changes in the draft until it writes, and nothing is kept until you use the draft.';
    addMsg('note', discuss
      ? discussNote
      : (versions[0].trim() === ''
        ? 'Nothing written yet. Describe what you want and it will draft one.'
        : 'Ask for a change to the draft, or a question about it.'));

    var composer = el('textarea', {
      class: 'ui-assist-input',
      rows: (window.matchMedia && window.matchMedia('(max-width: 760px)').matches) ? '1' : '2',
    });
    function placeholderFor() {
      return discuss ? 'Answer, or say what it needs…' : 'Tighten the second paragraph…';
    }
    composer.placeholder = placeholderFor();
    var sendBtn = el('button', {type: 'button', class: 'ui-assist-send', 'aria-label': 'Send'}, ['Send']);
    // Discuss mode: skip ahead once enough has been said. Sent as a message
    // like any other, so it is in the log and the history.
    var writeBtn = (discuss || switchable) ? el('button', {type: 'button', class: 'ui-assist-send ui-assist-write', title: 'Stop discussing and write it from what has been said'}, ['Write it now']) : null;
    var writeNowText = 'Write it now from what we have discussed. Say in one sentence what you had to assume.';

    // The switch between the two, when the caller lets the person choose.
    // The mode rides on each send, so a switch takes effect on the next
    // message and the conversation so far carries on under it.
    var modeRow = null, talkBtn = null, justBtn = null;
    function showMode() {
      if (writeBtn) writeBtn.style.display = discuss ? '' : 'none';
      composer.placeholder = placeholderFor();
      if (!modeRow) return;
      talkBtn.classList.toggle('active', discuss);
      justBtn.classList.toggle('active', !discuss);
      talkBtn.setAttribute('aria-checked', discuss ? 'true' : 'false');
      justBtn.setAttribute('aria-checked', discuss ? 'false' : 'true');
    }
    function setMode(toDiscuss) {
      if (toDiscuss === discuss) return;
      discuss = toDiscuss;
      showMode();
      addMsg('note', discuss
        ? 'Talking it through: ' + discussNote.charAt(0).toLowerCase() + discussNote.slice(1)
        : 'Just write: each request can change the draft right away.');
    }
    if (switchable) {
      talkBtn = el('button', {type: 'button', class: 'ui-assist-mode', role: 'radio',
        title: 'It asks what it needs and says what it will write before writing anything'}, ['Talk it through']);
      justBtn = el('button', {type: 'button', class: 'ui-assist-mode', role: 'radio',
        title: 'Each request can change the draft right away'}, ['Just write']);
      talkBtn.addEventListener('click', function() { setMode(true); });
      justBtn.addEventListener('click', function() { setMode(false); });
      modeRow = el('div', {class: 'ui-assist-modes', role: 'radiogroup', 'aria-label': 'How it works'}, [talkBtn, justBtn]);
    }
    showMode();

    function setBusy(on) {
      busy = on;
      sendBtn.disabled = on;
      composer.disabled = on;
      if (writeBtn) writeBtn.disabled = on;
    }

    function send(preset) {
      if (busy) return;
      var text = (typeof preset === 'string' ? preset : (composer.value || '')).trim();
      if (!text) return;
      composer.value = '';
      addMsg('user', text);
      // Snapshot the history BEFORE this turn — the server appends the
      // new message itself, so sending it in both places would duplicate.
      var priorHistory = history.slice();
      history.push({role: 'user', content: text});
      setBusy(true);
      var pending = addMsg('assistant pending', 'Thinking…');

      opts.send({message: text, draft: draft.value, history: priorHistory, mode: discuss ? 'discuss' : 'write'}, function(res, err) {
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
          // Looking at the conversation: the reply stays in view, and the
          // Draft tab is marked so the new version is not missed.
          if (wrap.classList.contains('ui-assist-on-chat')) draftTab.classList.add('ui-assist-tab-new');
          if (reply === '') reply = 'Updated the draft.';
        } else if (reply === '') {
          reply = '(no reply)';
        }
        addMsg('assistant', mdToHTML(reply), true);
        history.push({role: 'assistant', content: reply});
        if (!coarse) composer.focus();
      });
    }
    sendBtn.addEventListener('click', function() { send(); });
    if (writeBtn) writeBtn.addEventListener('click', function() { send(writeNowText); });
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
      mount: function(body, api) {
        api.overlay.classList.add('ui-assist-overlay');
        api.dialog.classList.add('ui-assist-modal');
        var tabs = el('div', {class: 'ui-assist-tabs', role: 'tablist'});
        tabs.appendChild(chatTab);
        tabs.appendChild(draftTab);
        wrap.appendChild(tabs);

        var left = el('div', {class: 'ui-assist-left'});
        var navRow = el('div', {class: 'ui-assist-navrow'});
        navRow.appendChild(backBtn);
        navRow.appendChild(verLabel);
        navRow.appendChild(fwdBtn);
        left.appendChild(navRow);
        left.appendChild(draft);

        var right = el('div', {class: 'ui-assist-right'});
        if (modeRow) right.appendChild(modeRow);
        right.appendChild(log);
        var composerRow = el('div', {class: 'ui-assist-composer'});
        var field = el('div', {class: 'ui-assist-field'});
        field.appendChild(composer);
        field.appendChild(sendBtn);
        composerRow.appendChild(field);
        if (writeBtn) composerRow.appendChild(writeBtn);
        right.appendChild(composerRow);

        wrap.appendChild(left);
        wrap.appendChild(right);
        body.appendChild(wrap);
        setTimeout(function() {
          if (!coarse) composer.focus();
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
