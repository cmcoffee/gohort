package core

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/cmcoffee/gohort/core/webui"
)

// liveRibbonCSS is the CSS for the webui live session ribbon, inlined
// for pages that render raw HTML via ServeHTMLWithBase rather than
// going through webui.RenderPage (which inlines base.css itself).
const liveRibbonCSS = `
#webui-live-ribbon {
  position: fixed; top: 0; right: 5px; max-width: 360px; margin: 0.5rem;
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px;
  padding: 0.3rem 0.6rem; font-size: 0.8rem; color: var(--text-mute);
  box-shadow: 0 4px 12px rgba(0,0,0,0.3); z-index: 9999; display: none;
}
@media (max-width: 640px) {
  #webui-live-ribbon {
    /* Keep on the same top row as the back arrow — sub-app pages
       don't have an auth-bar so there's nothing to clear. The
       back arrow sits at top:12 left:12 (32x32) and the ribbon
       at top:0 right:5; they're on opposite sides and don't
       collide even when the ribbon has items. */
    max-width: calc(100vw - 60px);
    font-size: 0.75rem;
  }
}
#webui-live-ribbon h4 {
  font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.05em;
  color: var(--text-mute); margin: 0; cursor: pointer;
  display: flex; align-items: center; gap: 0.4rem;
}
#webui-live-ribbon h4 .live-dot {
  width: 8px; height: 8px; border-radius: 50%; background: var(--good);
  display: inline-block; animation: pulse 2s infinite;
}
@keyframes pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.4; } }
#webui-live-ribbon .item {
  display: flex; gap: 0.4rem; align-items: center;
  padding: 0.3rem 0; color: var(--text); text-decoration: none;
}
#webui-live-ribbon .item:hover { color: var(--text-hi); }
#webui-live-ribbon .badge {
  font-size: 0.65rem; padding: 0.1rem 0.35rem; border-radius: 3px;
  background: var(--bg-2); color: var(--text-mute);
}
#webui-live-ribbon .badge.run { background: var(--good); color: #fff; }
#webui-live-ribbon .badge.q   { background: var(--warn); color: #fff; }
`

// liveRibbonJS is the JS for the webui live session ribbon, inlined
// for pages that don't use the webui framework.
const liveRibbonJS = `(function(){
  function esc(s){return String(s==null?'':s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;')}
  function ensureRibbon(){
    var el=document.getElementById('webui-live-ribbon');
    if(el)return el;
    el=document.createElement('div');
    el.id='webui-live-ribbon';
    el.innerHTML='<h4><span class="live-dot"></span>Live</h4><div class="items" style="display:none"></div>';
    document.body.appendChild(el);
    el.querySelector('h4').addEventListener('click',function(){
      var items=el.querySelector('.items');
      items.style.display=items.style.display==='none'?'':'none';
    });
    return el;
  }
  function refresh(){
    fetch(location.origin+'/api/live').then(function(r){return r.json()}).then(function(items){
      var el=ensureRibbon(),box=el.querySelector('.items');
      items=(items||[]).filter(function(it){return !it.spawned});
      if(items.length===0){el.style.display='none';box.innerHTML='';return}
      items.sort(function(a,b){var oa=a.order||0,ob=b.order||0;if(oa!==ob)return oa-ob;return(a.app||'').localeCompare(b.app||'')});
      var h='';
      for(var i=0;i<items.length;i++){
        var it=items[i];
        var badge=it.queued?'<span class="badge q">Queued</span>':'<span class="badge run">Running</span>';
        var app=it.app?'<span class="badge">'+esc(it.app)+'</span>':'';
        // Every live item opens the central Monitor page (the expanded view).
        h+='<a class="item" href="'+location.origin+'/monitor">'+app+badge+'<span class="label">'+esc(it.topic||it.label||'Untitled')+'</span></a>';
      }
      box.innerHTML=h;el.style.display='block';
    }).catch(function(){});
  }
  refresh();setInterval(refresh,10000);
})();`

// faviconLinkTag is the inline-SVG favicon link injected into every page
// served via ServeHTMLWithBase. Sources the SVG from webui.FaviconSVG so
// legacy and migrated pages share a single definition — update the icon
// in one place and both render paths pick it up.
//
// A var rather than a const because FaviconSVG is now derived from the icon
// markup at package init rather than hand-maintained as a second literal.
var faviconLinkTag = `<link rel="icon" type="image/svg+xml" href="data:image/svg+xml;utf8,` + webui.FaviconSVG + `">`

// ServeHTMLWithBase serves an HTML string, injecting a <base href> tag and
// converting absolute API paths to relative when a prefix is set.
// When prefix is empty (standalone mode), the HTML is served unchanged
// (other than favicon injection).
func ServeHTMLWithBase(w http.ResponseWriter, html string, prefix string) {
	// Inject the favicon into every page, regardless of prefix. Replaces
	// the first <head> opener with <head><favicon>. Skipped if the page
	// already has a favicon link (e.g., pages rendered via webui.RenderPage).
	if !strings.Contains(html, `rel="icon"`) {
		html = strings.Replace(html, "<head>", "<head>"+faviconLinkTag, 1)
	}

	// Inject the shared @font-face declaration (Orbitron) right after
	// <head>, so apps that render raw HTML templates (rather than
	// going through webui.RenderPage) pick up the header font without
	// wiring it per-app.
	if ff := webui.FontFaceCSS(); ff != "" && !strings.Contains(html, "@font-face") {
		html = strings.Replace(html, "<head>", "<head><style>"+ff+"</style>", 1)
	}

	if prefix != "" {
		base_href := prefix + "/"
		html = strings.Replace(html, "<head>",
			fmt.Sprintf("<head><base href=\"%s\">", base_href), 1)
		// Convert absolute API paths to relative so <base> resolves them.
		html = strings.ReplaceAll(html, "'/api/", "'api/")
		html = strings.ReplaceAll(html, "\"/api/", "\"api/")
		// Inject a floating back-arrow icon. Default behavior navigates
		// to the dashboard (/). Apps that have drilled-in views (e.g.,
		// viewing a single record from a list) can override this by
		// setting window.drillBackHandler to a function — when set,
		// clicking the arrow calls that function instead of navigating,
		// letting the app return to its own list view. Apps clear the
		// handler when they leave the drilled state so the arrow
		// reverts to dashboard-navigation.
		back_btn := `<a id="dashboard-back" href="/" title="Back" style="` +
			`position:fixed;top:12px;left:12px;z-index:9999;` +
			`display:inline-flex;align-items:center;justify-content:center;` +
			`width:32px;height:32px;border-radius:6px;` +
			`background:#161b22;border:1px solid #30363d;` +
			`color:#8b949e;text-decoration:none;font-size:1rem;` +
			`transition:border-color 0.2s,color 0.2s,background 0.2s;` +
			`" onclick="if(typeof window.drillBackHandler==='function'){window.drillBackHandler();return false;}return true;"` +
			` onmouseover="this.style.borderColor='#58a6ff';this.style.color='#f0f6fc';this.style.background='#1c2128'"` +
			` onmouseout="this.style.borderColor='#30363d';this.style.color='#8b949e';this.style.background='#161b22'"` +
			`>` +
			`<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor">` +
			`<path d="M7.78 12.53a.75.75 0 01-1.06 0L2.47 8.28a.75.75 0 010-1.06l4.25-4.25a.75.75 0 011.06 1.06L4.81 7h7.44a.75.75 0 010 1.5H4.81l2.97 2.97a.75.75 0 010 1.06z"/>` +
			`</svg></a>`
		dashboard_style := `<style>#dashboard-back~*{} body{padding-top:3.5rem!important;}</style>`
		// Inject the webui live ribbon for pages that don't use the webui
		// framework. Pages that DO use webui already have it via BaseCSS/BaseJS.
		live_widget := ""
		if !strings.Contains(html, "webui-live-ribbon") && !strings.Contains(html, "refreshRibbon") {
			live_widget = `<style>` +
				`:root{--bg-1:#161b22;--bg-2:#21262d;--border:#30363d;--text:#c9d1d9;--text-hi:#f0f6fc;--text-mute:#8b949e;--good:#238636;--warn:#d29922}` +
				liveRibbonCSS + `</style><script>` + liveRibbonJS + `</script>`
		}
		// Inject webui.BaseJS for pages that don't go through
		// webui.RenderPage. This exposes shared helpers (escapeHtml,
		// renderMarkdown, etc.) as window globals so app-specific JS
		// can call them. Keyed on an arbitrary symbol from the bundle
		// so it only fires once per page.
		shared_js := ""
		if !strings.Contains(html, "window.renderMarkdown") {
			shared_js = `<script>` + webui.BaseJS() + `</script>`
		}
		// Replace the LAST </body> tag — earlier occurrences may be inside JS strings.
		if idx := strings.LastIndex(html, "</body>"); idx >= 0 {
			html = html[:idx] + dashboard_style + back_btn + live_widget + shared_js + html[idx:]
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, html)
}

// AppUIAssets holds the app-specific HTML, CSS, and JS passed to NewWebUI.
// Title, AppName, and Prefix are filled in automatically from the WebApp.
type AppUIAssets struct {
	BodyHTML string
	AppCSS   string
	AppJS    string
	HeadHTML string
}

// NewWebUI creates a sub-mux and registers the app's root HTML page on it
// using the app's WebName() for the title and toolbar. The caller is
// responsible for mounting the returned sub-mux (via MountSubMux or a
// custom gate) so apps with special access-control wrappers can interpose.
func NewWebUI(app WebApp, prefix string, assets AppUIAssets) *http.ServeMux {
	sub := http.NewServeMux()
	// If the app uses the legacy hand-rolled body + css + js, mount it
	// at "/". Apps that have migrated to the core/ui framework leave
	// BodyHTML empty and register their own "/" handler — the legacy
	// catch-all would otherwise clash with that registration.
	if assets.BodyHTML != "" {
		sub.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			webui.WriteHTML(w, webui.RenderPage(webui.PageOpts{
				Title:    app.WebName(),
				AppName:  app.WebName(),
				Prefix:   prefix,
				BodyHTML: assets.BodyHTML,
				AppCSS:   assets.AppCSS,
				AppJS:    assets.AppJS,
				HeadHTML: assets.HeadHTML,
			}))
		})
	}
	return sub
}

// WriteAppHTML renders the app's hand-rolled BodyHTML/CSS/JS to w
// using the same webui.RenderPage path NewWebUI uses for "/", but as
// a function callable from any handler. Useful when an app needs to
// serve the legacy hand-rolled UI from a non-root path (e.g.
// /legacy) alongside a framework-based "/" handler during a phased
// migration.
func WriteAppHTML(w http.ResponseWriter, app WebApp, prefix string, assets AppUIAssets) {
	webui.WriteHTML(w, webui.RenderPage(webui.PageOpts{
		Title:    app.WebName(),
		AppName:  app.WebName(),
		Prefix:   prefix,
		BodyHTML: assets.BodyHTML,
		AppCSS:   assets.AppCSS,
		AppJS:    assets.AppJS,
		HeadHTML: assets.HeadHTML,
	}))
}

// MountSubMux registers a sub-mux under a prefix using StripPrefix.
// When prefix is empty (standalone mode), mounts at root. The sub-mux
// is wrapped with UsageReportMiddleware so every handler registered on
// it emits a per-request cost summary automatically — no handler-side
// code required. The middleware skips streaming paths (SSE event
// streams) where a held-open connection would mis-attribute work.
func MountSubMux(mux *http.ServeMux, prefix string, sub *http.ServeMux) {
	label := strings.TrimPrefix(prefix, "/")
	if label == "" {
		label = "web"
	}
	wrapped := UsageReportMiddleware(label)(sub)
	if prefix != "" {
		mux.Handle(prefix+"/", http.StripPrefix(prefix, wrapped))
	} else {
		mux.Handle("/", wrapped)
	}
}
