  components.pipeline_panel = function(cfg) {
    var idF     = cfg.session_id_field    || 'ID';
    var titleF  = cfg.session_title_field || 'Title';
    var dateF   = cfg.session_date_field  || 'Date';
    var blocksF = cfg.session_blocks_field || 'Blocks';

    var currentSessionId = '';
    var blockEls = {};       // block id -> DOM element { wrap, body, raw }
    var liveVerdictBid = null; // most-recent verdict id (for auto-scroll on done)
    var activeStream = null; // AbortController for in-flight submit

    var wrap = el('div', {class: 'ui-pl ui-chat'});
    var side = el('div', {class: 'ui-chat-side'});
    var bulkSelected = {};
    var bulkState    = {mode: false};

    var sideSelectBtn = null;
    if (cfg.bulk_select) {
      sideSelectBtn = el('button', {
        class: 'ui-chat-side-btn', title: 'Tap items to select multiple',
        onclick: function() {
          bulkState.mode = !bulkState.mode;
          if (!bulkState.mode) Object.keys(bulkSelected).forEach(function(k){ delete bulkSelected[k]; });
          sideSelectBtn.classList.toggle('active', bulkState.mode);
          sideSelectBtn.textContent = bulkState.mode ? '✓ Selecting' : 'Select';
          loadSessions();
        },
      }, ['Select']);
    }
    var sideHdrBuilt = renderSideHeader({
      label: 'Sessions',
      newTitle: 'New run',
      onNew:    function(){ openSession(null); },
      onClose:  function(){ closeDrawer(); },
      leftExtras: sideSelectBtn ? [sideSelectBtn] : [],
    });
    var sideHdr  = sideHdrBuilt.elt;
    var sideList = el('div', {class: 'ui-chat-side-list'}, ['Loading…']);
    var sideSearch = makeSideSearch(sideList);
    side.appendChild(sideHdr);
    side.appendChild(sideSearch);
    side.appendChild(sideList);

    var main = el('div', {class: 'ui-chat-main'});
    var drawer = makeDrawer(side, {
      title:          'New run',
      hamburgerTitle: 'Sessions',
    });
    var mobileTitle    = drawer.mobileTitle;
    var drawerBackdrop = drawer.backdrop;
    var openDrawer     = drawer.openDrawer;
    var closeDrawer    = drawer.closeDrawer;
    main.appendChild(drawer.mobileHdr);

    // Submit form section. Each field becomes an input in field-order.
    var form = el('div', {class: 'ui-pl-form'});
    var formInputs = {}; // field name -> input element
    var toggleRows = []; // toggle fields stash — appended to formActions
    (cfg.fields || []).forEach(function(f) {
      var row = el('div', {class: 'ui-pl-formrow'});
      if (f.label) row.appendChild(el('label', {class: 'ui-pl-formlabel'}, [f.label]));
      var inp;
      if (f.type === 'textarea') {
        inp = el('textarea', {
          class: 'ui-pl-input ui-pl-textarea',
          rows: String(f.rows || 3),
          placeholder: f.placeholder || '',
        });
      } else if (f.type === 'select') {
        inp = el('select', {class: 'ui-pl-input'});
        (f.options || []).forEach(function(opt) { inp.appendChild(el('option', {value: opt}, [opt])); });
      } else if (f.type === 'number') {
        inp = el('input', {
          type: 'number', class: 'ui-pl-input',
          min: typeof f.min !== 'undefined' ? String(f.min) : '',
          max: typeof f.max !== 'undefined' ? String(f.max) : '',
          placeholder: f.placeholder || '',
        });
      } else if (f.type === 'toggle') {
        // Compact inline toggle. Renders as a pill-shaped row with
        // a small switch + tight label, sitting flush-left in the
        // form column rather than stretching across it. Keeps
        // optional flags (email-on-done, "draft mode", etc.) from
        // dominating a form whose primary input is a textarea.
        inp = el('input', {type: 'checkbox', class: 'ui-switch ui-switch-sm'});
        inp.checked = !!f.default;
        row.classList.add('ui-pl-formrow-toggle');
        var firstChild = row.firstChild;
        if (firstChild && firstChild.classList && firstChild.classList.contains('ui-pl-formlabel')) {
          firstChild.style.marginBottom = '0';
        }
      } else if (f.type === 'file') {
        // File picker with SERVER-SIDE extraction. On selection the file
        // is multipart-POSTed to f.upload_url; the JSON reply {text,title}
        // fills f.upload_target (so the user reviews the extracted text
        // before submitting) and a "title" field when one exists. The
        // file input never contributes to the submit body itself.
        inp = el('input', {type: 'file', class: 'ui-pl-input ui-pl-file', accept: f.accept || ''});
        var fileStatus = el('div', {class: 'ui-pl-file-status'});
        inp.addEventListener('change', function() {
          var file = inp.files && inp.files[0];
          if (!file) return;
          if (!f.upload_url) { showToast('No upload endpoint configured'); return; }
          var fd = new FormData();
          fd.append('file', file, file.name);
          fileStatus.textContent = 'Extracting text from ' + file.name + '…';
          fileStatus.classList.remove('is-error');
          fetch(f.upload_url, {method: 'POST', body: fd}).then(function(r) {
            if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
            return r.json();
          }).then(function(d) {
            var tgt = f.upload_target && formInputs[f.upload_target];
            if (tgt) tgt.value = (d && d.text) || '';
            if (d && d.title && formInputs['title'] && !formInputs['title'].value) {
              formInputs['title'].value = d.title;
            }
            // Optionally flip a sibling field to a constant (e.g. set a
            // "source" select to "upload") now that a file was used.
            if (f.upload_set_field && formInputs[f.upload_set_field]) {
              formInputs[f.upload_set_field].value = f.upload_set_value || '';
            }
            var n = tgt ? String(tgt.value).length : 0;
            fileStatus.textContent = 'Extracted ' + n + ' characters — review below, then submit.';
          }).catch(function(err) {
            fileStatus.textContent = 'Extraction failed: ' + err.message;
            fileStatus.classList.add('is-error');
          });
        });
        row.appendChild(inp);
        row.appendChild(fileStatus);
        formInputs[f.name] = inp;
        form.appendChild(row);
        return; // fully handled — skip the shared append/registration below
      } else {
        // Honor HTML5 input types beyond plain text (email, tel,
        // url) — gives mobile users the right keyboard and lets
        // the browser do basic validation. Unknown values fall
        // back to "text".
        var htmlType = 'text';
        if (f.type === 'email' || f.type === 'tel' || f.type === 'url' || f.type === 'password') {
          htmlType = f.type;
        }
        inp = el('input', {type: htmlType, class: 'ui-pl-input', placeholder: f.placeholder || ''});
      }
      if (f.default) inp.value = f.default;
      formInputs[f.name] = inp;
      row.appendChild(inp);
      // Toggles land in the formActions bar (right-aligned) instead
      // of the form column so an "Email me when done" pill sits
      // beside the Start button rather than burning vertical space
      // above it. The row is held in a sidecar slot and appended
      // after formActions is built below.
      if (f.type === 'toggle') {
        toggleRows.push(row);
      } else {
        form.appendChild(row);
      }
    });
    var formActions = el('div', {class: 'ui-pl-formactions'});
    var prefillBtn = null;
    if (cfg.prefill_url) {
      var target = cfg.prefill_target || (function() {
        for (var i = 0; i < (cfg.fields || []).length; i++) {
          if (cfg.fields[i].type === 'textarea') return cfg.fields[i].name;
        }
        return cfg.fields && cfg.fields[0] && cfg.fields[0].name;
      })();
      // Popover that hosts an array of suggestions to pick from.
      // Hidden until a list response lands; click any item to fill
      // the target input. Click outside to dismiss.
      var prefillMenu = el('div', {class: 'ui-pl-prefill-menu', style: 'display:none'});
      var prefillWrap = el('div', {class: 'ui-pl-prefill-wrap'});
      prefillBtn = el('button', {
        class: 'ui-pl-btn ui-pl-prefill-btn',
        onclick: function() {
          var orig = prefillBtn.textContent;
          prefillBtn.textContent = '…';
          prefillBtn.disabled = true;
          // Default GET; apps that need a POST body declare
          // prefill_method + prefill_body so the runtime doesn't
          // need an app-specific wrapper endpoint.
          var fetchOpts = {};
          if ((cfg.prefill_method || 'GET').toUpperCase() === 'POST') {
            fetchOpts.method = 'POST';
            fetchOpts.headers = {'Content-Type': 'application/json'};
            fetchOpts.body = cfg.prefill_body || '{}';
          }
          fetch(cfg.prefill_url, fetchOpts).then(function(r) {
            if (!r.ok) throw new Error('HTTP ' + r.status);
            return r.text();
          }).then(function(text) {
            var t = String(text || '').trim();
            var arr = null;
            // Array response → show popover. Object → look for
            // common single-suggestion keys. String → direct fill.
            try {
              var j = JSON.parse(t);
              if (Array.isArray(j)) arr = j;
              else if (j && typeof j === 'object') {
                t = String(j.topic || j.text || j.suggestion || '').trim();
              }
            } catch (_) {}
            if (arr && arr.length) {
              prefillMenu.innerHTML = '';
              // Field overrides let apps point at their own response
              // shape (one app returns {question, hook}; another
              // returns {topic, text}; etc.). Without overrides,
              // {topic|text|question} fall through as the value and
              // {hook|description|summary} as the muted second line.
              var qField = cfg.prefill_list_question_field || '';
              var hField = cfg.prefill_list_hook_field || '';
              arr.forEach(function(s) {
                var label, hook;
                if (typeof s === 'string') {
                  label = s;
                } else {
                  label = qField ? s[qField] : (s.topic || s.text || s.question);
                  hook  = hField ? s[hField] : (s.hook || s.description || s.summary);
                }
                if (!label) return;
                var children = [el('div', {class: 'ui-pl-prefill-item-q'}, [label])];
                if (hook) children.push(el('div', {class: 'ui-pl-prefill-item-hook'}, [hook]));
                var fillValue = label;
                var item = el('div', {class: 'ui-pl-prefill-item', onclick: function() {
                  if (target && formInputs[target]) formInputs[target].value = fillValue;
                  prefillMenu.style.display = 'none';
                }}, children);
                prefillMenu.appendChild(item);
              });
              prefillMenu.style.display = '';
              return;
            }
            if (t && target && formInputs[target]) formInputs[target].value = t;
          }).catch(function(err) { showToast('Suggest failed: ' + err.message); }).then(function() {
            prefillBtn.textContent = orig;
            prefillBtn.disabled = false;
          });
        },
      }, [
        // AI sparkle icon — span keeps the icon styled distinctly
        // from the label and lets CSS target it without touching
        // the label text the app provides.
        el('span', {class: 'ui-pl-prefill-icon'}, ['✨']),
        ' ',
        cfg.prefill_label || 'Suggest',
      ]);
      prefillWrap.appendChild(prefillBtn);
      prefillWrap.appendChild(prefillMenu);
      formActions.appendChild(prefillWrap);
      // Dismiss on outside click.
      document.addEventListener('click', function(ev) {
        if (prefillMenu.style.display === 'none') return;
        if (!prefillWrap.contains(ev.target)) prefillMenu.style.display = 'none';
      });
    }
    var submitBtn = el('button', {class: 'ui-pl-btn primary', onclick: function(){ doSubmit(); }}, [cfg.submit_label || 'Start']);
    // Cancel button stays in the top action bar (defined below) so
    // it remains visible during a live run when the form is hidden.
    // ui-pl-btn-cancel uses margin-left:auto so it sits on the far
    // right of the actions toolbar regardless of how many action
    // buttons sit on the left.
    var cancelBtn = el('button', {class: 'ui-pl-btn danger ui-pl-btn-cancel', style: 'display:none', onclick: function(){ doCancel(); }}, ['Cancel']);
    // Toggle pills land on the far LEFT of the actions row,
    // opposite the Suggest + Start buttons. Achieved by prepending
    // toggle rows and giving the first one margin-right:auto so it
    // pushes the rest of the row content (Suggest, Start) to the
    // right edge that flex-end already targets.
    toggleRows.forEach(function(r, i) {
      if (i === 0) r.style.marginRight = 'auto';
      formActions.insertBefore(r, formActions.firstChild);
    });
    formActions.appendChild(submitBtn);
    form.appendChild(formActions);

    // Per-session actions toolbar — visible when a session is loaded
    // (live or saved) OR when a run is in flight (Cancel pinned to
    // the right of the toolbar so the user can abort even when the
    // form is hidden). renderActions repopulates it on each call;
    // cancelBtn is added here so it's reachable for an early submit
    // before the first 'session' SSE event triggers renderActions.
    var actionsBar = el('div', {class: 'ui-pl-actions', style: 'display:none'});
    actionsBar.appendChild(cancelBtn);
    // Session-title strip — shows the loaded topic immediately under
    // the actions toolbar so users always know which run they're
    // looking at without scrolling. Hidden until a session loads.
    var sessionTitle = el('div', {class: 'ui-pl-session-title', style: 'display:none'});

    var transcript = el('div', {class: 'ui-pl-transcript'});
    var emptyHint = el('div', {class: 'ui-chat-empty', style: 'padding:1rem'},
      [cfg.empty_text || 'Submit a topic to begin, or pick one from the sidebar.']);
    transcript.appendChild(emptyHint);

    main.appendChild(form);
    main.appendChild(actionsBar);
    main.appendChild(sessionTitle);
    main.appendChild(transcript);

    function setSessionTitle(text) {
      if (text && String(text).trim()) {
        sessionTitle.textContent = text;
        sessionTitle.style.display = '';
      } else {
        sessionTitle.style.display = 'none';
      }
    }

    // openStreamModal creates a centered modal overlay and streams
    // an SSE response into its body. Designed for "Generate Report"
    // style flows where the user wants the result on top of the
    // current page rather than replacing the transcript. Recognizes
    // the legacy report-stream event shape (report_header, _stream,
    // _replace, _status, _done) so handleReportSSE works unchanged.
    // openRelatedPopover wires a "Related" toolbar action — POSTs
    // [currentId] to the URL, expects a JSON response of either
    // {suggestions: [{question, reason?}, ...]} OR a bare array of
    // {question, reason?} objects. Shows a clickable popover; pick
    // an entry → fills the form's first textarea with the question
    // and submits it as a new run. This is the framework analog of
    // the legacy suggest panel.
    function openRelatedPopover(action, url, btn) {
      var origLabel = btn.textContent;
      btn.textContent = 'Loading…';
      btn.disabled = true;
      fetch(url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify([currentSessionId]),
      }).then(function(r) {
        if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
        return r.json();
      }).then(function(data) {
        var suggestions = (data && data.suggestions) || data || [];
        var unanswered  = (data && data.unanswered)  || [];
        var answered    = (data && data.answered)    || [];
        if (!Array.isArray(suggestions)) suggestions = [];
        if (!Array.isArray(unanswered))  unanswered  = [];
        if (!Array.isArray(answered))    answered    = [];
        if (!suggestions.length && !unanswered.length && !answered.length) {
          showToast('No related topics found');
          return;
        }
        // No backdrop-click-to-close: a text-selection drag ending on the
        // backdrop would dismiss mid-copy. Close via the × button.
        var overlay = el('div', {class: 'ui-pl-modal-overlay'});
        var modal = el('div', {class: 'ui-pl-modal'});
        var header = el('div', {class: 'ui-pl-modal-h'});
        header.appendChild(el('div', {class: 'ui-pl-modal-title'}, [action.label || 'Related']));
        header.appendChild(el('button', {class: 'ui-pl-modal-close', title: 'Close',
          onclick: function() { close(); }}, ['×']));
        modal.appendChild(header);
        var body = el('div', {class: 'ui-pl-modal-body'});
        // mode:
        //   "submit" — fill the form + start a new run
        //   "open"   — load the linked existing session
        // variant:
        //   "accent"  — primary suggestions (theme accent rule)
        //   "warning" — unanswered (amber, needs follow-up)
        //   "success" — previously answered (green, click to open)
        //   "" / null — neutral border
        var addSection = function(label, items, variant, mode) {
          if (!items.length) return;
          var hClass = 'ui-pl-related-h' + (variant ? ' ' + variant : '');
          body.appendChild(el('div', {class: hClass}, [label]));
          items.forEach(function(s) {
            var q, reason, openID;
            if (typeof s === 'string') {
              q = s;
            } else {
              q       = s.question || s.topic || '';
              reason  = s.reason   || s.hook  || '';
              openID  = s.report_id || s.id;
            }
            if (!q) return;
            var iClass = 'ui-pl-related-item' + (variant ? ' ' + variant : '');
            var item = el('div', {class: iClass});
            item.appendChild(el('div', {class: 'ui-pl-related-q'}, [q]));
            if (reason) item.appendChild(el('div', {class: 'ui-pl-related-reason'}, [reason]));
            // Hint pill on answered entries so users know clicking
            // opens the existing report rather than starting a new run.
            if (mode === 'open' && openID) {
              item.appendChild(el('div', {class: 'ui-pl-related-hint'}, ['↗ open existing report']));
            }
            item.addEventListener('click', function() {
              close();
              if (mode === 'open' && openID) {
                openSession(openID);
                return;
              }
              // Default: fill the form's first textarea (or named
              // target) and submit a new run.
              var target = action.fill_target || (function() {
                for (var i = 0; i < (cfg.fields || []).length; i++) {
                  if (cfg.fields[i].type === 'textarea') return cfg.fields[i].name;
                }
                return cfg.fields && cfg.fields[0] && cfg.fields[0].name;
              })();
              openSession(null);
              if (target && formInputs[target]) {
                formInputs[target].value = q;
              }
              doSubmit();
            });
            body.appendChild(item);
          });
        };
        // Order matches the legacy Related panel: unanswered
        // first (most urgent — these gaps were never closed),
        // already-researched second (existing reports the user can
        // jump to), fresh suggestions last.
        addSection('Unanswered Questions',     unanswered,  'warning', 'submit');
        addSection('Previously Answered',      answered,    'success', 'open');
        addSection('Suggested Follow-ups',     suggestions, 'accent',  'submit');
        modal.appendChild(body);
        overlay.appendChild(modal);
        document.body.appendChild(overlay);
        function close() { overlay.remove(); }
      }).catch(function(err) {
        showToast('Related failed: ' + err.message);
      }).then(function() {
        btn.textContent = origLabel;
        btn.disabled = false;
      });
    }

    function openStreamModal(action, url) {
      var overlay = el('div', {class: 'ui-pl-modal-overlay'});
      var modal = el('div', {class: 'ui-pl-modal'});
      var header = el('div', {class: 'ui-pl-modal-h'});
      var titleEl = el('div', {class: 'ui-pl-modal-title'}, [action.label || 'Report']);
      // headerActions slot — modal-actions buttons live here on the
      // top right (Save as PDF, Regenerate, Copy, ×). Hidden until
      // the stream completes so it doesn't hint at not-yet-ready
      // actions while the body is still being generated.
      var headerActions = el('div', {class: 'ui-pl-modal-h-actions', style: 'display:none'});
      var closeBtn = el('button', {class: 'ui-pl-modal-close', title: 'Close',
        onclick: function() { close(); }}, ['×']);
      header.appendChild(titleEl);
      header.appendChild(headerActions);
      header.appendChild(closeBtn);
      var body = el('div', {class: 'ui-pl-modal-body'});
      body.appendChild(el('div', {class: 'ui-pl-modal-status'},
        ['Generating...', el('span', {class: 'ui-pl-spinner'})]));
      // Footer is no longer used — kept as a hidden stub so existing
      // references (footer.style.display = '') still resolve. All
      // actions render in headerActions instead.
      var footer = el('div', {style: 'display:none'});
      // Custom modal actions defined on the parent action — for
      // example: Save as PDF, Regenerate, Push to next stage.
      // Substitution: {id} uses the same id used for the parent
      // action's URL (extracted from the parent url).
      var idMatch = url.match(/\/([^\/?#]+)(?:\?|$)/);
      var modalSessionId = idMatch ? decodeURIComponent(idMatch[1]) : '';
      (action.modal_actions || []).forEach(function(ma) {
        var maURL = (ma.url || '').replace('{id}', encodeURIComponent(modalSessionId));
        var maMethod = ma.method || 'open';
        var maBtn;
        if (maMethod === 'open') {
          maBtn = el('a', {
            class: 'ui-pl-btn secondary',
            href: maURL, target: '_blank', rel: 'noopener',
            title: ma.title || ma.label,
            style: 'text-decoration:none',
          }, [ma.label]);
        } else {
          maBtn = el('button', {class: 'ui-pl-btn secondary', title: ma.title || ma.label, onclick: async function() {
            if (ma.confirm && !(await window.uiConfirm(ma.confirm))) return;
            if (maMethod === 'copy') {
              var fullURL = window.location.origin + window.location.pathname + maURL;
              if (ma.url && /^https?:/i.test(ma.url)) fullURL = maURL;
              navigator.clipboard.writeText(fullURL).then(function() { showToast('Copied'); })
                .catch(function() { showToast('Copy failed'); });
              return;
            }
            if (maMethod === 'regenerate') {
              // Re-run the parent stream with ?regenerate=1 appended.
              ctrl.abort();
              streamRaw = '';
              body.innerHTML = '';
              body.appendChild(el('div', {class: 'ui-pl-modal-status'},
                ['Regenerating...', el('span', {class: 'ui-pl-spinner'})]));
              headerActions.style.display = 'none';
              var sep = url.indexOf('?') >= 0 ? '&' : '?';
              var newURL = url + sep + 'regenerate=1';
              ctrl = new AbortController();
              fetch(newURL, {signal: ctrl.signal}).then(function(r) {
                if (!r.ok) throw new Error('HTTP ' + r.status);
                var reader = r.body.getReader();
                var dec = new TextDecoder();
                var buf = '';
                function pump() {
                  return reader.read().then(function(res) {
                    if (res.done) return;
                    buf += dec.decode(res.value, {stream: true});
                    var idx;
                    while ((idx = buf.indexOf('\n\n')) >= 0) {
                      processModalEvent(buf.slice(0, idx));
                      buf = buf.slice(idx + 2);
                    }
                    return pump();
                  });
                }
                return pump();
              }).catch(function(err) {
                if (err.name !== 'AbortError') {
                  body.innerHTML = '<div class="ui-pl-modal-status" style="color:var(--danger)">Failed: ' + (err.message || err) + '</div>';
                }
              });
              return;
            }
            if (maMethod === 'post') {
              maBtn.disabled = true;
              fetch(maURL, {method: 'POST'}).then(function(r) {
                if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
                showToast(ma.label + ' done');
              }).catch(function(err) { showToast(ma.label + ' failed: ' + err.message); })
                .then(function(){ maBtn.disabled = false; });
              return;
            }
          }}, [ma.label]);
        }
        headerActions.appendChild(maBtn);
      });
      var copyBtn = el('button', {class: 'ui-pl-btn secondary', onclick: function() {
        var raw = body.dataset.raw || body.textContent || '';
        navigator.clipboard.writeText(raw).then(function() { showToast('Copied'); })
          .catch(function() { showToast('Copy failed'); });
      }}, ['Copy']);
      headerActions.appendChild(copyBtn);
      modal.appendChild(header);
      modal.appendChild(body);
      modal.appendChild(footer);
      overlay.appendChild(modal);
      document.body.appendChild(overlay);
      // No backdrop-click-to-close: a text-selection drag ending on the
      // backdrop would dismiss mid-copy. Use the × button to close.

      var ctrl = new AbortController();
      var streamRaw = '';
      function close() {
        ctrl.abort();
        overlay.remove();
      }

      fetch(url, {signal: ctrl.signal}).then(function(r) {
        if (!r.ok) throw new Error('HTTP ' + r.status);
        var reader = r.body.getReader();
        var dec = new TextDecoder();
        var buf = '';
        function pump() {
          return reader.read().then(function(res) {
            if (res.done) { return; }
            buf += dec.decode(res.value, {stream: true});
            var idx;
            while ((idx = buf.indexOf('\n\n')) >= 0) {
              processModalEvent(buf.slice(0, idx));
              buf = buf.slice(idx + 2);
            }
            return pump();
          });
        }
        return pump();
      }).catch(function(err) {
        if (err.name === 'AbortError') return;
        body.innerHTML = '<div class="ui-pl-modal-status" style="color:var(--danger)">Failed: ' +
          (err.message || err) + '</div>';
      });

      function processModalEvent(raw) {
        var lines = raw.split('\n');
        var ev = 'message';
        var dataStr = '';
        for (var i = 0; i < lines.length; i++) {
          var l = lines[i];
          if (l.indexOf('event: ') === 0) ev = l.slice(7);
          else if (l.indexOf('data: ') === 0) dataStr += (dataStr ? '\n' : '') + l.slice(6);
        }
        if (!dataStr) return;
        var data = {};
        try { data = JSON.parse(dataStr); } catch (_) {}
        // Legacy report stream uses Type field on anonymous events.
        var type = ev !== 'message' ? ev : (data.Type || data.type || '');
        switch (type) {
          case 'report_header':
          case 'header':
            // Headline + topic + confidence on top of the modal.
            body.innerHTML = '';
            var headline = data.AgainstPosition || data.Topic || data.headline || '';
            if (headline) {
              body.appendChild(el('h2', {class: 'ui-pl-modal-headline'}, [headline]));
            }
            if (data.Topic && data.AgainstPosition) {
              var topicLine = data.Topic.length > 150 ? data.Topic.slice(0, 150) + '…' : data.Topic;
              body.appendChild(el('div', {class: 'ui-pl-modal-sub'}, ['Topic: ' + topicLine]));
            }
            if (data.Summary || data.Body) {
              body.appendChild(el('div', {class: 'ui-pl-modal-sub'},
                ['Generated: ' + (data.Summary || '') + (data.Body ? ' · From source: ' + data.Body : '')]));
            }
            if (data.ForPosition) {
              body.appendChild(el('div', {class: 'ui-pl-modal-sub'}, ['Confidence: ' + data.ForPosition]));
            }
            body.appendChild(el('div', {class: 'ui-pl-modal-content'}));
            break;
          case 'report_status':
          case 'status':
            var prior = body.querySelector('.ui-pl-modal-status');
            if (prior) prior.remove();
            body.appendChild(el('div', {class: 'ui-pl-modal-status'},
              [(data.Summary || data.text || ''), el('span', {class: 'ui-pl-spinner'})]));
            break;
          case 'report_stream':
          case 'chunk':
            var statusEl = body.querySelector('.ui-pl-modal-status');
            if (statusEl) statusEl.remove();
            streamRaw += (data.Body || data.text || '');
            body.dataset.raw = streamRaw;
            var content = body.querySelector('.ui-pl-modal-content');
            if (!content) {
              content = el('div', {class: 'ui-pl-modal-content'});
              body.appendChild(content);
            }
            uiRenderMarkdown(content, streamRaw);
            // Don't auto-scroll-to-bottom on each chunk — that
            // pushes the overlay past the headline and forces the
            // user to scroll back up. Scroll to top on done instead.
            break;
          case 'report_replace':
            streamRaw = data.Body || '';
            body.dataset.raw = streamRaw;
            var c2 = body.querySelector('.ui-pl-modal-content');
            if (c2) uiRenderMarkdown(c2, streamRaw);
            break;
          case 'report_done':
          case 'done':
            headerActions.style.display = '';
            var s = body.querySelector('.ui-pl-modal-status');
            if (s) s.remove();
            // Scroll the overlay back to the top of the report so
            // the user lands on the headline. Defer once so the
            // last chunk's layout has settled.
            requestAnimationFrame(function() {
              overlay.scrollTop = 0;
              modal.scrollTop = 0;
            });
            break;
          case 'error':
            body.innerHTML = '<div class="ui-pl-modal-status" style="color:var(--danger)">' +
              (data.Body || data.message || 'Error') + '</div>';
            break;
        }
      }
    }

    function renderActions(sessionId) {
      actionsBar.innerHTML = '';
      if (!sessionId || !cfg.actions || !cfg.actions.length) {
        // Cancel button still belongs in the bar (when running) — re-
        // append it so a session-less reconnect or running-but-no-
        // actions config still has a Cancel.
        actionsBar.appendChild(cancelBtn);
        if (cancelBtn.style.display === 'none') {
          actionsBar.style.display = 'none';
        }
        return;
      }
      actionsBar.style.display = '';
      var sessionRec = sessionsByID[sessionId] || {};
      cfg.actions.forEach(function(a) {
        // ShowIfField — skip the action when the named summary field
        // on this session record is falsy. Lets apps hide buttons
        // that don't apply to every session (e.g. "Descendants"
        // only on parent records).
        if (a.show_if_field && !sessionRec[a.show_if_field]) return;
        if (a.hide_if_field && sessionRec[a.hide_if_field]) return;
        // URL placeholder substitution: {id} → current session id;
        // {<FieldName>} → that field on the session record. Lets
        // actions reference cross-session pointers like ParentID
        // (Back-to-parent button) without app-specific runtime code.
        var url = (a.url || '').replace(/\{([A-Za-z_][A-Za-z0-9_]*)\}/g, function(_, key) {
          if (key === 'id') return encodeURIComponent(sessionId);
          var v = sessionRec[key];
          return v != null ? encodeURIComponent(String(v)) : '';
        });
        var btnClass = 'ui-pl-btn ' + (a.variant || 'secondary');
        var btn;
        var method = a.method || 'open';
        if (method === 'open') {
          btn = el('a', {
            class: btnClass, href: url, target: '_blank', rel: 'noopener',
            title: a.title || a.label,
            style: 'text-decoration:none',
          }, [a.label]);
        } else {
          btn = el('button', {class: btnClass, title: a.title || a.label, onclick: async function() {
            if (a.confirm && !(await window.uiConfirm(a.confirm))) return;
            if (method === 'copy') {
              var fullURL = window.location.origin + window.location.pathname + url;
              if (a.url && /^https?:/i.test(a.url)) fullURL = url;
              else if (a.url && a.url.indexOf('?') === 0) fullURL = window.location.href.split('?')[0] + url;
              else if (a.url && a.url.indexOf('#') === 0) fullURL = window.location.href.split('#')[0] + url;
              // Inline button feedback — flip the label to "Copied!"
              // for ~1.5s then revert. Keeps the action local instead
              // of bouncing the user's eye to a corner toast.
              var origLabel = btn.textContent;
              navigator.clipboard.writeText(fullURL).then(function() {
                btn.textContent = 'Copied!';
                btn.classList.add('copied');
              }).catch(function() {
                btn.textContent = 'Copy failed';
              }).then(function() {
                setTimeout(function() {
                  btn.textContent = origLabel;
                  btn.classList.remove('copied');
                }, 1500);
              });
              return;
            }
            if (method === 'post') {
              btn.disabled = true;
              fetch(url, {method: 'POST'}).then(function(r) {
                if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
                showToast(a.label + ' done');
                loadSessions();
              }).catch(function(err){ showToast(a.label + ' failed: ' + err.message); })
                .then(function(){ btn.disabled = false; });
              return;
            }
            if (method === 'stream') {
              clearTranscript();
              setRunning(true);
              streamFrom(url, {method: 'POST', headers: {'Content-Type': 'application/json'}, body: '{}'});
              return;
            }
            if (method === 'modal') {
              openStreamModal(a, url);
              return;
            }
            if (method === 'related') {
              openRelatedPopover(a, url, btn);
              return;
            }
            if (method === 'load') {
              // Load the session whose id is in url (already
              // substituted from a {FieldName} placeholder) into
              // the current panel. Used for "Back to parent" style
              // cross-session navigation. Decode here because the
              // placeholder substitution URL-encoded the value.
              var targetID = decodeURIComponent(url);
              if (targetID) openSession(targetID);
              return;
            }
            if (method === 'client') {
              // Browser-side action — dispatch by name to a handler
              // registered via window.uiRegisterClientAction. The
              // URL field carries the action name (e.g. "print_
              // transcript"); the handler receives ({sessionId,
              // sessionRec, button}) so it can inspect state /
              // toggle UI / etc.
              var actionName = decodeURIComponent(url);
              var fn = window.UIClientActions && window.UIClientActions[actionName];
              if (typeof fn === 'function') {
                fn({sessionId: sessionId, sessionRec: sessionRec, button: btn, action: a});
              } else {
                showToast('No handler for client action: ' + actionName);
              }
              return;
            }
          }}, [a.label]);
        }
        actionsBar.appendChild(btn);
      });
      // Cancel pinned to the far right via margin-left:auto on the
      // .ui-pl-btn-cancel class. Appended last so flex order matches
      // visual order (action buttons left, Cancel right).
      actionsBar.appendChild(cancelBtn);
      // Re-applies running-state disabling to the freshly-built
      // buttons. Without this, a renderActions call mid-run would
      // produce live (clickable) action buttons.
      applyActionDisabled();
    }
    wrap.appendChild(side);
    wrap.appendChild(main);
    wrap.appendChild(drawerBackdrop);

    function showToast(msg) {
      var t = el('div', {class: 'ui-toast'}, [msg]);
      document.body.appendChild(t);
      setTimeout(function(){ t.remove(); }, 3000);
    }

    function clearTranscript() {
      blockEls = {};
      transcript.innerHTML = '';
    }

    // ensureBlock dispatches to a per-type renderer, falling back to
    // plain "text" if the block.type isn't one we know about.
    // Renderers must return {wrap, body, raw, done, data} where body
    // is the element chunk events should accumulate text into.
    function ensureBlock(id, type, data) {
      if (blockEls[id]) return blockEls[id];
      if (emptyHint.parentNode === transcript) emptyHint.remove();
      // Drop any live status before a structural block lands —
      // the new block IS the next thing, so the "Researching…"
      // status is stale by definition. Matches legacy arena
      // behavior: a real event always clears the prior spinner.
      var prior = transcript.querySelector('.ui-pl-status');
      if (prior) prior.remove();
      // ctx carries panel-level flags the renderers might consult
      // without reaching into pipeline_panel's closure scope. Apps
      // that ship their own renderers via Page.ExtraHeadHTML rely
      // on this — they can't see cfg directly, but they receive
      // ctx.markdown / ctx.cfg here.
      var ctx = {markdown: cfg.markdown, cfg: cfg};
      var renderer = blockRenderers[type] || blockRenderers.text;
      var rec = renderer(data || {type: type}, ctx);
      if (!rec || !rec.wrap) {
        // Defensive: never let a bad renderer leave us blockless.
        rec = blockRenderers.text(data || {type: type}, ctx);
      }
      rec.raw = '';
      rec.done = false;
      rec.data = data || {};
      transcript.appendChild(rec.wrap);
      blockEls[id] = rec;
      scrollTranscript();
      return rec;
    }

    function appendChunk(id, text) {
      var rec = blockEls[id] || ensureBlock(id, 'text', {});
      rec.raw += text;
      // Live render as plain text so partial markdown doesn't break
      // intermediate paint. finalizeBlock runs mdToHTML on done.
      if (rec.body) rec.body.textContent = rec.raw;
      scrollTranscript();
    }

    function finalizeBlock(id) {
      var rec = blockEls[id];
      if (!rec || rec.done) return;
      rec.done = true;
      if (rec.body && cfg.markdown && rec.raw.trim()) {
        uiRenderMarkdown(rec.body, rec.raw);
      }
      // Per-renderer onDone hook for any final chrome (e.g. drop the
      // streaming spinner, finalize sources panel).
      if (rec.onDone) rec.onDone(rec);
      scrollTranscript();
    }

    function applyBlockMeta(id, meta) {
      var rec = blockEls[id];
      if (!rec || !rec.onMeta) return;
      rec.onMeta(rec, meta || {});
      scrollTranscript();
    }

    // -------- Block renderers ----------------------------------------------
    // Each renderer takes the block's data object and returns
    // {wrap, body, onMeta?, onDone?}. wrap is the DOM node appended
    // to the transcript; body is where chunk text accumulates;
    // onMeta runs on block_meta events (for sources, summary, etc.);
    // onDone runs on block_done.
    //
    // The registry lives on the window object so apps can register
    // their own renderers from JS shipped via Page.ExtraHeadHTML.
    // Generic types (text, expandable) are registered below; app-
    // specific types (verdict, argument, etc.) per app
    // live in the app's package and load before pipeline_panel
    // mounts. Pipeline_panel takes a snapshot at mount time so a
    // late-arriving app payload doesn't surprise an already-rendered
    // panel; apps should declare their renderers in ExtraHeadHTML
    // (loaded synchronously in the document head) rather than via
    // deferred scripts.
    // Snapshot the global registry at mount time. The window-level
    // registry was initialized at runtime IIFE top so apps loading
    // via Page.ExtraHeadHTML can register before this point.
    var blockRenderers = Object.assign({}, window.UIBlockRenderers || {});

    blockRenderers.text = function(d) {
      // Optional class hint — bridges can declare a CSS class on the
      // block to scope styles (e.g. "ui-pl-final-report" tightens
      // heading→body spacing for synthesized reports).
      // Sanitized via a strict pattern so the wire format can't
      // inject arbitrary attributes.
      var classes = 'ui-pl-block ui-pl-block-text';
      if (d.class && /^[A-Za-z0-9 _-]+$/.test(d.class)) {
        classes += ' ' + d.class;
      }
      var wrap = el('div', {class: classes});
      // Always create the header so block_meta can rewrite the
      // title later (e.g. "Setup phase" → "Complete"
      // when the run finishes). Empty header collapses to nothing
      // visible, so this is safe for blocks that ship without a
      // title.
      var hdr = el('div', {class: 'ui-pl-block-h'}, [d.title || '']);
      if (!d.title) hdr.style.display = 'none';
      wrap.appendChild(hdr);
      var body = el('div', {class: 'ui-pl-block-body'});
      wrap.appendChild(body);
      return {
        wrap: wrap, body: body,
        onMeta: function(rec, meta) {
          if (typeof meta.title === 'string') {
            hdr.textContent = meta.title;
            hdr.style.display = meta.title ? '' : 'none';
          }
        },
      };
    };

    // App-specific renderers (round_header, section_header,
    // argument, verdict) live in each app's web_assets.go.
    // Loaded into window.UIBlockRenderers before pipeline_panel
    // mounts via the app page's ExtraHeadHTML.

    // expandable — generic collapsible card, reused for "Expert
    // Consensus Baseline" and similar named writeups.
    blockRenderers.expandable = function(d) {
      var wrap = el('div', {class: 'ui-pl-expandable'});
      wrap.addEventListener('click', function(){ wrap.classList.toggle('expanded'); });
      var head = el('div', {class: 'ui-pl-expandable-h'});
      // Title can be multi-line (an app payload may emit a
      // "Question: …\nAnswer: …" summary). Split on newlines
      // and render each as its own <div> so the lines stack
      // instead of collapsing whitespace into a single run.
      var nameWrap = el('span', {class: 'ui-pl-expandable-title'});
      var titleStr = String(d.title || '');
      var lines = titleStr.split('\n');
      for (var li = 0; li < lines.length; li++) {
        var line = lines[li];
        if (li === 0) {
          nameWrap.appendChild(document.createTextNode(line));
        } else {
          nameWrap.appendChild(el('div', {class: 'ui-pl-expandable-subtitle'}, [line]));
        }
      }
      head.appendChild(nameWrap);
      head.appendChild(el('span', {class: 'ui-pl-expandable-toggle'}, ['show details']));
      wrap.appendChild(head);
      var body = el('div', {class: 'ui-pl-expandable-body'});
      if (d.body) {
        if (cfg.markdown) uiRenderMarkdown(body, d.body);
        else body.textContent = d.body;
      }
      wrap.appendChild(body);
      return {wrap: wrap, body: body};
    };

    // Default renderers for the framework's clarifying-question control-tool
    // card blocks (ask_user / ask_user_form). These block TYPE strings are the
    // wire contract emitted by the agent runner; the renderers here are generic
    // ({question, options}/{steps}) and know no app's shape. Any AgentLoopPanel
    // surface renders the card + submits the answer with no per-app wiring. They
    // defer to an app-registered renderer (via window.UIBlockRenderers) when one
    // exists, so an app can ship a richer variant without this overriding it.
    // The answer is sent through the panel's own input row — the same send flow
    // a typed message takes — so the agent sees it as the next user turn.
    function uiAskSubmitAnswer(answer) {
      var inputArea = document.querySelector('.ui-agent-input');
      var sendBtn = document.querySelector('.ui-agent-input-row .ui-row-btn.primary');
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
    if (!blockRenderers.orchestrate_ask) {
      blockRenderers.orchestrate_ask = function(d) {
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
          if (!uiAskSubmitAnswer(answer)) return;
          wrap.classList.add('submitted');
          inputs.forEach(function(i){ i.disabled = true; });
          extra.disabled = true;
          submit.disabled = true;
        });
        return {wrap: wrap, body: null};
      };
    }
    if (!blockRenderers.orchestrate_ask_form) {
      blockRenderers.orchestrate_ask_form = function(d) {
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
          if (!uiAskSubmitAnswer(lines.join('\n'))) return;
          wrap.classList.add('submitted');
          wrap.querySelectorAll('input, textarea, select, button').forEach(function(x){ x.disabled = true; });
        });
        return {wrap: wrap, body: null};
      };
    }

    function scrollTranscript() {
      var pin = function() { transcript.scrollTop = transcript.scrollHeight; };
      pin();
      requestAnimationFrame(pin);
      setTimeout(pin, 100);
    }

    var runningState = false;
    // applyActionDisabled greys out every action button in actionsBar
    // except the Cancel button while a run is in flight. Buttons get
    // the native disabled attribute; <a> action buttons can't be
    // disabled natively, so they get the .is-disabled class which sets
    // pointer-events:none + opacity to match. Called from setRunning
    // and from renderActions so a re-render mid-run keeps state.
    function applyActionDisabled() {
      var kids = actionsBar.children;
      for (var i = 0; i < kids.length; i++) {
        var k = kids[i];
        if (k === cancelBtn || k.classList.contains('ui-pl-btn-cancel')) continue;
        if (!k.classList.contains('ui-pl-btn')) continue;
        if (runningState) {
          if (k.tagName === 'BUTTON') k.disabled = true;
          k.classList.add('is-disabled');
          k.setAttribute('aria-disabled', 'true');
        } else {
          if (k.tagName === 'BUTTON') k.disabled = false;
          k.classList.remove('is-disabled');
          k.removeAttribute('aria-disabled');
        }
      }
    }
    function setRunning(running) {
      runningState = !!running;
      submitBtn.style.display = running ? 'none' : '';
      cancelBtn.style.display = running ? '' : 'none';
      Object.keys(formInputs).forEach(function(k){ formInputs[k].disabled = !!running; });
      if (prefillBtn) prefillBtn.disabled = !!running;
      // Surface the actions toolbar when a run is in flight so the
      // Cancel button is reachable even before any session_id event
      // populates the per-session URLs.
      if (running) actionsBar.style.display = '';
      applyActionDisabled();
    }

    // setFormVisible toggles the whole submit form (topic, rounds,
    // suggest, start). Hidden when viewing a saved session — the
    // user clicks "+ New" in the sidebar to bring it back.
    function setFormVisible(visible) {
      form.style.display = visible ? '' : 'none';
    }

    // streamFrom drives the SSE reader for both fresh submits and
    // reconnects. fetchOpts is whatever fetch() needs (method/body
    // for POST, default GET for reconnect).
    function streamFrom(url, fetchOpts) {
      activeStream = new AbortController();
      fetchOpts = fetchOpts || {};
      fetchOpts.signal = activeStream.signal;
      return fetch(url, fetchOpts).then(function(r) {
        if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
        var reader = r.body.getReader();
        var decoder = new TextDecoder();
        var buf = '';
        function pump() {
          return reader.read().then(function(res) {
            if (res.done) { finish(); return; }
            buf += decoder.decode(res.value, {stream: true});
            var idx;
            while ((idx = buf.indexOf('\n\n')) >= 0) {
              var raw = buf.slice(0, idx);
              buf = buf.slice(idx + 2);
              processEvent(raw);
            }
            return pump();
          });
        }
        return pump();
      }).catch(function(err) {
        if (err.name !== 'AbortError') showToast('Failed: ' + err.message);
        finish();
      });
    }

    function doSubmit() {
      // Validate required fields and gather body.
      var body = {};
      var missing = null;
      (cfg.fields || []).forEach(function(f) {
        var inp = formInputs[f.name];
        // File fields only populate other fields (via server-side
        // extraction on pick); their own .value is a fake path string,
        // so they never contribute to the submit body.
        if (f.type === 'file') return;
        // Toggle/checkbox fields contribute a boolean from .checked
        // — taking .value on a checkbox returns "on"/"off" strings.
        if (f.type === 'toggle') {
          body[f.name] = !!inp.checked;
          return;
        }
        var v = inp.value;
        if (f.required && !String(v || '').trim()) { missing = missing || f.name; return; }
        if (v === '' || v == null) return;
        if (f.type === 'number') {
          var n = Number(v);
          if (!isNaN(n)) v = n;
        }
        body[f.name] = v;
      });
      if (missing) { showToast('Required: ' + missing); return; }

      currentSessionId = '';
      clearTranscript();
      // Use the title field's value (e.g. "topic") as the session
      // title strip immediately so users see what's running.
      setFormVisible(false);
      var titleField = cfg.session_title_field || 'Title';
      var liveTitle = body[titleField.toLowerCase()] || body.topic || body.subject || body.message || '';
      setSessionTitle(liveTitle);
      setRunning(true);
      streamFrom(cfg.submit_url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(body),
      });
    }

    // tryReconnect attempts to attach to a live pipeline run identified
    // by a ?session={id} URL query param. On success, streams events
    // into the transcript like a fresh submit; on failure (HTTP 404 →
    // session not live), falls back to loading the saved record.
    function tryReconnect(sessionId) {
      if (!cfg.reconnect_url || !sessionId) return false;
      var url = cfg.reconnect_url.replace('{id}', encodeURIComponent(sessionId));
      currentSessionId = sessionId;
      clearTranscript();
      setFormVisible(false);
      renderActions(sessionId);
      setRunning(true);
      // Set the page title from whatever session metadata we have.
      // sessionsByID is populated by the prior loadSessions() call;
      // if the entry isn't there yet (race) fall back to fetching
      // /api/sessions/{id} just for the title so the operator sees
      // the question they're reconnecting to instead of "New run".
      var cached = sessionsByID[sessionId];
      if (cached && cached[titleF]) {
        mobileTitle.textContent = cached[titleF];
        setSessionTitle(cached[titleF]);
      } else if (cfg.session_load_url) {
        var loadURL = cfg.session_load_url.replace('{id}', encodeURIComponent(sessionId));
        fetchJSON(loadURL).then(function(s) {
          var t = (s && s[titleF]) || '';
          if (t) {
            mobileTitle.textContent = t;
            setSessionTitle(t);
          }
        }).catch(function(){});
      }
      activeStream = new AbortController();
      fetch(url, {signal: activeStream.signal}).then(function(r) {
        if (r.status === 404) {
          // Not live anymore — load the saved record instead.
          setRunning(false);
          openSession(sessionId);
          return;
        }
        if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
        var reader = r.body.getReader();
        var decoder = new TextDecoder();
        var buf = '';
        function pump() {
          return reader.read().then(function(res) {
            if (res.done) { finish(); return; }
            buf += decoder.decode(res.value, {stream: true});
            var idx;
            while ((idx = buf.indexOf('\n\n')) >= 0) {
              var raw = buf.slice(0, idx);
              buf = buf.slice(idx + 2);
              processEvent(raw);
            }
            return pump();
          });
        }
        return pump();
      }).catch(function(err) {
        if (err.name !== 'AbortError') showToast('Reconnect failed: ' + err.message);
        finish();
      });
      return true;
    }

    function processEvent(raw) {
      var lines = raw.split('\n');
      var ev = 'message';
      var dataStr = '';
      for (var i = 0; i < lines.length; i++) {
        var l = lines[i];
        if (l.indexOf('event: ') === 0) ev = l.slice(7);
        else if (l.indexOf('data: ') === 0) dataStr += (dataStr ? '\n' : '') + l.slice(6);
      }
      var data = {};
      if (dataStr) { try { data = JSON.parse(dataStr); } catch(e) {} }
      switch (ev) {
        case 'session':
          if (data.id) {
            currentSessionId = data.id;
            renderActions(data.id);
          }
          break;
        case 'block':
          // Pass the full data object — renderer pulls type-specific
          // fields (side, round, summary, statement, etc.) out of it.
          var bid = data.id || ('b' + Object.keys(blockEls).length);
          ensureBlock(bid, data.type || 'text', data);
          // When the block event ships a full body (one-shot blocks
          // that don't stream — e.g. a synthesized report),
          // populate rec.raw so block_done's finalizeBlock has
          // something to mdToHTML. Without this the body field on the
          // block event is silently dropped and the block renders
          // empty.
          if (data.body) {
            var brec = blockEls[bid];
            if (brec && brec.body) {
              brec.raw = String(data.body);
              brec.body.textContent = brec.raw;
            }
          }
          if (data.type === 'verdict') liveVerdictBid = bid;
          break;
        case 'block_meta':
          applyBlockMeta(data.id, data);
          break;
        case 'chunk':
          appendChunk(data.id || 'main', data.text || '');
          break;
        case 'chunk_replace':
          // Replace the entire body of a block with new content.
          // Accepts either "text" (legacy streaming chunks)
          // or "body" (new bridges emitting markdown). When
          // cfg.markdown is on AND the payload is full markdown
          // (as opposed to in-flight chunked plain text), render
          // through mdToHTML so headings, lists, and links land
          // styled.  For streaming chunked text we still want
          // textContent so partial markdown doesn't break paint.
          var rec = blockEls[data.id || 'main'];
          if (rec) {
            var raw = String((data.body != null ? data.body : data.text) || '');
            rec.raw = raw;
            // The "body" field signals the caller is sending a
            // complete current snapshot (as opposed to streamed
            // chunks). Render it as markdown so contained-window
            // sections look polished.
            if (data.body != null && cfg.markdown) {
              uiRenderMarkdown(rec.body, raw);
            } else {
              rec.body.textContent = raw;
            }
            scrollTranscript();
          }
          break;
        case 'block_done':
          finalizeBlock(data.id);
          break;
        case 'block_remove':
          // Drop a block entirely — DOM node out, blockEls entry
          // gone. Used when an app's flow renders a transient
          // cluster that an app may emit as the
          // should disappear once the underlying work completes
          // and the next block carries the result. Generic — any
          // app can emit this for any transient block id.
          var rm = blockEls[data.id];
          if (rm) {
            if (rm.wrap && rm.wrap.parentNode) rm.wrap.remove();
            delete blockEls[data.id];
          }
          break;
        case 'status':
          // Single-line status that REPLACES any prior status — same
          // "current focus" — and the arena removed the prior status
          // before inserting the new one. Only the most recent
          // "what's happening right now" stays visible; the trail
          // would just clutter the transcript.
          var prior = transcript.querySelector('.ui-pl-status');
          if (prior) prior.remove();
          if (data.text) {
            var s = el('div', {class: 'ui-pl-status ui-pl-status-live'});
            s.appendChild(el('span', {class: 'ui-pl-status-text'}, [data.text]));
            s.appendChild(el('span', {class: 'ui-pl-spinner'}));
            transcript.appendChild(s);
            scrollTranscript();
          }
          break;
        case 'error':
          // Drop any live status when an error lands.
          var liveStatus = transcript.querySelector('.ui-pl-status');
          if (liveStatus) liveStatus.remove();
          var e = el('div', {class: 'ui-chat-error'}, [data.message || 'Error']);
          transcript.appendChild(e);
          scrollTranscript();
          break;
        case 'done':
          // Finalize any block still open.
          Object.keys(blockEls).forEach(function(k){ finalizeBlock(k); });
          // Auto-scroll to the verdict block (mirrors the
          // history-click behavior). Defer past finalize's own
          // scrollTranscript pins (rAF + 100ms) so the verdict
          // scroll wins; also re-pin at 300ms in case anything
          // shifts layout afterward.
          if (liveVerdictBid && blockEls[liveVerdictBid]) {
            var doScroll = function() {
              var rec = blockEls[liveVerdictBid];
              if (!rec || !rec.wrap) return;
              var wRect = rec.wrap.getBoundingClientRect();
              var tRect = transcript.getBoundingClientRect();
              transcript.scrollTop += (wRect.top - tRect.top);
            };
            setTimeout(doScroll, 150);
            setTimeout(doScroll, 350);
          }
          break;
      }
    }

    function finish() {
      setRunning(false);
      activeStream = null;
      loadSessions();
    }

    function doCancel() {
      if (activeStream) activeStream.abort();
      if (cfg.cancel_url && currentSessionId) {
        // LiveSessions.HandleCancel reads ?id= (legacy convention).
        fetch(cfg.cancel_url + '?id=' + encodeURIComponent(currentSessionId), {method: 'POST'}).catch(function(){});
      }
      finish();
    }

    function openSession(id) {
      currentSessionId = id || '';
      clearTranscript();
      transcript.appendChild(emptyHint);
      // Reset form to defaults.
      (cfg.fields || []).forEach(function(f) { formInputs[f.name].value = f.default || ''; });
      setRunning(false);
      renderActions(id);
      setSessionTitle('');
      // No id means "new run" — show form, hide actions toolbar
      // (handled by renderActions). With an id, hide the form so
      // the saved transcript gets the full main column.
      setFormVisible(!id);
      if (!id) {
        mobileTitle.textContent = 'New run';
        loadSessions();
        return;
      }
      var url = cfg.session_load_url.replace('{id}', encodeURIComponent(id));
      fetchJSON(url).then(function(s) {
        var title = (s && s[titleF]) || 'Untitled';
        mobileTitle.textContent = title;
        setSessionTitle(title);
        var blocks = (s && s[blocksF]) || [];
        if (blocks.length) emptyHint.remove();
        var verdictBid = null;
        blocks.forEach(function(b, i) {
          var bid = b.id || ('b' + i);
          ensureBlock(bid, b.type || 'text', b);
          var rec = blockEls[bid];
          if (rec && rec.onMeta && (b.summary || (b.sources && b.sources.length))) {
            rec.onMeta(rec, {summary: b.summary, sources: b.sources || []});
          }
          // verdict's analysis is stored in body for that type;
          // argument's body field is the full text to render.
          var content = b.body || b.analysis || '';
          if (content && rec && rec.body) {
            rec.raw = content;
            if (cfg.markdown) {
              uiRenderMarkdown(rec.body, content);
              // Diagnostic — surface the gap when the rendered HTML
              // has substantially less content than the raw markdown
              // (mdToHTML regex catastrophic-backtracking on certain
              // markdown patterns). Only logs on big shrinks so
              // normal markdown doesn't spam the console.
              if (window.console && rec.body.textContent.length < content.length * 0.6) {
                console.warn('[ui] block body renders shorter than source —',
                  'raw chars:', content.length,
                  'rendered chars:', rec.body.textContent.length,
                  'block:', bid);
              }
            } else {
              rec.body.textContent = content;
            }
          }
          finalizeBlock(bid);
          if (b.type === 'verdict') verdictBid = bid;
        });
        loadSessions();
        // Saved sessions are typically opened to read the verdict —
        // jump straight to it. finalizeBlock calls scrollTranscript
        // for each block (which schedules pins at now / rAF / 100ms),
        // so we have to fire AFTER the trailing pin or the verdict
        // scroll gets clobbered by scroll-to-bottom. 150ms is enough
        // to let the trailing pin land first.
        var doVerdictScroll = function() {
          if (verdictBid && blockEls[verdictBid]) {
            // offsetTop is relative to the nearest positioned ancestor
            // (which may not be the transcript). Compute the in-
            // container delta with bounding rects so the verdict
            // lands at the top edge of the transcript exactly.
            var wrap = blockEls[verdictBid].wrap;
            var wRect = wrap.getBoundingClientRect();
            var tRect = transcript.getBoundingClientRect();
            transcript.scrollTop += (wRect.top - tRect.top);
          } else {
            transcript.scrollTop = 0;
          }
        };
        setTimeout(doVerdictScroll, 150);
        // Also pin once more at 300ms in case anything else fires.
        setTimeout(doVerdictScroll, 300);
        // Add export button if configured.
        if (cfg.session_export_url) {
          var url = cfg.session_export_url.replace('{id}', encodeURIComponent(id));
          var exportBtn = el('a', {
            class: 'ui-pl-btn secondary',
            href: url, target: '_blank',
            style: 'display:inline-block;margin-top:0.5rem;text-decoration:none',
          }, [cfg.session_export_label || 'Export']);
          transcript.appendChild(exportBtn);
        }
      }).catch(function(err) {
        transcript.appendChild(el('div', {class: 'ui-chat-error'}, ['Load failed: ' + err.message]));
      });
    }

    // sessionsByID caches the most recent sidebar fetch keyed by id
    // so renderActions can read per-session fields (HasDescendants
    // etc.) without a second round-trip. Populated by every
    // loadSessions() call; renderActions reads it on demand.
    var sessionsByID = {};
    // sessionSources mirrors the one declared in agent_loop_panel.
    // pipeline_panel is its own components.X scope, so this needs
    // its own var or WebKit throws "Can't find variable:
    // sessionSources" when loadSessions assigns to it below.
    var sessionSources = {};
    function loadSessions() {
      fetchJSON(cfg.sessions_list_url).then(function(list) {
        sideList.innerHTML = '';
        sessionsByID = {};
        (list || []).forEach(function(s){ if (s && s[idF]) sessionsByID[s[idF]] = s; });
        if (!list || !list.length) {
          if (cfg.bulk_select && bulkState.mode) {
            renderBulkBar([], sideList, bulkState, bulkSelected,
              function(s){ return s[idF]; }, loadSessions, function(){});
          }
          sideList.appendChild(el('div', {class: 'ui-chat-empty', style: 'padding:0.5rem'}, ['No runs yet.']));
          return;
        }
        list.sort(function(a, b){ return String(b[dateF] || '').localeCompare(String(a[dateF] || '')); });
        var ids = {}; list.forEach(function(s){ ids[s[idF]] = true; });
        Object.keys(bulkSelected).forEach(function(k){ if (!ids[k]) delete bulkSelected[k]; });
        // Rebuild source map from the latest list. Cleared first so
        // a row that drops a source tag (or goes away entirely)
        // doesn't keep its old routing on subsequent loads.
        sessionSources = {};
        list.forEach(function(s){
          if (s && s.source) {
            sessionSources[s[idF]] = {source: s.source, chat_id: s.chat_id || ''};
          }
        });

        if (cfg.bulk_select) {
          renderBulkBar(list, sideList, bulkState, bulkSelected,
            function(s){ return s[idF]; },
            loadSessions,
            async function() {
              var sel = Object.keys(bulkSelected);
              if (!sel.length) return;
              if (!(await window.uiConfirm('Delete ' + sel.length + ' run(s) permanently?'))) return;
              Promise.all(sel.map(function(id) {
                var u = cfg.session_delete_url.replace('{id}', encodeURIComponent(id));
                return fetch(u, {method: 'DELETE'});
              })).then(function() {
                if (bulkSelected[currentSessionId]) openSession(null);
                bulkSelected = {};
                bulkState.mode = false;
                if (sideSelectBtn) {
                  sideSelectBtn.classList.remove('active');
                  sideSelectBtn.textContent = 'Select';
                }
                loadSessions();
              }).catch(function(err) { showToast('Delete failed: ' + err.message); });
            });
        }

        list.forEach(function(s) {
          var inMode = cfg.bulk_select && bulkState.mode;
          var selected = !!bulkSelected[s[idF]];
          var item = el('div', {
            class: 'ui-chat-side-item' +
              (s[idF] === currentSessionId ? ' active' : '') +
              (inMode ? ' selectable' : '') +
              (selected ? ' selected' : ''),
            onclick: function(ev) {
              if (ev.target.classList.contains('ui-chat-side-del')) return;
              if (inMode) {
                if (bulkSelected[s[idF]]) delete bulkSelected[s[idF]];
                else bulkSelected[s[idF]] = true;
                loadSessions();
                return;
              }
              openSession(s[idF]);
              closeDrawer();
            },
          });
          var textWrap = el('div', {class: 'ui-chat-side-text'});
          textWrap.appendChild(el('div', {class: 'ui-chat-side-title'}, [String(s[titleF] || 'Untitled')]));
          // Optional meta fields per session. To keep rows compact
          // (more visible at once on both desktop and mobile), all
          // "pill"/"badge" style fields share ONE horizontal row;
          // each "text" field gets a single ellipsized line.
          var pillRow = null;
          (cfg.session_meta_fields || []).forEach(function(mf) {
            var raw = s[mf.field];
            if (raw === undefined || raw === null || raw === '') return;
            var text = String(raw);
            if (mf.truncate && text.length > mf.truncate) {
              text = text.slice(0, mf.truncate - 1).trimEnd() + '…';
            }
            var style = mf.style || 'text';
            if (style === 'badge' || style === 'pill') {
              if (!pillRow) {
                pillRow = el('div', {class: 'ui-pl-side-pillrow'});
                textWrap.appendChild(pillRow);
              }
              var pill = el('span', {class: 'ui-pl-side-pill'});
              if (mf.variants) {
                var key = String(raw).toLowerCase();
                var color = mf.variants[key];
                if (color) pill.style.cssText = 'background:' + color + '20;color:' + color + ';border-color:' + color + '50';
              }
              pill.appendChild(document.createTextNode(text));
              if (style === 'pill') pill.classList.add('pill');
              pillRow.appendChild(pill);
            } else {
              var line = el('div', {class: 'ui-pl-side-metaline'});
              if (mf.label) {
                line.appendChild(el('span', {class: 'ui-pl-side-metalabel'}, [mf.label + ' ']));
              }
              line.appendChild(document.createTextNode(text));
              textWrap.appendChild(line);
            }
          });
          // Date at the bottom — share the row with pills if there
          // are any, otherwise its own muted line.
          if (pillRow) {
            pillRow.appendChild(el('span', {class: 'ui-chat-side-meta', style: 'margin-left:auto'}, [relTime(s[dateF])]));
          } else {
            textWrap.appendChild(el('div', {class: 'ui-chat-side-meta'}, [relTime(s[dateF])]));
          }
          item.appendChild(textWrap);
          if (!inMode) {
            var delBtn = el('button', {
              class: 'ui-chat-side-del', title: 'Delete',
              onclick: async function(ev) {
                ev.stopPropagation();
                if (!(await window.uiConfirm('Delete this run?'))) return;
                var u = cfg.session_delete_url.replace('{id}', encodeURIComponent(s[idF]));
                fetch(u, {method: 'DELETE'}).then(function() {
                  if (s[idF] === currentSessionId) openSession(null);
                  loadSessions();
                });
              },
            }, ['×']);
            item.appendChild(delBtn);
          }
          sideList.appendChild(item);
        });
      }).catch(function(err) {
        sideList.innerHTML = '';
        sideList.appendChild(el('div', {class: 'ui-chat-error', style: 'padding:0.5rem'}, ['Load failed: ' + err.message]));
      });
    }

    loadSessions();

    // Auto-attach to a live pipeline run if the URL carries a
    // session id. Accepts ?session=, ?id=, ?run= (legacy
    // queue links), ?id=, or ?run= so the runtime works with
    // links produced by the legacy provider URLs that haven't
    // been updated to the new convention.
    try {
      var params = new URLSearchParams(window.location.search);
      // Apps declare their preferred deep-link param via
      // cfg.deep_link_param; the generic "session" / "id" / "run"
      // names are always also honored as fallbacks so older
      // bookmarks and shareable links keep working.
      var sid = '';
      if (cfg.deep_link_param) sid = params.get(cfg.deep_link_param) || '';
      if (!sid) {
        sid = params.get('session') || params.get('id') || params.get('run') || '';
      }
      if (sid) {
        if (cfg.reconnect_url) tryReconnect(sid);
        else openSession(sid);
      }
    } catch (_) {}

    return wrap;
  };

