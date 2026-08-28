// uiRenderScopePills — a generic toggle-pill grid: one optional PRIMARY
// pill plus a list of item pills, each independently on/off. Domain-
// agnostic: the caller supplies a load() that returns the pill state and a
// toggle(key, on) that applies one change; this only renders and re-loads.
// Used for tool scope management (Global pill + one pill per agent), but it
// knows nothing about tools or agents — just labels and on/off.
//
//   uiRenderScopePills(container, {
//     load:   function() -> Promise<{ primary?:{label,on,disabled?}, items:[{key,label,on,under?}], note?:string }>,
//     toggle: function(key, on) -> Promise,   // key === '__primary__' for the primary pill
//   })
//
// An item may carry `under: <key of another item>`, which renders it on an
// indented row beneath that item instead of inline. Each pill is still an
// independent toggle — nesting is presentation, not containment, so switching
// a parent on says nothing about its children. Items whose `under` names no
// known key fall back to the top row rather than disappearing.
(function () {
  // A pill has three states, not two. `partial` is the third: some of what this
  // pill covers is on and some is not — a group whose members disagree. It
  // cannot render as either on or off without misreporting the other half, so
  // it gets its own look (dashed outline, accent text, a dash instead of a
  // tick) and clicking it commits everything to ON, the way an indeterminate
  // checkbox resolves.
  function makePill(label, on, primary, onToggle, partial) {
    var b = document.createElement('button');
    b.type = 'button';
    var border = on ? 'var(--accent,#6366f1)' : (partial ? 'var(--accent,#6366f1)' : 'var(--border,#3a3a4a)');
    var color = on ? '#fff' : (partial ? 'var(--accent,#6366f1)' : 'var(--text,#cfd0d8)');
    b.style.cssText =
      'border-radius:999px;padding:0.28rem 0.75rem;margin:0.18rem;font-size:0.8rem;' +
      'cursor:pointer;transition:background 0.12s,border-color 0.12s;border:1px ' +
      (partial && !on ? 'dashed ' : 'solid ') + border + ';background:' +
      (on ? 'var(--accent,#6366f1)' : 'transparent') + ';color:' + color +
      (primary ? ';font-weight:600' : '');
    b.textContent = (on ? '✓ ' : (partial ? '– ' : '')) + label;
    if (partial && !on) b.title = 'Some are on and some are not — click to turn all on';
    b.addEventListener('click', function () {
      if (b.disabled) return;
      b.disabled = true;
      b.style.opacity = '0.6';
      // From partial, the useful move is "make them all match" rather than a
      // toggle of a state that was never uniform.
      Promise.resolve(onToggle(partial && !on ? true : !on)).catch(function (err) {
        b.disabled = false;
        b.style.opacity = '';
        if (window.uiAlert) window.uiAlert('Failed: ' + (err && err.message || err));
      });
    });
    return b;
  }

  window.uiRenderScopePills = function (container, opts) {
    // Individual pills collapse behind an expander while the PRIMARY pill is
    // ON — when "everything" is granted, per-item overrides are the rare case
    // and a wall of pills is noise. Expansion sticks for this panel instance
    // (survives the re-render after each toggle). Generic: knows nothing
    // about what the items are.
    var itemsExpanded = false;
    function render() {
      container.innerHTML = '';
      var loading = document.createElement('div');
      loading.style.cssText = 'color:var(--text-mute,#888);font-size:0.82rem;padding:0.4rem 0';
      loading.textContent = 'Loading…';
      container.appendChild(loading);
      Promise.resolve(opts.load()).then(function (state) {
        container.innerHTML = '';
        if (state.note) {
          var n = document.createElement('div');
          n.style.cssText = 'color:var(--text-mute,#888);font-size:0.8rem;margin:0 0 0.5rem 0;line-height:1.4';
          n.textContent = state.note;
          container.appendChild(n);
        }
        var wrap = document.createElement('div');
        wrap.style.cssText = 'display:flex;flex-direction:column;align-items:stretch';
        var top = document.createElement('div');
        top.style.cssText = 'display:flex;flex-wrap:wrap;align-items:center';
        wrap.appendChild(top);
        if (state.primary) {
          var p = makePill(state.primary.label, state.primary.on, true, function (newOn) {
            return Promise.resolve(opts.toggle('__primary__', newOn)).then(render);
          }, state.primary.partial);
          if (state.primary.disabled) { p.disabled = true; p.style.opacity = '0.5'; p.style.cursor = 'not-allowed'; }
          top.appendChild(p);
          // A thin divider between the primary pill and the per-item pills.
          var sep = document.createElement('span');
          sep.style.cssText = 'width:1px;align-self:stretch;background:var(--border,#3a3a4a);margin:0.3rem 0.5rem';
          top.appendChild(sep);
        }
        var items = state.items || [];
        if (state.primary && state.primary.on && items.length && !itemsExpanded) {
          var more = document.createElement('button');
          more.type = 'button';
          more.style.cssText =
            'border:none;background:none;color:var(--text-mute,#888);cursor:pointer;' +
            'font-size:0.78rem;padding:0.28rem 0.4rem;text-decoration:underline dotted';
          more.textContent = '▸ Per-item overrides (' + items.length + ')';
          more.addEventListener('click', function () { itemsExpanded = true; render(); });
          top.appendChild(more);
          container.appendChild(wrap);
          return;
        }
        var known = {}, kids = {};
        items.forEach(function (it) { known[it.key] = true; });
        items.forEach(function (it) {
          if (it.under && known[it.under]) (kids[it.under] = kids[it.under] || []).push(it);
        });
        function pillFor(it) {
          return makePill(it.label, it.on, false, function (newOn) {
            return Promise.resolve(opts.toggle(it.key, newOn)).then(render);
          }, it.partial);
        }
        items.forEach(function (it) {
          if (it.under && known[it.under]) return;   // rendered under its parent
          if (!kids[it.key]) { top.appendChild(pillFor(it)); return; }
          // Parent with children gets its own block so the indented child row
          // sits directly beneath it instead of wrapping into the top row.
          var block = document.createElement('div');
          block.style.cssText = 'margin:0.35rem 0 0.1rem 0';
          block.appendChild(pillFor(it));
          var row = document.createElement('div');
          row.style.cssText =
            'display:flex;flex-wrap:wrap;align-items:center;margin:0.1rem 0 0 0.9rem;' +
            'padding-left:0.6rem;border-left:1px solid var(--border,#3a3a4a)';
          kids[it.key].forEach(function (k) { row.appendChild(pillFor(k)); });
          block.appendChild(row);
          wrap.appendChild(block);
        });
        if (!items.length && !state.primary) {
          var empty = document.createElement('div');
          empty.style.cssText = 'color:var(--text-mute,#888);font-size:0.82rem';
          empty.textContent = '(nothing to configure)';
          top.appendChild(empty);
        }
        container.appendChild(wrap);
      }).catch(function (err) {
        container.innerHTML = '';
        var e = document.createElement('div');
        e.style.cssText = 'color:var(--danger,#e5534b);font-size:0.82rem';
        e.textContent = 'Could not load scope: ' + (err && err.message || err);
        container.appendChild(e);
      });
    }
    render();
  };
})();
