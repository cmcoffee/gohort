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
  function uiNoticeBell() {
    var wrap = el('div', {class: 'ui-bell-wrap'});
    var count = el('span', {class: 'ui-bell-count'});
    var btn = el('button', {
      class: 'ui-bell', type: 'button', title: 'Notifications', 'aria-label': 'Notifications'
    }, ['\u{1F514}', count]);
    var panel = el('div', {class: 'ui-bell-panel', style: 'display:none'});
    wrap.appendChild(btn);
    wrap.appendChild(panel);

    function post(url) {
      return fetch(url, {method: 'POST'}).then(load).catch(function() {});
    }

    function render(items) {
      panel.innerHTML = '';
      var head = el('div', {class: 'ui-bell-head'}, ['Notifications']);
      var allBtn = el('button', {class: 'ui-bell-act', type: 'button',
        onclick: function() { post('/api/notifications/read'); }}, ['Mark all read']);
      head.appendChild(allBtn);
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
