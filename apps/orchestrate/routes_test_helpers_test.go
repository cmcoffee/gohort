package orchestrate

import "net/http"

// routed is the app's real HTTP routes, under its sub-mux: the console's and
// the rest. Never Routes(): that runs the boot (migrations, background
// passes, hooks).
func routed(T *OrchestrateApp) http.HandlerFunc {
	mux := http.NewServeMux()
	T.SetWebMux(mux, "")
	T.registerConsoleRoutes()
	T.registerHandlers()
	return mux.ServeHTTP
}
