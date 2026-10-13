package admin

// The Delete button on a user. It used to send a bare DELETE, which the server
// refused whenever an app still held the person's data ("resolve via
// data-action"), with nothing on the page that could resolve it.
//
// Now it asks what happens to what they own first: each app that holds some
// says how much, and how much of it reaches other people, and the
// administrator moves everything to somebody or deletes it. Moving is the
// default when something is shared, because deleting that breaks it for the
// people using it. Their own conversations, memory and records go with the
// account either way: those are nobody else's to inherit.
//
// No backticks in this string: it is a Go raw literal.
const deleteUserScript = `<script>
(function() {
  function register() {
    if (!window.uiRegisterClientAction) return;
    window.uiRegisterClientAction('admin_delete_user', function(ctx) {
      var rec = ctx.record || {};
      var user = rec.username || rec.name || '';
      if (!user) return;
      var base = 'api/users/' + encodeURIComponent(user);
      function getJSON(u) {
        return fetch(u, {credentials: 'same-origin'}).then(function(r) {
          if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
          return r.json();
        });
      }
      function finish() {
        return fetch(base + '?force=1', {method: 'DELETE', credentials: 'same-origin'}).then(function(r) {
          if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
          if (ctx.reload) ctx.reload();
        });
      }
      Promise.all([getJSON(base + '/data'), getJSON('api/users')]).then(function(res) {
        var held = (res[0] || []).filter(function(s) {
          var c = s.counts || {};
          return Object.keys(c).some(function(k) { return c[k] > 0; });
        });
        var others = (res[1] || []).map(function(u) { return u.username || u.name; })
          .filter(function(n) { return n && n !== user; });
        var shared = 0, schedules = 0;
        held.forEach(function(s) {
          shared += (s.counts || {}).shared || 0;
          schedules += (s.counts || {}).schedules || 0;
        });
        window.uiOpenModal({
          title: 'Delete ' + user,
          width: '520px',
          mount: function(body) {
            function line(text, mute) {
              var d = document.createElement('div');
              d.style.cssText = 'margin:0 0 0.45rem;line-height:1.45' + (mute ? ';color:var(--text-mute);font-size:var(--fs-sm)' : '');
              d.textContent = text;
              body.appendChild(d);
              return d;
            }
            line('Their sessions, keys and connected accounts are revoked, and their own conversations, memory and records are deleted with the account.', true);
            if (!held.length) {
              line('They own nothing anyone else would notice.');
              body.dataset.choice = 'none';
              return;
            }
            line('What they built:');
            var ul = document.createElement('ul');
            ul.style.cssText = 'margin:0 0 0.8rem 1.1rem;padding:0;line-height:1.5';
            held.forEach(function(s) {
              var c = s.counts || {}, parts = [];
              Object.keys(c).sort().forEach(function(k) {
                if (k !== 'shared' && c[k] > 0) parts.push(c[k] + ' ' + k);
              });
              var li = document.createElement('li');
              li.textContent = s.app + ': ' + parts.join(', ') + (c.shared ? ' (' + c.shared + ' shared with others)' : '');
              ul.appendChild(li);
            });
            body.appendChild(ul);
            function radio(value, label, checked) {
              var w = document.createElement('label');
              w.style.cssText = 'display:flex;align-items:center;gap:0.5rem;margin:0.35rem 0;cursor:pointer';
              var r = document.createElement('input');
              r.type = 'radio'; r.name = 'deluser-choice'; r.value = value; r.checked = !!checked;
              r.addEventListener('change', function() { body.dataset.choice = value; });
              w.appendChild(r);
              w.appendChild(document.createTextNode(label));
              body.appendChild(w);
              return w;
            }
            var mv = radio('move', 'Move everything to ', true);
            var sel = document.createElement('select');
            sel.className = 'ui-input';
            sel.style.cssText = 'margin-left:0.25rem;max-width:16rem';
            others.forEach(function(n) {
              var o = document.createElement('option'); o.value = n; o.textContent = n; sel.appendChild(o);
            });
            mv.appendChild(sel);
            body.dataset.choice = 'move';
            radio('purge', 'Delete everything', false);
            if (shared) {
              line(shared + ' of these reach other people: deleting them stops them working for those people. Moving keeps them running, as the new owner.', true);
            } else {
              line('Moving makes the new owner responsible for them; their scripts and tools then run as that person.', true);
            }
            if (schedules) {
              line('Their ' + schedules + ' schedule' + (schedules === 1 ? '' : 's') + ' stop either way: a schedule runs as a person, so the new owner turns back on what they want running in their name.', true);
            }
            body.dataset.target = sel.value;
            sel.addEventListener('change', function() { body.dataset.target = sel.value; });
            if (!others.length) {
              mv.style.opacity = '0.5';
              mv.querySelector('input').disabled = true;
              body.querySelector('input[value=purge]').checked = true;
              body.dataset.choice = 'purge';
            }
          },
          actions: [
            {label: 'Cancel', onClick: function(api) { api.close(); }},
            {label: 'Delete ' + user, primary: true, variant: 'danger', onClick: function(api) {
              var body = api.body || document.querySelector('.ui-modal-body');
              var choice = (body && body.dataset.choice) || 'none';
              var target = (body && body.dataset.target) || '';
              if (choice === 'move' && !target) { window.uiAlert('Pick who to move them to.'); return; }
              var steps = Promise.resolve();
              held.forEach(function(s) {
                var acts = s.actions || [];
                var action = (choice === 'move' && acts.indexOf('reassign') >= 0) ? 'reassign' : 'purge';
                steps = steps.then(function() {
                  return fetch(base + '/data-action', {
                    method: 'POST', credentials: 'same-origin',
                    headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify({app: s.app, action: action, target: action === 'reassign' ? target : ''})
                  }).then(function(r) {
                    if (!r.ok) return r.text().then(function(t) {
                      t = (t || '').trim();
                      // Every app is checked before the first one moves; the
                      // refusal names its own app, whichever call met it.
                      if (/^nothing was moved:/.test(t)) { var e = new Error(t); e.untouched = true; throw e; }
                      throw new Error(s.app + ': ' + (t || ('HTTP ' + r.status)));
                    });
                  });
                });
              });
              steps.then(finish).then(function() { api.close(); }).catch(function(e) {
                if (e.untouched) {
                  window.uiAlert('Not deleted, and ' + e.message.replace(/^nothing was moved: /, 'nothing was moved. ') + '. Fix that, or pick someone else, and try again.');
                  return;
                }
                window.uiAlert('Not deleted: ' + e.message + '. Nothing after that step ran; the account is still there.');
              });
            }}
          ]
        });
      }).catch(function(e) {
        window.uiAlert('Could not load what ' + user + ' owns: ' + e.message);
      });
    });
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', register);
  } else {
    register();
  }
})();
</` + `script>`
