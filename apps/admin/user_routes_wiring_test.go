package admin

// Each users-API path reaches its own handler. The handlers are named
// methods; this pins that every path is wired to the one it was written for,
// by an answer only that handler gives.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestEachUsersPathReachesItsOwnHandler(t *testing.T) {
	prevA, prevP, prevM := AdminListUserOwnedAgents, AdminListUserOwnedPipelines, AdminListUserOwnedMachines
	AdminListUserOwnedAgents = func(Database) []UserOwnedAgentRow { return []UserOwnedAgentRow{{ID: "an-agent-row"}} }
	AdminListUserOwnedPipelines = func(Database) []UserOwnedPipelineRow { return []UserOwnedPipelineRow{{ID: "a-pipeline-row"}} }
	AdminListUserOwnedMachines = func(Database) []UserOwnedMachineRow { return []UserOwnedMachineRow{{ID: "a-machine-row"}} }
	t.Cleanup(func() {
		AdminListUserOwnedAgents, AdminListUserOwnedPipelines, AdminListUserOwnedMachines = prevA, prevP, prevM
	})

	db := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(db, "root", "pw", true)
	token := AuthCreateSession(db, "root")
	a := &AdminApp{db: db}
	mux := http.NewServeMux()
	a.registerUsersRoutes(mux)
	for _, tc := range []struct {
		method, path string
		code         int
		want         string
	}{
		{"GET", "/api/users", http.StatusOK, `"username":"root"`},
		{"GET", "/api/user-candidates", http.StatusOK, `"value":"root"`},
		{"GET", "/api/feature-access?feature=zz_feature", http.StatusOK, `"feature":"zz_feature"`},
		{"POST", "/api/user-credentials?owner=o&name=n&action=zz", http.StatusBadRequest, "enable|disable|revoke_share|allow_share"},
		{"GET", "/api/user-agents", http.StatusOK, "an-agent-row"},
		{"GET", "/api/user-pipelines", http.StatusOK, "a-pipeline-row"},
		{"GET", "/api/user-machines", http.StatusOK, "a-machine-row"},
		{"POST", "/api/promotions", http.StatusBadRequest, "missing id"},
		{"POST", "/api/global-tools?action=zz", http.StatusBadRequest, "action must be rollback"},
		{"GET", "/api/users/nobody", http.StatusNotFound, "user not found"},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s %s answered %d %q, want %d with %q", tc.method, tc.path, w.Code, w.Body.String(), tc.code, tc.want)
		}
	}
}
