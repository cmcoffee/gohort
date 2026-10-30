  // --- assist ---------------------------------------------------------
  //
  // Help from the model with one thing someone is working on: the work on
  // the left, a conversation on the right. Two shapes share the
  // conversation:
  //
  //   uiOpenAssist  a modal workbench for drafting one value: the text,
  //                 with a version walk across the iterations.
  //   assist_panel  a component (ui.AssistPanel) whose left side is any
  //                 component, a form usually: the model's proposals fill
  //                 its fields, with the same version walk, and the form's
  //                 own Save keeps them.
  //
  // Domain-agnostic. The caller supplies a send() that owns the request
  // shape, or the URL of an endpoint that speaks the core assist protocol
  // (uiAssistPost), so this file knows nothing about any app's endpoint.
  //
  // Versions are a plain list of snapshots, never truncated. The work
  // always edits the SELECTED version in place, and a model proposal
  // appends a new one and selects it. Walking back and editing therefore
  // cannot destroy a later draft, which matters because the only thing
  // worse than a bad suggestion is one that ate the good draft you had.

  var assistDiscussNote = 'It asks first, then says what it will write. Nothing changes in the draft until it writes, and nothing is kept until you use the draft.';

  // uiAssistChat(opts) is the conversation half, for both shapes:
  //   send      — fn(req, done, progress, signal). req is {message, history,
  //               mode, web} plus extra(). Call done({reply, ...}) on
  //               success or done(null, "error text") on failure; progress
  //               (text) says what it is doing meanwhile; signal aborts
  //               when the person presses Stop.
  //   extra     — fn() giving the rest of each request: the draft, or the
  //               form and its values.
  //   onResult  — fn(res) applying what came back (a value, fields), and
  //               returning what to say when the reply is empty, or ''.
  //   discuss   — whether it starts by talking it through.
  //   switchable — show the "Talk it through | Just write" switch.
  //   web       — offer "Look things up on the web", off until turned on.
  //   attach    — offer a 📎 to attach files, for the endpoint's tools.
  //   starters  — opening requests offered as buttons, before anything is
  //               said: the two things most people came to ask.
  //   intro     — the opening note.
  //   writeNote — the note when switched to just writing.
  //   placeholders — {discuss, write} for the composer.
  // Returns {right, say(text), focus(), discuss()}: right is the pane.
  window.uiAssistChat = function(opts) {
    opts = opts || {};
    var discuss = !!opts.discuss;
    var switchable = !!opts.switchable;
    var history = []; // [{role, content}] — the conversation so far
    var busy = null;  // the turn in flight: {ctl}
    var coarse = !!(window.matchMedia && window.matchMedia('(pointer: coarse)').matches);
    var ph = opts.placeholders || {};
    // The window's name, on every turn: the endpoint keeps what was attached
    // in it under this, so a file goes once and serves the conversation.
    var windowId = 'w' + Math.random().toString(36).slice(2, 12) + Date.now().toString(36);

    var log = el('div', {class: 'ui-assist-log'});
    function addMsg(role, text, isHTML) {
      // Rendered markdown takes the prose class, so a code block in a reply
      // scrolls inside its box instead of running past the window.
      var m = el('div', {class: 'ui-assist-msg ' + role + (isHTML ? ' ui-md' : '')});
      if (isHTML) m.innerHTML = text;
      else m.textContent = text;
      log.appendChild(m);
      log.scrollTop = log.scrollHeight;
      return m;
    }
    if (opts.intro) addMsg('note', opts.intro);

    var composer = el('textarea', {
      class: 'ui-assist-input',
      rows: (window.matchMedia && window.matchMedia('(max-width: 760px)').matches) ? '1' : '2',
    });
    function placeholderFor() {
      return discuss ? (ph.discuss || 'Answer, or say what it needs…') : (ph.write || 'Tighten the second paragraph…');
    }
    composer.placeholder = placeholderFor();
    var sendBtn = el('button', {type: 'button', class: 'ui-assist-send', 'aria-label': 'Send'}, ['Send']);

    // The switch between the two, when the caller lets the person choose.
    // The mode rides on each send, so a switch takes effect on the next
    // message and the conversation so far carries on under it.
    var modeRow = null, talkBtn = null, justBtn = null;
    function showMode() {
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
        ? 'Talking it through: ' + assistDiscussNote.charAt(0).toLowerCase() + assistDiscussNote.slice(1)
        : (opts.writeNote || 'Just write: each request can change the draft right away.'));
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

    // Looking things up on the web: offered by the caller, off until the
    // person turns it on, because a search sends their words to an outside
    // engine. It rides on each send, like the mode.
    var webBox = null, webRow = null;
    if (opts.web) {
      webBox = el('input', {type: 'checkbox', class: 'ui-assist-web-box'});
      webRow = el('label', {class: 'ui-assist-web', title: 'Lets it search the web and read pages, sending what it searches for to a search engine'},
        [webBox, el('span', {}, ['Look things up on the web'])]);
    }

    // Files for the endpoint's tools (a sample to try a conversion on):
    // picked with the 📎, shown until the next send carries them.
    var pendingFiles = [], attachBtn = null, fileRow = null, picker = null;
    function drawFiles() {
      fileRow.innerHTML = '';
      fileRow.style.display = pendingFiles.length ? '' : 'none';
      pendingFiles.forEach(function(f, i) {
        var x = el('button', {type: 'button', class: 'ui-assist-file-x', title: 'Remove', 'aria-label': 'Remove ' + f.name}, ['×']);
        x.addEventListener('click', function() { pendingFiles.splice(i, 1); drawFiles(); });
        fileRow.appendChild(el('span', {class: 'ui-assist-file'}, [el('span', {}, [f.name]), x]));
      });
    }
    if (opts.attach) {
      picker = el('input', {type: 'file', multiple: true, style: 'display:none'});
      attachBtn = el('button', {type: 'button', class: 'ui-assist-attach', title: 'Attach a file for it to try things on', 'aria-label': 'Attach a file'}, ['📎']);
      fileRow = el('div', {class: 'ui-assist-files'});
      fileRow.style.display = 'none';
      attachBtn.addEventListener('click', function() { picker.click(); });
      picker.addEventListener('change', function() {
        Array.prototype.forEach.call(picker.files || [], function(f) { pendingFiles.push(f); });
        picker.value = '';
        drawFiles();
      });
    }
    // readFiles is the files as a turn carries them, base64.
    function readFiles(files) {
      return Promise.all(files.map(function(f) {
        return new Promise(function(resolve, reject) {
          var rd = new FileReader();
          rd.onload = function() { var u = String(rd.result || ''); resolve({name: f.name, data: u.slice(u.indexOf(',') + 1)}); };
          rd.onerror = function() { reject(new Error(f.name + ' could not be read')); };
          rd.readAsDataURL(f);
        });
      }));
    }

    function setBusy(turn) {
      busy = turn;
      composer.disabled = !!turn;
      // While a turn runs, Send is its Stop.
      sendBtn.textContent = turn ? 'Stop' : 'Send';
      sendBtn.setAttribute('aria-label', turn ? 'Stop' : 'Send');
      sendBtn.classList.toggle('ui-assist-stop', !!turn);
    }

    // The answers to the question just asked, as buttons: one sends itself as
    // the reply. Any send answers the question, so the buttons go then.
    var choices = null;
    function addChoices(options) {
      if (!Array.isArray(options)) return;
      var opts2 = options.map(function(o) { return String(o == null ? '' : o).trim(); }).filter(Boolean);
      if (!opts2.length) return;
      choices = el('div', {class: 'ui-assist-choices'}, opts2.map(function(o) {
        var b = el('button', {type: 'button', class: 'ui-assist-choice'}, [o]);
        b.addEventListener('click', function() { send(o); });
        return b;
      }));
      log.appendChild(choices);
      log.scrollTop = log.scrollHeight;
    }

    // The pending line is alive: a moving frame, what it is doing when the
    // endpoint says, and the seconds once past three.
    function pendingLine() {
      var m = addMsg('assistant pending', '');
      var frames = '⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏', fi = 0, started = Date.now(), doing = 'Thinking';
      function paint() {
        var secs = Math.round((Date.now() - started) / 1000);
        m.textContent = frames.charAt(fi++ % frames.length) + ' ' + doing + '…' + (secs >= 3 ? ' ' + secs + 's' : '');
      }
      paint();
      var timer = setInterval(paint, 250);
      if (timer && timer.unref) timer.unref();
      return {
        doing: function(text) { doing = String(text || 'Thinking'); paint(); },
        done: function() { clearInterval(timer); m.remove(); },
      };
    }

    function send(preset) {
      if (busy) return;
      var text = (typeof preset === 'string' ? preset : (composer.value || '')).trim();
      var files = pendingFiles.slice();
      if (!text && files.length) text = 'I attached ' + (files.length > 1 ? 'files' : 'a file') + '.';
      if (!text) return;
      if (choices) { choices.remove(); choices = null; }
      composer.value = '';
      addMsg('user', text + (files.length ? '\n📎 ' + files.map(function(f) { return f.name; }).join(', ') : ''));
      if (files.length) { pendingFiles = []; drawFiles(); }
      // Snapshot the history BEFORE this turn — the server appends the
      // new message itself, so sending it in both places would duplicate.
      var priorHistory = history.slice();
      history.push({role: 'user', content: text});
      var turn = {ctl: (typeof AbortController !== 'undefined') ? new AbortController() : null};
      setBusy(turn);
      var pending = pendingLine();
      var req = {message: text, history: priorHistory, mode: discuss ? 'discuss' : 'write', window: windowId};
      if (webBox) req.web = !!webBox.checked;
      var more = typeof opts.extra === 'function' ? (opts.extra() || {}) : {};
      Object.keys(more).forEach(function(k) { req[k] = more[k]; });
      var settled = false;
      // Stop ends the turn here, whether or not the caller's send() heeds
      // the signal: an answer that arrives after it is dropped.
      turn.stop = function() {
        if (turn.ctl) turn.ctl.abort();
        finish(null, 'Stopped.');
      };
      var go = function() {
        if (settled) return;
        opts.send(req, finish, function(text) { if (!settled) pending.doing(text); }, turn.ctl ? turn.ctl.signal : undefined);
      };
      if (files.length) {
        readFiles(files).then(function(got) { req.files = got; go(); }, function(e) { finish(null, e.message); });
      } else {
        go();
      }
      function finish(res, err) {
        if (settled) return;
        settled = true;
        pending.done();
        setBusy(null);
        if (err || !res) {
          if (err === 'Stopped.') addMsg('note', 'Stopped.');
          else addMsg('error', 'Error: ' + (err || 'no response'));
          // Drop the turn that produced nothing so a retry doesn't ask
          // the model to account for a reply it never gave.
          history.pop();
          return;
        }
        var reply = String(res.reply == null ? '' : res.reply).trim();
        var said = typeof opts.onResult === 'function' ? opts.onResult(res) : '';
        if (reply === '') reply = said || '(no reply)';
        addMsg('assistant', mdToHTML(reply), true);
        history.push({role: 'assistant', content: reply});
        addChoices(res.options);
        if (!coarse) composer.focus();
      }
    }
    sendBtn.addEventListener('click', function() {
      if (busy) { busy.stop(); return; }
      send();
    });
    composer.addEventListener('keydown', function(ev) {
      // Enter sends, Shift+Enter breaks the line: this is a chat box, and
      // multi-line requests are rare enough to cost a modifier.
      if (ev.key === 'Enter' && !ev.shiftKey) {
        ev.preventDefault();
        send();
      }
    });

    var right = el('div', {class: 'ui-assist-right'});
    if (modeRow) right.appendChild(modeRow);
    right.appendChild(log);
    var composerRow = el('div', {class: 'ui-assist-composer'});
    var field = el('div', {class: 'ui-assist-field'});
    if (attachBtn) field.appendChild(attachBtn);
    field.appendChild(composer);
    field.appendChild(sendBtn);
    composerRow.appendChild(field);
    if (fileRow) { right.appendChild(picker); right.appendChild(fileRow); }
    right.appendChild(composerRow);
    if (webRow) right.appendChild(webRow);
    if (opts.starters) addChoices(opts.starters);

    return {
      right: right,
      say: function(text) { send(String(text == null ? '' : text)); },
      focus: function() { if (!coarse) composer.focus(); },
      discuss: function() { return discuss; },
    };
  };

  // uiAssistPost(url) is a send() for an endpoint that speaks the core
  // assist protocol (core/assist): the request as JSON, and back a stream of
  // JSON lines, {progress} while it works and one {reply, value, fields,
  // options} or {error} at the end. A plain JSON answer works too.
  window.uiAssistPost = function(url) {
    return function(req, done, progress, signal) {
      fetch(url, {
        method: 'POST', cache: 'no-store', credentials: 'same-origin',
        headers: {'Content-Type': 'application/json', 'Accept': 'application/x-ndjson'},
        body: JSON.stringify(req), signal: signal,
      }).then(function(r) {
        if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
        var final = null, buf = '';
        function line(s) {
          s = s.trim();
          if (!s) return;
          var m;
          try { m = JSON.parse(s); } catch (e) { return; }
          if (m && m.progress != null && m.reply == null && m.error == null) { progress(m.progress); return; }
          final = m;
        }
        if (!r.body || !r.body.getReader) {
          return r.text().then(function(t) { t.split('\n').forEach(line); return final; });
        }
        var reader = r.body.getReader(), dec = new TextDecoder();
        function pump() {
          return reader.read().then(function(x) {
            if (x.done) { line(buf); return final; }
            buf += dec.decode(x.value, {stream: true});
            var parts = buf.split('\n');
            buf = parts.pop();
            parts.forEach(line);
            return pump();
          });
        }
        return pump();
      }).then(function(res) {
        if (!res) { done(null, 'no answer came back'); return; }
        if (res.error) { done(null, res.error); return; }
        done(res);
      }).catch(function(e) {
        if (e && e.name === 'AbortError') { done(null, 'Stopped.'); return; }
        done(null, (e && e.message) || String(e));
      });
    };
  };

  // uiOpenAssist(opts)
  //   title     — modal title
  //   subtitle  — one-line context under it (the field's help, usually)
  //   initial   — the text to open with
  //   placeholder — the draft's placeholder
  //   send      — fn({message, draft, history, mode}, done, progress,
  //               signal). Call done({reply, value, options}) on success —
  //               value omitted or empty means the model answered without
  //               proposing a change; options, when given, are answers to
  //               the reply's question, drawn as buttons that send the one
  //               picked — or done(null, "error text") on failure.
  //   url       — instead of send: an endpoint speaking the core assist
  //               protocol (uiAssistPost).
  //   web       — offer "Look things up on the web" (an endpoint that can).
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
  //               mode rides on every send so it knows which to use).
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
    var tabs = assistTabs(wrap, 'Draft');
    // Talking it through starts on the conversation; revising text that is
    // already there starts on that text.
    tabs.show(discuss || versions[0].trim() === '' ? 'chat' : 'draft');
    backBtn.addEventListener('click', function() { showVersion(idx - 1); });
    fwdBtn.addEventListener('click', function() { showVersion(idx + 1); });
    updateNav();

    var chat = window.uiAssistChat({
      discuss: discuss, switchable: switchable, web: !!opts.web,
      intro: discuss
        ? assistDiscussNote
        : (versions[0].trim() === ''
          ? 'Nothing written yet. Describe what you want and it will draft one.'
          : 'Ask for a change to the draft, or a question about it.'),
      send: opts.send || window.uiAssistPost(opts.url),
      extra: function() { return {draft: draft.value}; },
      onResult: function(res) {
        var value = res.value == null ? '' : String(res.value);
        if (value === '' || value === versions[idx]) return '';
        versions.push(value);
        showVersion(versions.length - 1);
        // Looking at the conversation: the reply stays in view, and the
        // Draft tab is marked so the new version is not missed.
        tabs.markNew();
        return 'Updated the draft.';
      },
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
        wrap.appendChild(tabs.row);

        var left = el('div', {class: 'ui-assist-left'});
        var navRow = el('div', {class: 'ui-assist-navrow'});
        navRow.appendChild(backBtn);
        navRow.appendChild(verLabel);
        navRow.appendChild(fwdBtn);
        left.appendChild(navRow);
        left.appendChild(draft);

        wrap.appendChild(left);
        wrap.appendChild(chat.right);
        body.appendChild(wrap);
        setTimeout(function() {
          chat.focus();
          // The opening request, if the caller had one. Sent through the
          // same path a typed one takes, so it appears in the log, joins
          // the history, and produces a version like any other — the
          // person can walk back to the original from it.
          var ask = String(opts.ask == null ? '' : opts.ask).trim();
          if (ask) chat.say(ask);
        }, 0);
      },
    });
  };

  // assistTabs is the Conversation | <work> pair a narrow screen shows, the
  // two sides taking turns. markNew flags the work's tab when something
  // changes there while the conversation is showing.
  function assistTabs(wrap, workLabel) {
    var chatTab = el('button', {type: 'button', class: 'ui-tab', role: 'tab'}, ['Conversation']);
    var draftTab = el('button', {type: 'button', class: 'ui-tab', role: 'tab'}, [workLabel]);
    function show(side) {
      wrap.classList.toggle('ui-assist-on-chat', side === 'chat');
      wrap.classList.toggle('ui-assist-on-draft', side === 'draft');
      chatTab.classList.toggle('active', side === 'chat');
      draftTab.classList.toggle('active', side === 'draft');
      chatTab.setAttribute('aria-selected', side === 'chat' ? 'true' : 'false');
      draftTab.setAttribute('aria-selected', side === 'draft' ? 'true' : 'false');
      if (side === 'draft') draftTab.classList.remove('ui-assist-tab-new');
    }
    chatTab.addEventListener('click', function() { show('chat'); });
    draftTab.addEventListener('click', function() { show('draft'); });
    var row = el('div', {class: 'ui-assist-tabs', role: 'tablist'});
    row.appendChild(chatTab);
    row.appendChild(draftTab);
    return {
      row: row, show: show,
      markNew: function() { if (wrap.classList.contains('ui-assist-on-chat')) draftTab.classList.add('ui-assist-tab-new'); },
    };
  }

  // --- the form helper ---------------------------------------------------
  //
  // Help with a form, swapped in where the form is rather than squeezed
  // beside it: one thing at a time, the same on a phone as on a desk. The
  // helper shows the conversation and a small version of the form, each
  // field and what it holds, the ones it just changed lit and the required
  // ones still empty marked; Back to the form swaps the form in again, with
  // what it filled marked until the person touches it, and a bar to undo
  // that or (where the form saves as you type) save it. Nothing is saved by
  // the conversation.
  //
  // Two ways in: the form's own ✨ Get assistance (a wizard, or a form with
  // FormPanel.Assist), which talks to the framework's form help or the
  // form's AssistURL; and ui.AssistPanel, a form opened with the helper
  // already on it and a request already sent (System Hooks' Fix).

  var FORM_ASSIST_URL = '/api/assist/form';
  var assistAvail = {};

  // uiFormAssistAvailable(url) is whether the endpoint has a worker to help,
  // asked once a page.
  window.uiFormAssistAvailable = function(url) {
    url = url || FORM_ASSIST_URL;
    if (!assistAvail[url]) {
      assistAvail[url] = fetchJSON(url).then(function(d) { return !!(d && d.available); }, function() { return false; });
    }
    return assistAvail[url];
  };

  // uiFormAssist(formEl) swaps the helper in for a form panel, making it the
  // first time and keeping its conversation after. A form with an opening
  // request (FormPanel.AssistAsk) sends it the first time, in place of the
  // starters.
  window.uiFormAssist = function(formEl) {
    if (!formEl || !formEl.uiForm) return;
    var ask = '';
    if (!formEl.__assist) {
      var cfg = formEl.uiForm.cfg || {};
      ask = String(cfg.assist_ask == null ? '' : cfg.assist_ask).trim();
      formEl.__assist = assistHelper(formEl, {url: cfg.assist_url || FORM_ASSIST_URL, web: true, starters: !ask,
        attach: !!cfg.assist_attach});
    }
    formEl.__assist.open();
    if (ask) formEl.__assist.say(ask);
  };

  // assistPageContext is where a form is, in the page's own words: the
  // dialog or section around it, and the page.
  function assistPageContext(node) {
    var parts = [];
    function text(n, sel) {
      var x = n && n.querySelector && n.querySelector(sel);
      return x ? String(x.textContent || '').trim() : '';
    }
    for (var n = node; n; n = n.parentNode) {
      var c = ' ' + (n.className || '') + ' ';
      if (c.indexOf(' ui-modal-box ') >= 0) {
        var mt = text(n, '.ui-modal-title'), ms = text(n, '.ui-modal-sub');
        if (mt) parts.push('Dialog: ' + mt + (ms ? ' - ' + ms : ''));
      } else if (c.indexOf(' ui-section ') >= 0) {
        var st = text(n, '.ui-section-h'), ss = text(n, '.ui-section-sub');
        if (st) parts.push('Section: ' + st + (ss ? ' - ' + ss : ''));
      }
    }
    if (typeof document !== 'undefined' && document.title) parts.push('Page: ' + document.title);
    return parts.join('\n');
  }

  // describeForm is the form as the model is told it: each field it may
  // see, with its help, whether it is required, and whether it is a secret.
  function describeForm(cfg) {
    var fields = Array.isArray(cfg.fields) ? cfg.fields : [];
    if (Array.isArray(cfg.steps) && cfg.steps.length) {
      fields = cfg.steps.reduce(function(acc, st) { return acc.concat(st.fields || []); }, []);
    }
    function describe(f) {
      var d = {field: f.field, label: f.label || '', type: f.type || 'text'};
      if (f.help) d.help = f.help;
      if (f.detail) d.detail = f.detail;
      if (f.required) d.required = true;
      if (f.type === 'password' || f.secret) d.secret = true;
      if (Array.isArray(f.options) && f.options.length) {
        d.options = f.options.map(function(o) { return (o && typeof o === 'object') ? String(o.value) : String(o); });
      }
      if (Array.isArray(f.columns) && f.columns.length) d.columns = f.columns.map(describe);
      return d;
    }
    return fields.filter(function(f) {
      return f && f.field && f.type !== 'header' && f.type !== 'hidden' && !f.read_only;
    }).map(describe);
  }

  // assistShort is a value as the small version of the form shows it.
  function assistShort(d, v) {
    if (d.secret) return (v != null && v !== '') ? 'set' : '';
    if (v == null || v === '') return '';
    if (Array.isArray(v)) {
      var col = d.columns && d.columns[0] && d.columns[0].field;
      return v.map(function(r) { return (r && typeof r === 'object') ? String(r[col] == null ? '' : r[col]) : String(r); })
        .filter(Boolean).join(' · ');
    }
    if (typeof v === 'boolean') return v ? 'on' : 'off';
    if (typeof v === 'object') return JSON.stringify(v);
    return String(v);
  }

  // assistHelper makes the helper for a form panel: inserted beside it,
  // swapped with it by open() and close().
  //   opts: url, web, attach, intro, placeholder, starters
  function assistHelper(formEl, opts) {
    var f = formEl.uiForm;
    var parent = formEl.parentNode;
    var formDoc = describeForm(f.cfg || {});
    var byField = {};
    formDoc.forEach(function(d) { byField[d.field] = d; });
    var lastChanged = {};
    var before = null; // the values before its first fill, for Undo

    function shownValues() {
      var v = f.values(), out = {};
      formDoc.forEach(function(d) {
        out[d.field] = d.secret ? ((v[d.field] != null && v[d.field] !== '') ? '(set)' : '(empty)') : v[d.field];
      });
      return out;
    }

    // The small version of the form.
    var mini = el('div', {class: 'ui-assist-mini'});
    function drawMini() {
      mini.innerHTML = '';
      var v = f.values();
      mini.appendChild(el('div', {class: 'ui-assist-mini-h'}, ['The form']));
      formDoc.forEach(function(d) {
        var val = assistShort(d, v[d.field]);
        var cls = 'ui-assist-mini-row';
        if (lastChanged[d.field]) cls += ' new';
        if (d.required && val === '') cls += ' needed';
        mini.appendChild(el('div', {class: cls}, [
          el('span', {class: 'ui-assist-mini-k'}, [d.label || d.field]),
          el('span', {class: 'ui-assist-mini-v'}, [val || (d.required ? 'needed' : '-')]),
        ]));
      });
    }

    var backBtn = el('button', {type: 'button', class: 'ui-row-btn compact'}, ['Back to the form']);
    var helperHead = el('div', {class: 'ui-assist-helper-h'}, [
      el('span', {class: 'ui-assist-helper-t'}, ['✨ Assistance']), backBtn]);
    var chat = window.uiAssistChat({
      discuss: false, switchable: false, web: !!opts.web, attach: !!opts.attach,
      intro: opts.intro || 'Ask what any of it means, or say what you are setting up and it will help you fill it in. What it suggests goes into the form, marked, for you to check; nothing is saved until you save it.',
      writeNote: 'Each request can fill the form right away.',
      placeholders: {write: opts.placeholder || 'Ask about the form, or say what you want…'},
      starters: opts.starters ? ['Help me fill this in', 'What is this for?'] : null,
      send: window.uiAssistPost(opts.url),
      extra: function() { return {form: formDoc, values: shownValues(), page: assistPageContext(formEl)}; },
      onResult: function(res) {
        var got = res.fields && typeof res.fields === 'object' ? res.fields : null;
        if (!got) return '';
        var now = f.values(), next = JSON.parse(JSON.stringify(now)), changed = [];
        Object.keys(got).forEach(function(k) {
          var d = byField[k];
          if (!d || d.secret) return;
          if (JSON.stringify(now[k]) === JSON.stringify(got[k])) return;
          next[k] = got[k];
          changed.push(k);
        });
        if (!changed.length) return '';
        if (!before) before = now;
        lastChanged = {};
        changed.forEach(function(k) { lastChanged[k] = true; });
        f.fill(next, changed);
        drawMini();
        drawBar();
        return 'Filled in ' + changed.map(function(k) { return byField[k].label || k; }).join(', ') + '. Back to the form to check it.';
      },
    });
    var body = el('div', {class: 'ui-assist-helper-b'}, [mini, chat.right]);
    var helper = el('div', {class: 'ui-assist-helper'}, [helperHead, body]);

    // The bar over the form once the helper exists: back to it, and what it
    // filled, with Undo and (where the form saves as you type) Save.
    var bar = el('div', {class: 'ui-assist-bar'});
    function drawBar() {
      bar.innerHTML = '';
      var n = f.filled().length;
      bar.appendChild(el('span', {class: 'ui-assist-bar-t'}, [n
        ? '✨ It filled ' + n + (n === 1 ? ' field' : ' fields') + ', marked below' + (f.autosave ? ': not saved yet.' : ': check them, then save.')
        : '✨ Assistance']));
      if (n && f.autosave) {
        var save = el('button', {type: 'button', class: 'ui-row-btn compact primary'}, ['Save what it filled in']);
        save.addEventListener('click', function() { f.saveFilled(f.filled()); before = null; drawBar(); });
        bar.appendChild(save);
      }
      if (n && before) {
        var undo = el('button', {type: 'button', class: 'ui-row-btn compact'}, ['Undo']);
        undo.addEventListener('click', function() {
          f.fill(before, []);
          f.unmark();
          before = null;
          lastChanged = {};
          drawBar();
        });
        bar.appendChild(undo);
      }
      var again = el('button', {type: 'button', class: 'ui-row-btn compact'}, ['Back to the helper']);
      again.addEventListener('click', open);
      bar.appendChild(again);
    }

    function open() {
      bar.style.display = 'none';
      formEl.style.display = 'none';
      helper.style.display = '';
      drawMini();
      chat.focus();
    }
    function close() {
      helper.style.display = 'none';
      formEl.style.display = '';
      drawBar();
      bar.style.display = '';
    }
    backBtn.addEventListener('click', close);
    // A submit that went through ends the help: the form has done its job.
    f.onSaved = function() {
      helper.style.display = 'none';
      bar.style.display = 'none';
      formEl.style.display = '';
      before = null;
    };
    helper.style.display = 'none';
    bar.style.display = 'none';
    parent.insertBefore(bar, formEl);
    parent.insertBefore(helper, formEl);
    return {open: open, close: close, say: chat.say, helper: helper, bar: bar};
  }

  // assist_panel (ui.AssistPanel): a form opened with the helper on it, and
  // the request it was opened for already sent.
  components.assist_panel = function(cfg, ctx) {
    if (cfg.walk && cfg.walk.source) return assistWalk(cfg, ctx);
    return assistPanel(cfg, ctx);
  };

  // assistWalk is an assist panel over each record of a list in turn (Walk):
  // "2 of 3: <the record>" with Previous and Next, and the panel for the
  // record shown, its {field} placeholders filled from it. Saving the form
  // moves on to the next, and from the last closes the dialog. Moving off a
  // record leaves its conversation: each record is its own.
  function assistWalk(cfg, ctx) {
    var walk = cfg.walk;
    var wrap = el('div', {class: 'ui-assist-walk'});
    var head = el('div', {class: 'ui-assist-walk-head'});
    var body = el('div', {class: 'ui-assist-walk-body'});
    wrap.appendChild(head);
    wrap.appendChild(body);
    var items = [], at = 0;
    if (window.uiLoading) head.appendChild(window.uiLoading());
    fetchJSON(walk.source).then(function(d) {
      items = Array.isArray(d) ? d : ((d && (d.records || d.items)) || []);
      show(0);
    }).catch(function(err) { head.textContent = 'Failed to load: ' + ((err && err.message) || err); });
    function fill(obj, rec) {
      if (!obj || typeof obj !== 'object') return;
      for (var k in obj) {
        if (typeof obj[k] === 'string' && obj[k].indexOf('{') >= 0) obj[k] = substitute(obj[k], rec, true);
        else if (typeof obj[k] === 'object') fill(obj[k], rec);
      }
    }
    function show(i) {
      head.innerHTML = '';
      body.innerHTML = '';
      if (!items.length) {
        body.appendChild(el('div', {class: 'ui-assist-walk-empty'}, [walk.empty || 'Nothing to work on.']));
        return;
      }
      at = Math.max(0, Math.min(i, items.length - 1));
      var rec = items[at];
      var prev = el('button', {type: 'button', class: 'ui-row-btn compact'}, ['‹ Previous']);
      var next = el('button', {type: 'button', class: 'ui-row-btn compact'}, ['Next ›']);
      prev.disabled = at <= 0;
      next.disabled = at >= items.length - 1;
      prev.addEventListener('click', function() { show(at - 1); });
      next.addEventListener('click', function() { show(at + 1); });
      head.appendChild(el('span', {class: 'ui-assist-walk-at'}, [(at + 1) + ' of ' + items.length + ': ' + String(rec[walk.label] == null ? '' : rec[walk.label])]));
      head.appendChild(prev);
      head.appendChild(next);
      var one = JSON.parse(JSON.stringify(cfg));
      delete one.walk;
      fill(one, rec);
      var childCtx = {};
      for (var k in (ctx || {})) childCtx[k] = ctx[k];
      for (var k2 in rec) childCtx[k2] = rec[k2];
      // The form's save moves on, rather than closing the dialog mid-walk.
      childCtx.__closeModal = function() {
        if (at < items.length - 1) show(at + 1);
        else if (ctx && ctx.__closeModal) ctx.__closeModal();
      };
      body.appendChild(assistPanel(one, childCtx));
    }
    return wrap;
  }

  function assistPanel(cfg, ctx) {
    var wrap = el('div', {class: 'ui-assist-panel'});
    mountComponent(cfg.work || null, wrap, ctx);
    // The helper goes on once the form has its record: the opening request
    // travels with the values, and they have to be the real ones.
    var tries = 0;
    (function attach() {
      var formEl = wrap.querySelector && wrap.querySelector('.ui-form');
      if (!formEl || !formEl.uiForm || !formEl.uiForm.loaded) {
        if (tries++ < 200) setTimeout(attach, 50);
        return;
      }
      var h = assistHelper(formEl, {url: cfg.url, web: !!cfg.web, attach: !!cfg.attach,
        intro: cfg.intro, placeholder: cfg.placeholder});
      formEl.__assist = h;
      h.open();
      var ask = String(cfg.ask == null ? '' : cfg.ask).trim();
      if (ask) h.say(ask);
    })();
    return wrap;
  }
