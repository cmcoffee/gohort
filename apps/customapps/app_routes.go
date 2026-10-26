package customapps

// The JSON routes of one app, /apps/<slug>/<route>, each behind the gate the
// raw router (route) applies to the rest: the app resolves for the caller,
// they may reach it, and it is enabled.

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// appCall is one request to an app the caller may open.
type appCall struct {
	*jsonapi.Call
	spec  AppSpec
	owner string   // the app's owner, whom its scripts run as
	appdb Database // its record store, for the caller
}

// mine reports whether the caller owns the app, which owner-scoped settings
// and the agent it asks need.
func (a *appCall) mine() bool { return a.User == a.owner }

// errAppDisabled answers any route of an app that is disabled. Bundle imports
// land disabled; the My Apps Enable button is the review gate.
var errAppDisabled = jsonapi.Forbidden("this app is disabled: review it and press Enable on the My Apps page to activate it")

// openApp is the app slug names for user, as every route serves it: their own
// first, else one shared with them that the framework grant admits them to,
// and never a disabled one. A disabled app serves NOTHING: no page, no
// records, and above all no data-source or action scripts.
func (T *CustomApps) openApp(r *http.Request, user, slug string) (AppSpec, string, error) {
	spec, owner, found := T.resolveSpec(user, slug)
	if !found {
		return AppSpec{}, "", errNoApp
	}
	// A shared app is somebody else's: the framework grant has to admit this
	// user to it. Own apps skip this — an owner needs no grant to their own
	// work — and the operator allowlist was already applied in resolveSpec.
	if owner != user && !T.sharedAppReachableBy(r, slug) {
		return AppSpec{}, "", errNoApp
	}
	if spec.Disabled {
		return AppSpec{}, "", errAppDisabled
	}
	return spec, owner, nil
}

// inApp opens the app the {slug} in the route names.
func (T *CustomApps) inApp(c *jsonapi.Call) (*appCall, error) {
	spec, owner, err := T.openApp(c.R, c.User, c.R.PathValue("slug"))
	if err != nil {
		return nil, err
	}
	return &appCall{Call: c, spec: spec, owner: owner, appdb: T.recordBase(spec, c.User)}, nil
}

// appGet is a GET on one app.
func (T *CustomApps) appGet(fn func(a *appCall) (any, error)) http.HandlerFunc {
	return jsonapi.Get(T.DB, func(c *jsonapi.Call) (any, error) {
		a, err := T.inApp(c)
		if err != nil {
			return nil, err
		}
		return fn(a)
	})
}

// appPost is a write to one app, with its body.
func appPost[In any](T *CustomApps, fn func(a *appCall, in In) (any, error), opts ...jsonapi.Option) http.HandlerFunc {
	return jsonapi.Post(T.DB, inAppWrite(T, fn), opts...)
}

// appDelete is appPost for a DELETE.
func appDelete[In any](T *CustomApps, fn func(a *appCall, in In) (any, error), opts ...jsonapi.Option) http.HandlerFunc {
	return jsonapi.Delete(T.DB, inAppWrite(T, fn), opts...)
}

// inAppWrite runs fn on the app the path names.
func inAppWrite[In any](T *CustomApps, fn func(a *appCall, in In) (any, error)) func(c *jsonapi.Call, in In) (any, error) {
	return func(c *jsonapi.Call, in In) (any, error) {
		a, err := T.inApp(c)
		if err != nil {
			return nil, err
		}
		return fn(a, in)
	}
}

// appRoutes registers the app's JSON routes. The page, its scripts' data and
// actions, its files, the shared board and the chat and run surfaces stay on
// route.
func (T *CustomApps) appRoutes() {
	T.HandleFunc("/{slug}/view-state", jsonapi.Methods{
		"GET":  T.appGet(T.viewState),
		"POST": appPost(T, T.saveViewState, jsonapi.MaxBody(viewStateMax*2)),
	}.Serve)
	T.HandleFunc("/{slug}/_settings/values", jsonapi.Methods{
		"GET":  T.appGet(T.settingsValues),
		"POST": appPost(T, T.saveSettingsValues),
	}.Serve)
	T.HandleFunc("/{slug}/_settings/reset", appPost(T, T.resetSettings))
	T.HandleFunc("/{slug}/_settings/agent", jsonapi.Methods{
		"GET":  T.appGet(T.settingsAgent),
		"POST": appPost(T, T.saveSettingsAgent),
	}.Serve)
	T.HandleFunc("/{slug}/actions", jsonapi.Methods{"GET": T.appGet(T.actionsList)}.Serve)
	T.HandleFunc("/{slug}/action-progress/{name}", jsonapi.Methods{"GET": T.appGet(T.actionProgress)}.Serve)
	T.HandleFunc("/{slug}/action-cancel/{name}", appPost(T, T.cancelAction))
	T.HandleFunc("/{slug}/records", jsonapi.Methods{
		"GET":  T.appGet(T.listRecords),
		"POST": appPost(T, T.saveRecord),
	}.Serve)
	T.HandleFunc("/{slug}/record", jsonapi.Methods{
		"GET":    T.appGet(T.getRecord),
		"DELETE": appDelete(T, T.deleteRecord),
	}.Serve)
	T.HandleFunc("/{slug}/changes", jsonapi.Methods{"GET": T.appGet(T.changes)}.Serve)
}
