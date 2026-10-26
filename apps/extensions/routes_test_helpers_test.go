package extensions

import (
	"net/http"
	"strings"
)

// serve is the app's real routes, under the /extensions mount.
func serve(app *Extensions) http.HandlerFunc {
	mux := http.NewServeMux()
	app.SetWebMux(mux, "")
	app.Routes()
	return func(w http.ResponseWriter, r *http.Request) {
		if p, ok := strings.CutPrefix(r.URL.Path, "/extensions"); ok {
			r.URL.Path = p
		}
		mux.ServeHTTP(w, r)
	}
}
