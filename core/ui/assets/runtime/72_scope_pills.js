// uiRenderScopePills — a generic toggle-pill grid: one optional PRIMARY
// pill plus a list of item pills, each independently on/off. Domain-
// agnostic: the caller supplies a load() that returns the pill state and a
// toggle(key, on) that applies one change; this only renders and re-loads.
// Used for tool scope management (Global pill + one pill per agent), but it
// knows nothing about tools or agents — just labels and on/off.
//
//   uiRenderScopePills(container, {
//     load:   function() -> Promise<{ primary?:{label,on,disabled?}, items:[{key,label,on}], note?:string }>,
//     toggle: function(key, on) -> Promise,   // key === '__primary__' for the primary pill
//   })
(function () {
  function makePill(label, on, primary, onToggle) {
    var b = document.createElement('button');
    b.type = 'button';
    b.style.cssText =
      'border-radius:999px;padding:0.28rem 0.75rem;margin:0.18rem;font-size:0.8rem;' +
      'cursor:pointer;transition:background 0.12s,border-color 0.12s;border:1px solid ' +
      (on ? 'var(--accent,#6366f1)' : 'var(--border,#3a3a4a)') + ';background:' +
      (on ? 'var(--accent,#6366f1)' : 'transparent') + ';color:' +
      (on ? '#fff' : 'var(--text,#cfd0d8)') + (primary ? ';font-weight:600' : '');
    b.textContent = (on ? '✓ ' : '') + label;
    b.addEventListener('click', function () {
      if (b.disabled) return;
      b.disabled = true;
      b.style.opacity = '0.6';
      Promise.resolve(onToggle(!on)).catch(function (err) {
        b.disabled = false;
        b.style.opacity = '';
        if (window.uiAlert) window.uiAlert('Failed: ' + (err && err.message || err));
      });
    });
    return b;
  }

  window.uiRenderScopePills = function (container, opts) {
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
        wrap.style.cssText = 'display:flex;flex-wrap:wrap;align-items:center';
        if (state.primary) {
          var p = makePill(state.primary.label, state.primary.on, true, function (newOn) {
            return Promise.resolve(opts.toggle('__primary__', newOn)).then(render);
          });
          if (state.primary.disabled) { p.disabled = true; p.style.opacity = '0.5'; p.style.cursor = 'not-allowed'; }
          wrap.appendChild(p);
          // A thin divider between the primary pill and the per-item pills.
          var sep = document.createElement('span');
          sep.style.cssText = 'width:1px;align-self:stretch;background:var(--border,#3a3a4a);margin:0.3rem 0.5rem';
          wrap.appendChild(sep);
        }
        (state.items || []).forEach(function (it) {
          wrap.appendChild(makePill(it.label, it.on, false, function (newOn) {
            return Promise.resolve(opts.toggle(it.key, newOn)).then(render);
          }));
        });
        if (!(state.items && state.items.length) && !state.primary) {
          var empty = document.createElement('div');
          empty.style.cssText = 'color:var(--text-mute,#888);font-size:0.82rem';
          empty.textContent = '(nothing to configure)';
          wrap.appendChild(empty);
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
