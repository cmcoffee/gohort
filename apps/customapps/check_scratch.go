package customapps

// A page check's requests (docs/design/app-checks.md). A page check drives the
// app's real page as its owner, so its browser carries a cookie naming a
// scratch store (appscript.BeginCheckScratch): every record it reads or
// writes, and every script it runs, uses that store and is held to a check's
// rules (appscript.Job.Check). A cookie naming a check that is over, or
// another app, is refused, never read as no check at all, so a check can
// never fall through to someone's real records.

import (
	"context"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/tools/appscript"
)

type checkScratchKey struct{}

// errCheckOver answers a check cookie with no live check behind it.
var errCheckOver = jsonapi.Forbidden("this page check is over, or is for another app: nothing it does touches anyone's records")

// errNotInCheck answers a route a page check may not use.
var errNotInCheck = jsonapi.Forbidden("not in a check: this part of the app can act on its own (an agent's chat, a pipeline, a file written to disk), which a check cannot hold to its scratch records")

// appCheckScratch is the page check r belongs to, if any.
func appCheckScratch(r *http.Request, spec AppSpec, owner string) (appscript.CheckScratch, bool, error) {
	c, err := r.Cookie(appscript.CheckScratchCookie)
	if err != nil || strings.TrimSpace(c.Value) == "" {
		return appscript.CheckScratch{}, false, nil
	}
	sc, ok := appscript.CheckScratchFor(c.Value)
	if !ok || sc.Owner != owner || sc.Slug != spec.Slug {
		return appscript.CheckScratch{}, false, errCheckOver
	}
	return sc, true, nil
}

// withCheckScratch carries a page check's store on its request.
func withCheckScratch(r *http.Request, sc appscript.CheckScratch) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), checkScratchKey{}, sc))
}

// checkScratchOf is the page check a request carries.
func checkScratchOf(r *http.Request) (appscript.CheckScratch, bool) {
	if r == nil {
		return appscript.CheckScratch{}, false
	}
	sc, ok := r.Context().Value(checkScratchKey{}).(appscript.CheckScratch)
	return sc, ok
}

// scriptBase is the store a script run for r reads and writes: the page
// check's scratch for a check's request, else the owner's part of the app.
func (T *CustomApps) scriptBase(r *http.Request, spec AppSpec, owner string) Database {
	if sc, ok := checkScratchOf(r); ok {
		return sc.DB
	}
	return T.recordBase(spec, owner)
}

// checkRouteAllowed reports whether a page check may use the route rest:
// the page, its records and views, its data sources and actions, shared
// collections and reading assets. Not chat or a pipeline (they act on their
// own), files (written to disk) or anything else.
func checkRouteAllowed(rest, method string) bool {
	switch {
	case rest == "", rest == "_view/reset", rest == "changes",
		strings.HasPrefix(rest, "data/"), strings.HasPrefix(rest, "action/"), strings.HasPrefix(rest, "shared/"):
		return true
	case rest == "assets" || strings.HasPrefix(rest, "assets/"):
		return method == http.MethodGet
	}
	return false
}
