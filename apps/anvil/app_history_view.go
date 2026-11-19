package anvil

// An app project's history (docs/design/app-history.md, stage 3, S5): every
// version the app went live as, who made it and with which agent, what each
// changed, and going back to one. The history is the app's own
// (orchestrate.AppHistoryLog), the same Builder reads; going back is the
// person's own change, through every gate a save passes, and this checkout
// then takes it as it takes any change made to the app elsewhere.

import (
	"errors"
	"strings"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// handleAppHistory lists the app's versions, newest first.
func (T *Anvil) handleAppHistory(c *jsonapi.Call) (any, error) {
	p, _, err := T.appProjectOf(c)
	if err != nil {
		return nil, err
	}
	list, err := orchestrate.AppHistoryLog(c.User, p.AppSlug, 0)
	if errors.Is(err, orchestrate.ErrNoAppHistory) {
		return map[string]any{"commits": []orchestrate.AppCommit{}, "none": err.Error()}, nil
	}
	if err != nil {
		return map[string]any{"commits": []orchestrate.AppCommit{}, "none": err.Error()}, nil
	}
	return map[string]any{"commits": list}, nil
}

// handleAppHistoryDiff is what one version (?commit=) changed.
func (T *Anvil) handleAppHistoryDiff(c *jsonapi.Call) (any, error) {
	p, _, err := T.appProjectOf(c)
	if err != nil {
		return nil, err
	}
	diff, err := orchestrate.AppHistoryDiff(c.User, p.AppSlug, c.R.URL.Query().Get("commit"))
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"diff": diff}, nil
}

// handleAppHistoryRestore POSTs {commit} and makes the app as that version
// had it live again, then brings this checkout up to it, keeping the work in
// progress on top as an install does.
func (T *Anvil) handleAppHistoryRestore(c *jsonapi.Call, body struct {
	Commit string `json:"commit"`
}) (any, error) {
	p, base, err := T.appProjectOf(c)
	if err != nil {
		return nil, err
	}
	o, err := appHost()
	if err != nil {
		return nil, jsonapi.Status(503, err.Error())
	}
	msg, err := o.RestoreAppVersion(c.R.Context(), c.User, p.AppSlug, strings.TrimSpace(body.Commit))
	if broken, ok := savedBroken(err); ok {
		return map[string]any{"ok": true, "message": msg, "broken": broken.Problem}, nil
	}
	if err != nil {
		return nil, jsonapi.Status(409, err.Error())
	}
	if _, err := takeAppChanges(c.R.Context(), c.User, p.AppSlug, base); err != nil {
		Log("[anvil] %s/%s went back a version, but the checkout could not take it: %v", c.User, p.AppSlug, err)
		msg += " This project's files still hold your work from before; its next Install brings the two together: " + err.Error()
	}
	return map[string]any{"ok": true, "message": msg}, nil
}

// anvilAppHistoryJS is the History modal: the app's versions, newest first,
// each opening to what it changed, and each but the live one offering to go
// back to it. Anvil's own diff rows and painter, as Changes uses them.
const anvilAppHistoryJS = `function(ctx) {
  var m = window.uiOpenModal({title: 'History', subtitle: window.anvilProjectName(), width: '820px'});
  var body = m.body;
  var pid = window.anvilRequireProject(body);
  if (!pid) return;
  var api = 'api/project/' + encodeURIComponent(pid) + '/app_history';

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }
  function when(iso) {
    var d = new Date(iso);
    return isNaN(d.getTime()) ? iso : d.toLocaleString();
  }

  function load() {
    body.innerHTML = '';
    body.appendChild(window.anvilNote('Loading the app\'s history...'));
    window.anvilFetchJSON(api).then(function(res) {
      body.innerHTML = '';
      var list = (res && res.commits) || [];
      if (!list.length) {
        body.appendChild(window.anvilNote(res && res.none ? res.none : 'The app has no history yet: it starts at its next change.'));
        return;
      }
      body.appendChild(window.anvilNote('Every version the app went live as, newest first. Open one to see what it changed. Going back makes the app as that version had it live again, as a change of your own: the version it replaces stays here, to go back to the same way.'));
      list.forEach(function(c) {
        var wrap = el('div', 'anvil-edit');
        var head = el('div', 'anvil-edit-head');
        var title = el('span', 'anvil-edit-path', c.subject || '(no message)');
        title.title = c.body || c.subject || '';
        head.appendChild(title);
        var who = c.author + (c.agent ? ' with ' + c.agent : '');
        head.appendChild(el('span', 'anvil-edit-stat', c.short + '  -  ' + when(c.when) + '  -  ' + who + (c.live ? '  -  live' : '')));
        var bodyEl = el('div', 'anvil-edit-body');
        if (c.body) bodyEl.appendChild(el('div', 'anvil-note', c.body));
        if (!c.live) {
          var back = el('button', 'ui-row-btn', 'Go back to this version');
          back.addEventListener('click', function(ev) {
            ev.stopPropagation();
            window.anvilConfirm('Go back to the version of ' + when(c.when) + '? The app becomes as it was then, through its checks, for everyone using it; this project\'s work in progress is kept on top.').then(function(ok) {
              if (!ok) return;
              back.disabled = true;
              back.textContent = 'Going back...';
              window.anvilFetchJSON(api + '/restore', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({commit: c.hash})})
                .then(function(d) {
                  window.anvilAlert(((d && d.message) || 'Done.') + (d && d.broken ? ' But it does not work as it is: ' + d.broken : ''));
                  load();
                })
                .catch(function(err) {
                  back.disabled = false;
                  back.textContent = 'Go back to this version';
                  window.anvilAlert('Not changed: ' + (err.message || err));
                });
            });
          });
          bodyEl.appendChild(back);
        }
        var pre = document.createElement('pre');
        bodyEl.appendChild(pre);
        var loaded = false;
        head.addEventListener('click', function() {
          wrap.classList.toggle('open');
          if (loaded || !wrap.classList.contains('open')) return;
          loaded = true;
          pre.textContent = 'Loading...';
          window.anvilFetchJSON(api + '/diff?commit=' + encodeURIComponent(c.hash))
            .then(function(d) { pre.innerHTML = ''; window.anvilPaintDiff(pre, (d && d.diff) || '(no textual difference)'); })
            .catch(function(err) { pre.textContent = String(err.message || err); });
        });
        wrap.appendChild(head);
        wrap.appendChild(bodyEl);
        body.appendChild(wrap);
      });
    }).catch(function(err) {
      body.innerHTML = '';
      body.appendChild(window.anvilNote('Could not read the history: ' + (err.message || err)));
    });
  }
  load();
}`
