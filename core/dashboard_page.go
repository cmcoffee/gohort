package core

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/cmcoffee/gohort/core/ui"
)

func serve_dashboard(w http.ResponseWriter, r *http.Request, apps []dashApp) {
	renderCard := func(b *strings.Builder, a dashApp, extraCls string) {
		fmt.Fprintf(b, `<a class="card%s" href="%s/">
			<div class="card-name">%s</div>
			<div class="card-desc">%s</div>
		</a>`, extraCls, a.path, a.name, a.desc)
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
	var heroB, restB strings.Builder
	var family []dashApp
	for _, a := range apps {
		featured := false
		if f, ok := a.app.(WebAppFeatured); ok && f.WebFeatured() {
			featured = true
		}
		if _, isHub := a.app.(WebAppHubTab); isHub {
			family = append(family, a)
			continue
		}
		if featured {
			renderCard(&heroB, a, " featured") // standalone hero — full-width, set apart
			continue
		}
		extra := ""
		if wd, ok := a.app.(WebAppWide); ok && wd.WebWide() {
			extra = " wide" // full-width row, regular height
		}
		renderCard(&restB, a, extra)
	}
	// Order the family by HubTab order so the cluster and the tab row stay in
	// lockstep from one source (the featured lead sorts first via its low order).
	sort.SliceStable(family, func(i, j int) bool {
		return hubTabOrder(family[i].app) < hubTabOrder(family[j].app)
	})

	var cards strings.Builder
	cards.WriteString(heroB.String())
	if len(family) > 0 {
		cards.WriteString(`<div class="cluster"><div class="cluster-head">Orchestrator</div><div class="cluster-grid">`)
		for _, a := range family {
			// Every family member renders at equal size so the four hub apps
			// (Agents / Bridges / Knowledge / Extensions) sit together as a tidy
			// grid. The primary (Agents) is deliberately NOT blown up into a
			// full-width lead here — the cluster title signals the grouping.
			renderCard(&cards, a, "")
		}
		cards.WriteString(`</div></div>`)
	}
	cards.WriteString(restB.String())

	// Detect logged-in user for the auth bar.
	username := AuthCurrentUser(r)
	auth_html := ""
	if username != "" {
		auth_html = fmt.Sprintf(
			`<div class="auth-bar"><span class="auth-user">%s</span><a class="auth-link" href="/account">Account</a><form class="auth-logout" method="POST" action="/logout"><button type="submit" class="auth-link">Logout</button></form></div>`,
			username)
	}

	html := `<!DOCTYPE html>
<html lang="en" data-theme="%THEME%">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
%FAVICON%
<title>Gohort Dashboard</title>
<style>
%THEMECSS%
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif;
    background: #0d1117; color: #c9d1d9; min-height: 100vh;
    display: flex; flex-direction: column; align-items: center;
    padding: 80px 20px;
  }
  .ascii-logo {
    font-family: 'JetBrains Mono', 'Fira Code', 'SF Mono', ui-monospace, Menlo, Consolas, monospace;
    font-size: 1rem; line-height: 1.15; white-space: pre; letter-spacing: 0.02em;
    margin-bottom: 0.5rem; text-align: center;
    background: linear-gradient(180deg, #f0f6fc 0%, #30363d 100%);
    -webkit-background-clip: text; -webkit-text-fill-color: transparent;
    background-clip: text;
  }
  .subtitle { color: #8b949e; margin-bottom: 3rem; font-size: 1rem; }
  /* Column width for the PHONE layout, where one centred column is the right
     answer. Desktop stops being that shape entirely — see the wide layout
     below — so this is not a cap that grows, it is the narrow case's width. */
  :root { --dash-w: 700px; }
  .grid {
    display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 1.5rem; width: 100%; max-width: var(--dash-w);
  }
  .card {
    display: block; text-decoration: none; color: #c9d1d9;
    background: #161b22; border: 1px solid #30363d; border-radius: 8px;
    padding: 1.5rem; transition: border-color 0.2s, transform 0.2s;
  }
  .card:hover { border-color: #58a6ff; transform: translateY(-2px); }
  .card-name { font-size: 1.25rem; font-weight: 600; color: #f0f6fc; margin-bottom: 0.5rem; }
  .card-desc { font-size: 0.9rem; color: #8b949e; line-height: 1.4; }
  /* Featured hero card — the primary entry point. Spans the full grid
     width and is larger so it stands apart by SIZE, not color (a blue
     border reads as a hover/selected state and is confusing here). */
  .card.featured {
    grid-column: 1 / -1;
    padding: 2.25rem 2rem;
    background: linear-gradient(135deg, #161b22 0%, #1b2230 100%);
    box-shadow: 0 8px 24px rgba(0,0,0,0.35);
  }
  .card.featured:hover { transform: translateY(-3px); box-shadow: 0 12px 30px rgba(0,0,0,0.45); }
  .card.featured .card-name { font-size: 1.9rem; margin-bottom: 0.6rem; }
  /* Capped by MEASURE, not by the card: the hero spans every column, and at
     four of them its one line of description would run the width of the
     screen. A line that long is measurably harder to read, and the card looks
     empty rather than generous. */
  .card.featured .card-desc { font-size: 1.02rem; max-width: 62ch; }
  /* Wide card — spans the full grid row at the REGULAR card height (a
     "double" button). Used for a bottom utility entry like Administrator. */
  .card.wide { grid-column: 1 / -1; }
  /* Orchestrator family cluster — a full-width titled block grouping the apps
     that also appear as shared top-nav tabs (Agents / Bridges / Knowledge /
     Gateways), so the dashboard mirrors that grouping instead of scattering
     them among unrelated cards. It spans the full grid width and holds its own
     inner card grid. */
  .cluster {
    grid-column: 1 / -1;
    border: 1px solid #30363d; border-radius: 10px;
    padding: 1rem 1rem 1.25rem;
  }
  .cluster-head {
    font-size: 0.78rem; font-weight: 600; letter-spacing: 0.08em;
    text-transform: uppercase; color: #8b949e; margin-bottom: 0.9rem;
  }
  .cluster-grid {
    /* auto-FIT, not auto-fill. The cluster holds a bounded set — the apps that
       are also hub tabs, four of them — so a reserved empty track reads as a
       missing app rather than as spare room, and invites filling a hole by
       moving something in that does not belong. auto-fit collapses the empty
       track and the members share the width instead.
       The main grid above keeps auto-fill on purpose: it is an open-ended list
       where a short last row is just the end of the list. */
    display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 1rem;
  }
  #live-panel {
    width: 100%; max-width: var(--dash-w); margin-top: 2rem;
  }
  #live-panel h3 { color: #8b949e; font-size: 0.9rem; margin-bottom: 0.75rem; cursor: pointer; }
  #live-panel h3:hover { color: #c9d1d9; }
  .live-item {
    display: flex; align-items: center; gap: 0.75rem;
    padding: 0.6rem 0.8rem; background: #161b22; border: 1px solid #21262d;
    border-radius: 6px; margin-bottom: 0.4rem; cursor: pointer;
    text-decoration: none; color: #c9d1d9; font-size: 0.85rem;
  }
  .live-item:hover { border-color: #30363d; }
  .live-badge {
    font-size: 0.7rem; padding: 0.15rem 0.4rem; border-radius: 4px;
    font-weight: 600; white-space: nowrap;
  }
  .live-badge.running { background: var(--success); color: #fff; }
  .live-badge.queued { background: var(--warning); color: #fff; }
  .live-label { flex: 1; }
  .live-status { color: #8b949e; font-size: 0.8rem; }
  .auth-bar {
    position: fixed; top: 12px; right: 12px; z-index: 9999;
    display: flex; align-items: center; gap: 0.6rem;
    font-size: 0.8rem;
    max-width: calc(100vw - 64px); /* leave room for the dashboard-back icon at top-left */
  }
  .auth-user {
    color: #8b949e;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 40vw;
  }
  .auth-link {
    color: #8b949e; text-decoration: none;
    padding: 0.3rem 0.7rem; border: 1px solid #30363d; border-radius: 6px;
    background: #161b22; transition: border-color 0.2s, color 0.2s;
    white-space: nowrap;
    /* Match a <button class="auth-link"> (POST-logout) to the <a> siblings. */
    cursor: pointer; font: inherit; line-height: normal;
  }
  .auth-link:hover { border-color: #58a6ff; color: #f0f6fc; }
  .auth-logout { display: inline; margin: 0; padding: 0; }
  /* DESKTOP: stop being a phone. Below this the page is one centred column,
     which is the right answer on a handset and the wrong one on a monitor —
     the same 700px ribbon sat in the middle of a 27-inch screen with two feet
     of nothing either side, and widening the ribbon would only have made a
     bigger ribbon.
     So the shape changes rather than the size. Body becomes a two-region grid:
     the apps take the room, and Live Sessions moves out of the stack below
     them into a rail on the right, where the horizontal space it is given is
     space it can use. The masthead still spans and centres, because a logo
     that slides left to sit over one column reads as misaligned rather than as
     laid out.
     1100px is where the rail earns its place: it needs ~300px and the grid
     needs two comfortable columns beside it, and below that sum the rail is
     stealing width rather than using it. */
  @media (min-width: 1100px) {
    body {
      display: grid;
      grid-template-columns: minmax(0, 1fr) 320px;
      column-gap: 2.5rem;
      align-content: start;
      /* The base rule centres items for the phone column; in a grid that
         would centre the shorter of the two regions against the taller, so a
         short rail would float halfway down beside a long card grid. */
      align-items: start;
      width: 100%;
      max-width: 1600px;
      margin: 0 auto;
      padding: 72px 40px 48px;
    }
    /* Masthead spans both regions and stays centred over them. */
    .ascii-logo, .subtitle { grid-column: 1 / -1; justify-self: center; }
    /* The grid drops its cap and fills its region — auto-fill then decides the
       column count from the space it actually has, which is the whole point of
       auto-fill and was never reachable behind a fixed max-width. */
    .grid { grid-column: 1; grid-row: 3; max-width: none; }
    #live-panel {
      grid-column: 2; grid-row: 3;
      max-width: none; margin-top: 0;
      position: sticky; top: 72px;
    }
  }
  /* Past a point the cards stop growing and the page gains margin instead: a
     tile wide enough to hold a paragraph it will never contain is not using
     the space, it is padding it. */
  @media (min-width: 1900px) {
    body { max-width: 1800px; }
  }
  @media (max-width: 640px) {
    body { padding: 60px 12px 20px; }
    .auth-bar { top: 8px; right: 8px; gap: 0.4rem; }
    .auth-user { display: none; } /* keep just the Logout button visible on narrow screens */
    .grid { grid-template-columns: 1fr; gap: 0.75rem; }
    .cluster-grid { grid-template-columns: 1fr; gap: 0.75rem; }
    .card { padding: 1rem; }
  }
  /* Theme overrides — re-point chrome surfaces to the active theme's tokens
     (injected above via %THEMECSS%). Additive + same selector specificity as
     the rules above, so these win; anything not re-pointed keeps its original
     color as a fallback. */
  body { background: var(--bg-0); color: var(--text); }
  .subtitle, .card-desc, .auth-user, .live-status, #live-panel h3 { color: var(--text-mute); }
  .card, .card.featured, .live-item, .auth-link { background: var(--bg-1); border-color: var(--border); color: var(--text); }
  .cluster { border-color: var(--border); }
  .cluster-head { color: var(--text-mute); }
  .card-name { color: var(--text-hi); }
  .card:hover, .auth-link:hover, .live-item:hover { border-color: var(--accent); }
  .auth-link:hover { color: var(--text-hi); }
  .live-badge.running { background: var(--success); }
  .ascii-logo { background: linear-gradient(180deg, var(--text-hi) 0%, var(--border) 100%); -webkit-background-clip: text; background-clip: text; }
</style>
</head>
<body>
  %AUTH%
  <div class="ascii-logo">
  ____       _                _
 / ___| ___ | |__   ___  _ __| |_
| |  _ / _ \| '_ \ / _ \| '__| __|
| |_| | (_) | | | | (_) | |  | |_
 \____|\___/|_| |_|\___/|_|   \__|</div>
  <p class="subtitle">Agent Dashboard</p>
  <div class="grid">%CARDS%</div>
  <div id="live-panel"><h3><a href="/monitor" style="color:inherit;text-decoration:none">Live Sessions &rarr;</a></h3><div id="live-list"></div></div>
<script>
var liveHidden = false;
function toggleLive() {
  liveHidden = !liveHidden;
  document.getElementById('live-list').style.display = liveHidden ? 'none' : 'block';
  if (!liveHidden) refreshLive();
}
function refreshLive() {
  if (liveHidden) return;
  var list = document.getElementById('live-list');
  fetch('/api/live').then(function(r){return r.json()}).then(function(items){
    // Drop spawned child sessions. The parent session's status
    // already reflects its current stage, so showing both the parent
    // and every child turns one logical operation into several noisy
    // rows on the dashboard.
    items = (items || []).filter(function(it) { return !it.spawned; });
    if (items.length === 0) {
      list.innerHTML = '<div style="color:#484f58;padding:0.5rem;font-size:0.85rem">No active sessions.</div>';
      return;
    }
    items.sort(function(a, b) {
      return (a.app || '').localeCompare(b.app || '');
    });
    var html = '';
    for (var i = 0; i < items.length; i++) {
      var it = items[i];
      var badge = it.queued ? '<span class="live-badge queued">Queued</span>' : '<span class="live-badge running">Running</span>';
      var app = it.app ? '<span class="live-badge" style="background:#30363d;color:#8b949e">' + it.app + '</span>' : '';
      // Every live item opens the central Monitor page (the expanded view).
      html += '<a class="live-item" href="/monitor">';
      html += app + badge;
      html += '<span class="live-label">' + (it.topic || it.label || 'Untitled') + '</span>';
      if (it.status) html += '<span class="live-status">' + it.status + '</span>';
      html += '</a>';
    }
    list.innerHTML = html;
  });
}
refreshLive();
setInterval(refreshLive, 10000);
</script>
</body>
</html>`
	html = strings.Replace(html, "%THEME%", ui.ActiveTheme(), 1)
	html = strings.Replace(html, "%THEMECSS%", ui.ThemeCSS(), 1)
	html = strings.Replace(html, "%FAVICON%", faviconLinkTag, 1)
	html = strings.Replace(html, "%AUTH%", auth_html, 1)
	html = strings.Replace(html, "%CARDS%", cards.String(), 1)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, html)
}
