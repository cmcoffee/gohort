  // stage_tracker: where a long job is (see ui.StageTracker). Every stage in
  // order with its mark, count and detail; the running stage's items; then
  // what it is doing now and the last thing worth noticing. Polled, so a page
  // arriving mid-run shows exactly what one that watched from the start does.
  components.stage_tracker = function(cfg) {
    var wrap = el('div', {class: 'ui-stages'});
    var frames = '⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏', fi = 0;
    var spinners = [];
    // Both timers end once the tracker has been on the page and left it.
    var attached = false, stopPoll = function() {};
    var spin = setInterval(function() {
      if (wrap.isConnected) {
        attached = true;
      } else if (attached) {
        clearInterval(spin);
        stopPoll();
        return;
      }
      fi++;
      var f = frames.charAt(fi % frames.length);
      for (var i = 0; i < spinners.length; i++) spinners[i].textContent = f;
    }, 120);
    function mark(state) {
      var m = el('span', {class: 'ui-stage-mark ' + (state || 'pending')});
      switch (state) {
        case 'done': m.textContent = '✓'; break;
        case 'failed': m.textContent = '✗'; break;
        case 'skipped': m.textContent = '–'; break;
        case 'running': m.textContent = frames.charAt(0); spinners.push(m); break;
        default: m.textContent = '○';
      }
      return m;
    }
    function row(s, cls) {
      var li = el('li', {class: cls + ' ' + (s.state || 'pending')});
      var line = el('div', {class: 'ui-stage-line'}, [mark(s.state), el('span', {class: 'ui-stage-label'}, [s.label || ''])]);
      if (s.count) line.appendChild(el('span', {class: 'ui-stage-count'}, [s.count]));
      li.appendChild(line);
      if (s.detail) li.appendChild(el('div', {class: 'ui-stage-detail'}, [s.detail]));
      if (s.items && s.items.length) {
        var ul = el('ul', {class: 'ui-stage-items'});
        s.items.forEach(function(it) { ul.appendChild(row(it, 'ui-stage-item')); });
        li.appendChild(ul);
      }
      return li;
    }
    function render(d) {
      spinners = [];
      wrap.innerHTML = '';
      if (!d || !d.stages || !d.stages.length) {
        wrap.appendChild(el('div', {class: 'ui-stages-empty'}, [cfg.empty_text || 'Nothing running.']));
        return;
      }
      var head = el('div', {class: 'ui-stages-head'});
      if (d.running) {
        var s = el('span', {class: 'ui-stages-spin'}, [frames.charAt(fi % frames.length)]);
        spinners.push(s);
        head.appendChild(s);
      }
      head.appendChild(el('span', {class: 'ui-stages-title'}, [d.title || '']));
      var meta = [d.elapsed, d.meta].filter(Boolean).join(' · ');
      if (meta) head.appendChild(el('span', {class: 'ui-stages-meta'}, [meta]));
      wrap.appendChild(head);
      if (d.note) wrap.appendChild(el('div', {class: 'ui-stages-note'}, [d.note]));
      var ol = el('ol', {class: 'ui-stages-list'});
      d.stages.forEach(function(st) { ol.appendChild(row(st, 'ui-stage')); });
      wrap.appendChild(ol);
      if (d.running && d.now) wrap.appendChild(el('div', {class: 'ui-stages-now'}, [el('b', {}, ['Now']), ' ', d.now]));
      if (d.last) wrap.appendChild(el('div', {class: 'ui-stages-last'}, [el('b', {}, ['Last']), ' ', d.last]));
      if (!d.running && d.outcome) wrap.appendChild(el('div', {class: 'ui-stages-outcome'}, [d.outcome]));
    }
    function load() {
      return fetchJSON(cfg.source).then(render).catch(function() {});
    }
    load();
    stopPoll = uiAutoRefresh(cfg.refresh_ms || 2000, load);
    return wrap;
  };
