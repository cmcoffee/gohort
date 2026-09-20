  // The notifications bell, in the shared page header.
  //
  // Deployment-wide rather than one app's: anything that runs on its own can
  // have something to tell its owner, and the place to find that out must not
  // be inside whichever app happened to write it. So this rides the chrome the
  // framework renders for every page, beside the live pill, and speaks only to
  // /api/notifications — a framework endpoint, no app named anywhere in here.
  //
  // Muted until there is something unread. A bell that is always lit is one
  // nobody reads, and the quiet state carrying information is the whole point.
  //
  // The dashboard renders its own copy of this panel, because it is hand-rolled
  // HTML that never loads this runtime. Neither copy holds any of the rules:
  // the store, the fold-with-a-count and the read semantics are all in
  // core/notices, and both only render what /api/notifications returns.

  // Drawn rather than typed. The glyph used to be the 🔔 emoji, which is not
  // one shape: every platform draws its own, at its own size, off its own
  // baseline, and a phone renders a large colour bitmap where a desktop
  // renders a small one. That is why it sat wrong on mobile — and a colour
  // emoji cannot be muted either, which is the whole design of this control:
  // quiet until there is something unread. At 0.55 opacity a colour bell goes
  // washed-out rather than muted, and the two read differently.
  //
  // Filled, no strokes, in the 64-unit viewBox the rest of the runtime's
  // glyphs use — checked by rasterizing to 16px and looking, which is how
  // anything this small gets judged here rather than by reasoning about it.
  // currentColor throughout, so opacity and the unread state do the work.
  function uiBellGlyph() {
    var ns = 'http://www.w3.org/2000/svg';
    var svg = document.createElementNS(ns, 'svg');
    svg.setAttribute('viewBox', '0 0 64 64');
    svg.setAttribute('class', 'ui-bell-glyph');
    svg.setAttribute('fill', 'currentColor');
    svg.setAttribute('aria-hidden', 'true');
    [
      'M32 6c-9 0-16 7-16 16v8c0 7-2 11-6 15-1 1 0 3 2 3h40c2 0 3-2 2-3-4-4-6-8-6-15v-8c0-9-7-16-16-16z',
      'M23 53h18c-1 6-4 9-9 9s-8-3-9-9z'
    ].forEach(function(d) {
      var path = document.createElementNS(ns, 'path');
      path.setAttribute('d', d);
      svg.appendChild(path);
    });
    return svg;
  }

  function uiNoticeBell() {
    var wrap = el('div', {class: 'ui-bell-wrap'});
    var count = el('span', {class: 'ui-bell-count'});
    var btn = el('button', {
      class: 'ui-bell', type: 'button', title: 'Notifications', 'aria-label': 'Notifications'
    }, [uiBellGlyph(), count]);
    var panel = el('div', {class: 'ui-bell-panel', style: 'display:none'});
    wrap.appendChild(btn);
    wrap.appendChild(panel);

    function post(url) {
      return fetch(url, {method: 'POST'}).then(load).catch(function() {});
    }

    function render(items) {
      panel.innerHTML = '';
      var head = el('div', {class: 'ui-bell-head'}, ['Notifications']);
      if (items.length) {
        // Only when there is something to act on. Two dead buttons over an
        // empty list is the same fault as a control offering a state its row
        // cannot hold.
        head.appendChild(el('button', {class: 'ui-bell-act', type: 'button',
          onclick: function() { post('/api/notifications/read'); }}, ['Mark all read']));
        // Clear is safe to offer because nothing here is the only record of
        // anything: a condition that is still true says so again on its next
        // occurrence. Confirmed anyway, since it acts on rows the reader may
        // not have scrolled to.
        head.appendChild(el('button', {class: 'ui-bell-act', type: 'button',
          onclick: function() {
            Promise.resolve(window.uiConfirm
              ? window.uiConfirm('Clear all notifications? Anything still happening will tell you again.')
              : window.confirm('Clear all notifications? Anything still happening will tell you again.')
            ).then(function(okd) { if (okd) { post('/api/notifications/dismiss'); } });
          }}, ['Clear all']));
      }
      panel.appendChild(head);
      if (!items.length) {
        panel.appendChild(el('div', {class: 'ui-bell-empty'}, ['Nothing yet.']));
        return;
      }
      items.forEach(function(it) {
        var row = el('div', {class: 'ui-bell-item' + (it.read ? '' : ' unread')});
        row.appendChild(el('div', {class: 'ui-bell-title'}, [it.title || '']));
        if (it.body) { row.appendChild(el('div', {class: 'ui-bell-body'}, [it.body])); }
        // The count is stated only when it is more than one. "1 time" on every
        // row is noise, and the repeating ones are what a reader is scanning
        // for.
        var when = it.when || '';
        if (it.count > 1) { when += ' · ' + it.count + ' times'; }
        var meta = el('div', {class: 'ui-bell-meta'}, [el('span', {}, [when])]);
        if (!it.read) {
          meta.appendChild(el('button', {class: 'ui-bell-act', type: 'button',
            onclick: function() { post('/api/notifications/read?id=' + encodeURIComponent(it.id)); }}, ['Mark read']));
        }
        meta.appendChild(el('button', {class: 'ui-bell-act', type: 'button',
          onclick: function() { post('/api/notifications/dismiss?id=' + encodeURIComponent(it.id)); }}, ['Dismiss']));
        row.appendChild(meta);
        panel.appendChild(row);
      });
    }

    function load() {
      return fetch('/api/notifications').then(function(r) {
        if (!r.ok) { throw new Error('unavailable'); }
        return r.json();
      }).then(function(d) {
        var n = (d && d.unread) || 0;
        // Hidden with VISIBILITY rather than display, matching the live pill:
        // a control that leaves the layout on its own schedule makes the header
        // jump every time it polls.
        wrap.style.visibility = 'visible';
        btn.className = 'ui-bell' + (n > 0 ? ' unread' : '');
        count.textContent = n > 99 ? '99+' : String(n);
        render((d && d.notices) || []);
      }).catch(function() {
        // A header that cannot reach the API still has to render. No bell is
        // the right answer here: an empty one would say "nothing to tell you",
        // which is a claim this cannot make when it could not ask.
        wrap.style.visibility = 'hidden';
      });
    }

    btn.onclick = function(ev) {
      ev.stopPropagation();
      var open = panel.style.display !== 'none';
      panel.style.display = open ? 'none' : 'block';
      if (!open) { load(); }
    };
    document.addEventListener('click', function(ev) {
      if (!wrap.contains(ev.target)) { panel.style.display = 'none'; }
    });

    load();
    // Slower than the live pill's ten seconds on purpose. The pill answers
    // "is something running right now"; this answers "did anything happen",
    // and nothing here is worth a request every ten seconds on every open tab.
    setInterval(load, 30000);
    return wrap;
  }
