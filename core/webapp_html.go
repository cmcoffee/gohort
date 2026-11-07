package core

import (
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
  padding: 0.3rem 0.6rem; font-size: var(--fs-sm, 0.8rem); color: var(--text-mute);
  box-shadow: 0 4px 12px rgba(0,0,0,0.3); z-index: 9999; display: none;
}
@media (max-width: 640px) {
  #webui-live-ribbon {
    /* Keep on the same top row as the back arrow: sub-app pages
       don't have an auth-bar so there's nothing to clear. The
       back arrow sits at top:12 left:12 (32x32) and the ribbon
       at top:0 right:5; they're on opposite sides and don't
       collide even when the ribbon has items. */
    max-width: calc(100vw - 60px);
    font-size: var(--fs-xs, 0.75rem);
  }
}
#webui-live-ribbon h4 {
  font-size: var(--fs-xs, 0.75rem); text-transform: uppercase; letter-spacing: 0.05em;
  color: var(--text-mute); margin: 0; cursor: pointer;
  display: flex; align-items: center; gap: 0.4rem;
}
#webui-live-ribbon h4 .live-dot {
  width: 8px; height: 8px; border-radius: 50%; background: var(--good);
  display: inline-block; animation: pulse 2s infinite;
}
@keyframes pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.4; } }
/* Mirrors base.css: see there for why the label carries a flex floor
   instead of taking whatever the badges leave over. */
#webui-live-ribbon .item {
  display: flex; gap: 0.4rem; align-items: center; flex-wrap: wrap;
  padding: 0.3rem 0; color: var(--text); text-decoration: none;
}
#webui-live-ribbon .item:hover { color: var(--text-hi); }
#webui-live-ribbon .item .label {
  flex: 1 1 7rem; min-width: 0;
  overflow-wrap: anywhere; line-height: 1.35;
}
#webui-live-ribbon .badge {
  flex: 0 0 auto; white-space: nowrap;
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
