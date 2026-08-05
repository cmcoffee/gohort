// Hooks the docs leaf needs from the hub. docs must not import core (that
// would close an import cycle), so the two services it can't carry out with
// it — per-user storage and request authentication — arrive as an interface
// it defines itself and a func var core fills in. See core/core.go's init.

package docs

import "net/http"

// Store is the slice of a key/value database docs actually uses. core's
// Database satisfies it structurally, so callers keep passing the handle
// they already have and never see this type.
type Store interface {
	Get(table, key string, output interface{}) bool
	Set(table, key string, value interface{})
}

// RequireUser resolves the per-user store for a request, writing the
// challenge itself and reporting ok=false when the caller isn't
// authenticated. Wired by core, whose auth lives in the hub. Nil until
// wired — HandleDocRules refuses to serve rather than serving them
// unauthenticated, which is the safe direction for a wire that didn't
// happen (a CLI or a test binary that never imported core).
var RequireUser func(w http.ResponseWriter, r *http.Request, base Store) (string, Store, bool)
