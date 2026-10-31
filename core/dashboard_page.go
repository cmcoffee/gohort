package core

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"slices"
	"strings"

	"github.com/cmcoffee/gohort/core/ui"
)

// bellGlyphSVG is the same bell the framework header draws, inline because
// this page is hand-rolled HTML that never loads the runtime.
//
// Drawn rather than typed. The emoji this replaced is not one shape: every
// platform draws its own, at its own size, off its own baseline, so it sat
// differently on every device — and a colour emoji cannot be muted, which is
// the whole design here. At 0.55 opacity a colour bell goes washed out rather
// than quiet, and washed out reads as broken.
//
// Kept identical to core/ui/assets/runtime/71_notice_bell.js on purpose: the
// same control in two places that look different is worse than either.
const bellGlyphSVG = `<svg viewBox="0 0 64 64" fill="currentColor" aria-hidden="true">` +
	`<path d="M32 6c-9 0-16 7-16 16v8c0 7-2 11-6 15-1 1 0 3 2 3h40c2 0 3-2 2-3-4-4-6-8-6-15v-8c0-9-7-16-16-16z"/>` +
	`<path d="M23 53h18c-1 6-4 9-9 9s-8-3-9-9z"/></svg>`

// notifyPanelHTML is the bell's dropdown and the script that fills it.
//
// Plain markup and a fetch, not a component: the dashboard is hand-rolled HTML
// and does not load the ui runtime, so it has no bundle to hang one on.
//
// This is therefore a SECOND presentation of the same thing the shared page
// header renders (core/ui/assets/runtime/71_notice_bell.js), and the two will
// drift. What they must not drift on lives in neither of them: the store, the
// fold-with-a-count rule and the read semantics are all in core/notices, and
// both of these only render what /api/notifications returns. Keep it that way,
// and a difference between them stays cosmetic.
const notifyPanelHTML = `<div class="notify-panel" id="notify-panel">
  <div class="notify-head"><span>Notifications</span><span><button type="button" id="notify-all">Mark all read</button> <button type="button" id="notify-clear">Clear all</button></span></div>
  <div id="notify-list"><div class="notify-empty">Nothing yet.</div></div>
</div>
<script>
(function() {
  var bell = document.getElementById('bell');
  var panel = document.getElementById('notify-panel');
  var list = document.getElementById('notify-list');
  var count = document.getElementById('bell-count');
  if (!bell || !panel) { return; }
  function esc(s) { var d = document.createElement('div'); d.textContent = s == null ? '' : s; return d.innerHTML; }
  function post(url) { return fetch(url, {method: 'POST'}).then(load); }
  function load() {
    return fetch('/api/notifications').then(function(r) { return r.json(); }).then(function(d) {
      var n = d.unread || 0;
      bell.classList.toggle('unread', n > 0);
      count.textContent = n > 99 ? '99+' : String(n);
      var items = d.notices || [];
      if (!items.length) { list.innerHTML = '<div class="notify-empty">Nothing yet.</div>'; return; }
      list.innerHTML = items.map(function(it) {
        // The count is stated only when it is more than one: "1 time" on every
        // row is noise, and the chronic ones are what a reader is scanning for.
        var times = it.count > 1 ? ' - ' + it.count + ' times' : '';
        return '<div class="notify-item' + (it.read ? '' : ' unread') + '">' +
          '<div class="notify-title">' + esc(it.title) + '</div>' +
          (it.body ? '<div class="notify-body">' + esc(it.body) + '</div>' : '') +
          '<div class="notify-meta"><span>' + esc(it.when) + times + '</span>' +
          (it.read ? '' : '<button type="button" data-read="' + esc(it.id) + '">Mark read</button>') +
          '<button type="button" data-dismiss="' + esc(it.id) + '">Dismiss</button></div></div>';
      }).join('');
    }).catch(function() { /* a dashboard that cannot reach its own API still has to render */ });
  }
  bell.onclick = function() { panel.classList.toggle('open'); if (panel.classList.contains('open')) { load(); } };
  document.addEventListener('click', function(e) {
    if (!panel.contains(e.target) && e.target !== bell) { panel.classList.remove('open'); }
  });
  list.addEventListener('click', function(e) {
    var id = e.target && (e.target.getAttribute('data-read') || e.target.getAttribute('data-dismiss'));
    if (!id) { return; }
    post(e.target.hasAttribute('data-read')
      ? '/api/notifications/read?id=' + encodeURIComponent(id)
      : '/api/notifications/dismiss?id=' + encodeURIComponent(id));
  });
  document.getElementById('notify-all').onclick = function() { post('/api/notifications/read'); };
  // Safe to offer: nothing here is the only record of anything, and a condition
  // that is still true says so again on its next occurrence. Confirmed anyway,
  // since it acts on rows the reader may not have scrolled to.
  document.getElementById('notify-clear').onclick = function() {
    if (window.confirm('Clear all notifications? Anything still happening will tell you again.')) {
      post('/api/notifications/dismiss');
    }
  };
  load();
  setInterval(load, 30000);
})();
</script>`

// gearGlyphSVG is the section gear: a cog, drawn in the current colour.
const gearGlyphSVG = `<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>`

// bandGear is a section's gear button, carrying how many of its cards are
// new to the viewer when any are.
func bandGear(name string, fresh int) string {
	e := html.EscapeString(name)
	badge := ""
	if fresh > 0 {
		badge = fmt.Sprintf(`<span class="band-new">%d new</span>`, fresh)
	}
	return fmt.Sprintf(`<button type="button" class="band-gear" data-band="%s" title="Arrange %s" aria-label="Arrange %s">%s%s</button>`, e, e, e, gearGlyphSVG, badge)
}

// emptyBand is a section with nothing showing: its heading and gear, and a
// line saying where its cards are chosen.
func emptyBand(name, gear string) string {
	return fmt.Sprintf(`<div class="cluster band empty"><div class="cluster-head"><span>%s</span>%s</div><div class="band-empty">Nothing shown here. The gear chooses what this section shows.</div></div>`,
		html.EscapeString(name), gear)
}

// sectionWindowHTML is what a gear opens: the section's cards in the same
// draggable table the framework draws everywhere else (dashSectionTable),
// with a switch for the whole section. The dashboard is hand-written markup
// and does not load the framework runtime for its first paint; the gear
// fetches it the first time it is used, putting its stylesheet ahead of the
// dashboard's own so nothing on the page changes look. Closing the window
// counts the section's cards as seen and redraws the page.
func sectionWindowHTML() string {
	cfg, _ := json.Marshal(dashSectionTable())
	return `<script id="band-table" type="application/json">` + strings.ReplaceAll(string(cfg), "</", "<\\/") + `</script>
<script>
(function() {
  var tableCfg = JSON.parse(document.getElementById('band-table').textContent);
  var waiting = null;
  function withUI(done, fail) {
    if (window.uiOpenModal && window.uiMountComponent) { done(); return; }
    if (waiting) { waiting.push([done, fail]); return; }
    waiting = [[done, fail]];
    var css = document.createElement('link');
    css.rel = 'stylesheet';
    css.href = '/_ui/ui.css';
    document.head.insertBefore(css, document.head.querySelector('style'));
    var js = document.createElement('script');
    js.src = '/_ui/ui.js';
    js.onload = function() { var q = waiting; waiting = null; q.forEach(function(p) { p[0](); }); };
    js.onerror = function() { var q = waiting; waiting = null; q.forEach(function(p) { p[1](); }); };
    document.body.appendChild(js);
  }
  function post(url, body) {
    return fetch(url, {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body || {})});
  }
  function openSection(name, btn) {
    btn.classList.add('busy');
    withUI(function() {
      btn.classList.remove('busy');
      var cfg = JSON.parse(JSON.stringify(tableCfg));
      cfg.source = cfg.source + encodeURIComponent(name);
      window.uiOpenModal({
        title: name,
        subtitle: 'Drag a card by its grip to reorder it, or switch it off to take it off your dashboard. Cards you have not seen yet are marked New.',
        width: '680px',
        mount: function(body) {
          var row = document.createElement('label');
          row.className = 'band-switch';
          var cb = document.createElement('input');
          cb.type = 'checkbox';
          cb.className = 'ui-switch';
          cb.checked = true;
          cb.addEventListener('change', function() {
            post('/api/dashboard/band?name=' + encodeURIComponent(name), {shown: cb.checked});
          });
          row.appendChild(cb);
          row.appendChild(document.createTextNode(' Show this section on the dashboard'));
          body.appendChild(row);
          window.uiMountComponent(cfg, body);
        },
        onClose: function() {
          var again = function() { location.reload(); };
          post('/api/dashboard/seen?band=' + encodeURIComponent(name)).then(again, again);
        }
      });
    }, function() {
      btn.classList.remove('busy');
      btn.title = 'Could not load the arranging tools: reload the page and try again.';
    });
  }
  document.addEventListener('click', function(e) {
    var t = e.target;
    var g = t.closest && t.closest('.band-gear');
    if (g) {
      e.preventDefault();
      e.stopPropagation();
      openSection(g.getAttribute('data-band'), g);
      return;
    }
    var s = t.closest && t.closest('.band-show');
    if (s) {
      var again = function() { location.reload(); };
      post('/api/dashboard/band?name=' + encodeURIComponent(s.getAttribute('data-band')), {shown: true}).then(again, again);
    }
  });
})();
</script>`
}

func serve_dashboard(w http.ResponseWriter, r *http.Request, apps []dashApp, notices []DashboardNotice, bands dashBands) {
	cards := dashboardCards(apps, bands)

	// Detect logged-in user for the auth bar.
	auth_html := dashboardAuthBar(AuthCurrentUser(r))

	html := dashboardPageHTML
	html = strings.Replace(html, "%THEME%", ui.ActiveTheme(), 1)
	html = strings.Replace(html, "%APPHEAD%", ui.AppHead(ui.ActiveTheme()), 1)
	html = strings.Replace(html, "%THEMECSS%", ui.ThemeCSS(), 1)
	html = strings.Replace(html, "%FAVICON%", faviconLinkTag, 1)
	// Before the notices, the account bar and the cards, which carry text
	// people typed: filled first, its placeholder is still the template's own.
	html = strings.Replace(html, "%SECTIONS%", sectionWindowHTML(), 1)
	// Notices sit above the cards, because the point is to be read before the
	// grid is used — an admin who scrolls past them has already clicked into
	// the app that cannot work yet.
	html = strings.Replace(html, "%NOTICES%", dashboardNotices(notices), 1)
	html = strings.Replace(html, "%AUTH%", auth_html, 1)
	html = strings.Replace(html, "%CARDS%", cards, 1)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Drawn from live state on every request, as a ui.Page is. With no header
	// a browser may serve its earlier copy when the back arrow returns here.
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, html)
}

// dashboardPageHTML is the dashboard page, filled in by serve_dashboard at
// its %PLACEHOLDER% marks.
//
//go:embed assets/dashboard_page.html
var dashboardPageHTML string

// renderDashCard writes one app's card.
func renderDashCard(b *strings.Builder, a dashApp, extraCls string) {
	// Escaped: a published agent's name and description are typed by its
	// owner and shown on other people's dashboards, and a custom app's by
	// whoever wrote it. A link carrying a query opens as written.
	href := a.path
	if !strings.Contains(href, "?") {
		href += "/"
	}
	// The description is held to two lines (.card-desc), so the whole of it
	// rides on the card's title for hovering.
	fmt.Fprintf(b, `<a class="card%s" href="%s" title="%s">
			<div class="card-name">%s</div>
			<div class="card-desc">%s</div>
		</a>`, extraCls, html.EscapeString(href), html.EscapeString(a.desc), html.EscapeString(a.name), html.EscapeString(a.desc))
}

// dashCards is the dashboard's cards sorted into where they are drawn: the
// standalone heroes, the Orchestrator family, a builder per named band in the
// order the bands first appear, and the cards that come after every band.
type dashCards struct {
	heroB, lastB strings.Builder
	family       []dashApp
	bandNames    []string
	bandCards    map[string]*strings.Builder
}

// dashboardCards is the cards part of the page.
func dashboardCards(apps []dashApp, bands dashBands) string {
	dc := &dashCards{bandCards: map[string]*strings.Builder{}}
	dc.sort(apps)
	return dc.draw(bands)
}

// Partition: the orchestrator family, standalone featured heroes, and the rest.
// The family is exactly the apps that ALSO appear as shared top-nav tabs —
// membership is "implements WebAppHubTab", the SAME single source HubNav reads,
// so no new metadata. They render as one titled cluster, so the dashboard
// mirrors the tab-row grouping instead of scattering these among unrelated
// apps. A featured app that is NOT a family member stays a standalone hero;
// a featured app that IS a family member (e.g. Agents) just joins the cluster
// as an equal card — the cluster's title already carries the grouping, so the
// four hub apps sit together as a tidy grid instead of one being blown up
// into a full-width lead.
// The rest go in an outlined band per kind (dashBand: Apps, My apps,
// Agents), drawn like the Orchestrator cluster, each with the gear that
// arranges it, and a card moves only within its section. The
// administrator's card is in none and comes after them all.
func (dc *dashCards) sort(apps []dashApp) {
	for _, a := range apps {
		featured := false
		if f, ok := a.app.(WebAppFeatured); ok && f.WebFeatured() {
			featured = true
		}
		if _, isHub := a.app.(WebAppHubTab); isHub {
			dc.family = append(dc.family, a)
			continue
		}
		if featured {
			renderDashCard(&dc.heroB, a, " featured") // standalone hero — full-width, set apart
			continue
		}
		extra := ""
		if wd, ok := a.app.(WebAppWide); ok && wd.WebWide() {
			extra = " wide" // full-width row, regular height
		}
		band := dashBand(a)
		if band == "" {
			renderDashCard(&dc.lastB, a, extra)
			continue
		}
		b, ok := dc.bandCards[band]
		if !ok {
			b = &strings.Builder{}
			dc.bandCards[band] = b
			dc.bandNames = append(dc.bandNames, band)
		}
		renderDashCard(b, a, extra)
	}
	// The family arrives in HubTab order unless the viewer moved its cards
	// themselves (sortDashDefault, then their order): nothing to sort here.
}

// Every section the viewer may arrange is drawn, in order, with a gear
// that arranges it: one switched off is left out and named at the foot,
// and one with nothing showing yet still gets its heading and gear, or a
// new app or agent waiting to be switched on would have nowhere to be.
func (dc *dashCards) draw(bands dashBands) string {
	order := append([]string(nil), bands.order...)
	for _, name := range dc.bandNames {
		if !slices.Contains(order, name) {
			order = append(order, name)
		}
	}
	var cards strings.Builder
	var hidden []string
	for _, name := range order {
		if bands.hidden[name] {
			hidden = append(hidden, name)
			continue
		}
		gear := bandGear(name, bands.fresh[name])
		switch name {
		case "Featured":
			if dc.heroB.Len() == 0 {
				cards.WriteString(emptyBand(name, gear))
				continue
			}
			// The gear sits on the hero's corner, outside its link.
			cards.WriteString(`<div class="hero-wrap">` + strings.Replace(gear, `class="band-gear"`, `class="band-gear hero-gear"`, 1) + dc.heroB.String() + `</div>`)
		case "Orchestrator":
			if len(dc.family) == 0 {
				cards.WriteString(emptyBand(name, gear))
				continue
			}
			cards.WriteString(`<div class="cluster"><div class="cluster-head"><span>Orchestrator</span>` + gear + `</div><div class="cluster-grid">`)
			for _, a := range dc.family {
				// Every family member renders at equal size so the four hub apps
				// (Agents / Bridges / Knowledge / Extensions) sit together as a tidy
				// grid. The primary (Agents) is deliberately NOT blown up into a
				// full-width lead here — the cluster title signals the grouping.
				renderDashCard(&cards, a, "")
			}
			cards.WriteString(`</div></div>`)
		default:
			b := dc.bandCards[name]
			if b == nil || b.Len() == 0 {
				cards.WriteString(emptyBand(name, gear))
				continue
			}
			fmt.Fprintf(&cards, `<div class="cluster band"><div class="cluster-head"><span>%s</span>%s</div><div class="cluster-grid">%s</div></div>`,
				html.EscapeString(name), gear, b.String())
		}
	}
	cards.WriteString(dc.lastB.String())
	if len(hidden) > 0 {
		cards.WriteString(`<div class="hidden-bands"><span>Hidden sections:</span>`)
		for _, name := range hidden {
			e := html.EscapeString(name)
			fmt.Fprintf(&cards, `<button type="button" class="band-show" data-band="%s" title="Show %s again">%s</button>`, e, e, e)
		}
		cards.WriteString(`</div>`)
	}
	return cards.String()
}

// dashboardAuthBar is the account bar for a signed-in viewer, and nothing
// for anyone else.
func dashboardAuthBar(username string) string {
	auth_html := ""
	if username != "" {
		// The bell sits with the account controls because it is about the
		// VIEWER, not about the deployment: the notices below it are what an
		// admin has to fix, these are what somebody's own agents had to say.
		// It renders muted and changes only when there is something unread, so
		// a quiet bell is as informative as a loud one.
		auth_html = fmt.Sprintf(
			`<div class="auth-bar"><span class="auth-user">%s</span>`+
				`<button type="button" class="auth-link bell" id="bell" title="Notifications" aria-label="Notifications">`+bellGlyphSVG+`<span class="bell-count" id="bell-count"></span></button>`+
				`<a class="auth-link" href="/account">Account</a><form class="auth-logout" method="POST" action="/logout"><button type="submit" class="auth-link">Logout</button></form></div>`+
				notifyPanelHTML,
			username)
	}
	return auth_html
}

// dashboardNotices draws the notices that have something to say.
func dashboardNotices(notices []DashboardNotice) string {
	var noticeB strings.Builder
	for _, n := range notices {
		if strings.TrimSpace(n.Text) == "" {
			continue
		}
		noticeB.WriteString(`<div class="notice"><span>` + template.HTMLEscapeString(n.Text) + `</span><div class="notice-acts">`)
		if strings.TrimSpace(n.URL) != "" && strings.TrimSpace(n.Action) != "" {
			fmt.Fprintf(&noticeB, `<a class="notice-act" href="%s">%s</a>`, template.HTMLEscapeString(n.URL), template.HTMLEscapeString(n.Action))
		}
		if strings.TrimSpace(n.Dismiss) != "" {
			fmt.Fprintf(&noticeB, `<button type="button" class="notice-dismiss" data-dismiss="%s">Dismiss</button>`, template.HTMLEscapeString(n.Dismiss))
		}
		noticeB.WriteString(`</div></div>`)
	}
	return noticeB.String()
}
