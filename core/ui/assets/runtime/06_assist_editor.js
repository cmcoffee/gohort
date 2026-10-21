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

    var log = el('div', {class: 'ui-assist-log'});
    function addMsg(role, text, isHTML) {
      var m = el('div', {class: 'ui-assist-msg ' + role});
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
      if (!text) return;
      if (choices) { choices.remove(); choices = null; }
      composer.value = '';
      addMsg('user', text);
      // Snapshot the history BEFORE this turn — the server appends the
      // new message itself, so sending it in both places would duplicate.
      var priorHistory = history.slice();
      history.push({role: 'user', content: text});
      var turn = {ctl: (typeof AbortController !== 'undefined') ? new AbortController() : null};
      setBusy(turn);
      var pending = pendingLine();
      var req = {message: text, history: priorHistory, mode: discuss ? 'discuss' : 'write'};
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
      opts.send(req, finish, function(text) { if (!settled) pending.doing(text); }, turn.ctl ? turn.ctl.signal : undefined);
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
    field.appendChild(composer);
    field.appendChild(sendBtn);
    composerRow.appendChild(field);
    right.appendChild(composerRow);
    if (webRow) right.appendChild(webRow);

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

  // assist_panel (ui.AssistPanel): any component on the left, a form
  // usually, and the conversation on the right. The model is told the
  // form's fields and what they hold, and what it proposes fills them, as a
  // version: ◀ ▶ walk back to what was there. Nothing is saved by the
  // conversation; the form's own Save keeps what is in it.
  components.assist_panel = function(cfg, ctx) {
    var wrap = el('div', {class: 'ui-assist ui-assist-panel'});
    var left = el('div', {class: 'ui-assist-left ui-assist-work'});
    var host = el('div', {class: 'ui-assist-work-body'});
    var work = cfg.work || null;
    mountComponent(work, host, ctx);

    // The form the work holds, found once it has rendered: a form panel
    // exposes {values(), fill(map)} on its element.
    function form() {
      var f = host.querySelector && host.querySelector('.ui-form');
      return (f && f.uiForm) || null;
    }
    var fields = (work && Array.isArray(work.fields)) ? work.fields : [];
    if (work && Array.isArray(work.steps)) {
      fields = work.steps.reduce(function(acc, st) { return acc.concat(st.fields || []); }, []);
    }
    function describe(f) {
      var d = {field: f.field, label: f.label || '', type: f.type || 'text'};
      if (f.help) d.help = f.help;
      if (f.detail) d.detail = f.detail;
      if (Array.isArray(f.options) && f.options.length) {
        d.options = f.options.map(function(o) { return (o && typeof o === 'object') ? String(o.value) : String(o); });
      }
      if (Array.isArray(f.columns) && f.columns.length) d.columns = f.columns.map(describe);
      return d;
    }
    var formDoc = fields.filter(function(f) {
      return f && f.field && f.type !== 'header' && f.type !== 'hidden' && !f.read_only;
    }).map(describe);

    // The version walk, over the form's values.
    var versions = null, idx = 0;
    var backBtn = el('button', {type: 'button', class: 'ui-assist-nav', title: 'Previous version'}, ['◀']);
    var fwdBtn = el('button', {type: 'button', class: 'ui-assist-nav', title: 'Next version'}, ['▶']);
    var verLabel = el('span', {class: 'ui-assist-ver'});
    var navRow = el('div', {class: 'ui-assist-navrow'}, [backBtn, verLabel, fwdBtn]);
    function updateNav() {
      var n = versions ? versions.length : 1;
      navRow.style.display = n > 1 ? '' : 'none';
      backBtn.disabled = idx <= 0;
      fwdBtn.disabled = idx >= n - 1;
      verLabel.textContent = 'v' + (idx + 1) + ' of ' + n + (idx === 0 ? ' (as it was)' : '');
    }
    function showVersion(i) {
      var f = form();
      if (!f || !versions || i < 0 || i >= versions.length) return;
      versions[idx] = f.values(); // keep what was typed into this one
      idx = i;
      f.fill(versions[idx]);
      updateNav();
    }
    backBtn.addEventListener('click', function() { showVersion(idx - 1); });
    fwdBtn.addEventListener('click', function() { showVersion(idx + 1); });
    updateNav();

    var tabs = assistTabs(wrap, cfg.work_label || 'Form');
    var chat = window.uiAssistChat({
      discuss: false, switchable: false, web: !!cfg.web,
      intro: cfg.intro || 'Ask about it, or say what to change. What it suggests fills the form; nothing is saved until you save it.',
      writeNote: 'Each request can fill the form right away.',
      placeholders: {write: cfg.placeholder || 'What is wrong with it?'},
      send: window.uiAssistPost(cfg.url),
      extra: function() {
        var f = form();
        return {form: formDoc, values: f ? f.values() : {}};
      },
      onResult: function(res) {
        var f = form();
        var got = res.fields && typeof res.fields === 'object' ? res.fields : null;
        if (!f || !got || !Object.keys(got).length) return '';
        var now = f.values();
        if (!versions) versions = [now];
        else versions[idx] = now;
        var next = JSON.parse(JSON.stringify(now));
        Object.keys(got).forEach(function(k) { next[k] = got[k]; });
        versions.push(next);
        idx = versions.length - 1;
        f.fill(next);
        updateNav();
        tabs.markNew();
        return 'Filled in the form.';
      },
    });
    tabs.show('chat');

    left.appendChild(navRow);
    left.appendChild(host);
    wrap.appendChild(tabs.row);
    wrap.appendChild(left);
    wrap.appendChild(chat.right);
    var ask = String(cfg.ask == null ? '' : cfg.ask).trim();
    // The opening request waits for the form: it travels with the values,
    // and the form loads its record first.
    var tries = 0;
    (function start() {
      if (!ask) { chat.focus(); return; }
      if (!form() && tries++ < 40) { setTimeout(start, 50); return; }
      chat.say(ask);
    })();
    return wrap;
  };
