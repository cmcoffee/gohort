package admin

import (
	"net/http"
	"testing"
)

// TestRegisterRoutesDoesNotCollide. ServeMux answers a duplicate pattern with
// a panic, and route registration happens during startup — so a path claimed
// twice does not degrade a page, it stops the deployment from booting. That is
// exactly what shipped: the Apps-tab switchboard took /api/apps, which the
// permission picker's options source had held for far longer, and the server
// died in ServeDashboard before it ever listened.
//
// Registering every admin route on a fresh mux is the whole test. It costs
// nothing and it is the only thing that would have caught this before the
// binary was run.
func TestRegisterRoutesDoesNotCollide(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("admin route registration panicked — two handlers claim one path: %v", r)
		}
	}()
	(&AdminApp{}).RegisterRoutes(http.NewServeMux(), "/admin")
}

// TestTheAppsPathStillAnswersThePermissionPicker: the two endpoints answer
// with different POPULATIONS, so they cannot be merged back onto one path.
// /api/apps includes the dynamic per-agent surfaces a grant can name and the
// switchboard cannot toggle; /api/app-switches carries the enabled state.
// Whichever moves, this pins that they stay apart.
func TestTheAppsPathStillAnswersThePermissionPicker(t *testing.T) {
	mux := http.NewServeMux()
	(&AdminApp{}).RegisterRoutes(mux, "/admin")

	for _, path := range []string{"/admin/api/apps", "/admin/api/app-switches"} {
		r, _ := http.NewRequest("GET", "http://example.invalid"+path, nil)
		h, pattern := mux.Handler(r)
		if h == nil || pattern == "" {
			t.Errorf("%s has no handler — a consumer of it is now broken", path)
		}
	}
}
