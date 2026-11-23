package extensions

// A tool's kept versions: the list, the read-only preview and the way back,
// from core/revisions.Surface, the same as a skill's. Every save of a tool
// keeps the version it replaced (core writeToolPool), so a change that broke
// it, by Builder, by you or by anybody else, can be put back in one click.

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/revisions"
)

// handleUserToolOne serves /api/tools/{name}/revisions[/preview|/restore],
// for the caller's own tools.
func (T *Extensions) handleUserToolOne(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/tools/")
	name, tail, _ := strings.Cut(rest, "/")
	action, isRev := revisionAction(tail)
	if !isRev || strings.TrimSpace(name) == "" {
		http.NotFound(w, r)
		return
	}
	cur, found := UserToolByName(AuthDB(), user, name)
	if !found {
		http.NotFound(w, r)
		return
	}
	store, key := ToolRevisionRing(AuthDB(), user, name)
	locked := ""
	if cur.Tool.Locked {
		locked = "This tool is locked: unlock it before restoring a version."
	}
	revisions.Surface{
		Store:        store,
		Kind:         revisions.KindTool,
		Key:          key,
		Noun:         "tool",
		Current:      cur.Tool,
		Ignore:       []string{"trial", "trial_since"},
		LockedReason: locked,
		Restore: func(ref string) error {
			_, err := RestoreTempTool(AuthDB(), user, name, ref)
			return err
		},
	}.Serve(w, r, action)
}

// toolHistoryJS opens a tool's kept versions from its row.
const toolHistoryJS = `function(ctx){
  var r = (ctx && ctx.record) || {};
  if (!r.name) return;
  window.uiOpenHistory('api/tools/' + encodeURIComponent(r.name) + '/revisions', {onRestored: ctx && ctx.reload});
}`
