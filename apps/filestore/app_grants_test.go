package filestore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appstores"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An app granted Intake (write, commands) and Analysis (pull): the grants
// resolve to folders and rights, a two-phase command gives its challenge and
// then takes the response, a pull lands, and anything not granted is refused
// with what would allow it.
func TestAnAppUsesOnlyWhatItsStoresGrant(t *testing.T) {
	app, intake, analysis := pullFixture(t)
	// A two-phase extractor: no response, it prints the challenge; with one,
	// it writes prepared/ beside the dump.
	bin := filepath.Join(t.TempDir(), "extract.sh")
	os.WriteFile(bin, []byte("#!/bin/sh\nif [ -z \"$2\" ]; then echo 'CHALLENGE 4711'; exit 0; fi\nmkdir -p \"$1/prepared\" && echo \"opened with $2\" > \"$1/prepared/dump.txt\"\n"), 0o755)
	if _, err := SaveStoreCommand(app.DB, StoreCommand{Slug: intake.Slug, Name: "extract", Label: "Extract", Command: bin, TwoPhase: true}); err != nil {
		t.Fatal(err)
	}
	intake.AppGrants = []AppGrant{{App: "alice/kwprep", Write: true, Commands: true}}
	analysis.AppGrants = []AppGrant{{App: "alice/kwprep", Pull: true}}
	SaveStore(app.DB, intake)
	SaveStore(app.DB, analysis)
	host := storeHost{app}

	grants := host.GrantsFor("alice", "kwprep")
	if len(grants) != 2 {
		t.Fatalf("grants: %+v", grants)
	}
	for _, g := range grants {
		if g.Store == intake.Slug && (!g.Write || !g.Commands || g.Pull || g.Path != intake.Path) {
			t.Errorf("Intake grant: %+v", g)
		}
		if g.Store == analysis.Slug && (g.Write || !g.Pull || !g.Read) {
			t.Errorf("Analysis grant: %+v", g)
		}
	}
	if len(host.GrantsFor("alice", "other")) != 0 || len(host.GrantsFor("bob", "kwprep")) != 0 {
		t.Error("another app, or another owner's app of that name, got the grants")
	}

	ctx := context.Background()
	challenge, err := host.RunCommand(ctx, "alice", "kwprep", intake.Slug, "CASE-2", "extract", "")
	if err != nil || !strings.Contains(challenge, "CHALLENGE 4711") {
		t.Fatalf("first phase: %q %v", challenge, err)
	}
	if _, err := host.RunCommand(ctx, "alice", "kwprep", intake.Slug, "CASE-2", "extract", "s3cret"); err != nil {
		t.Fatalf("second phase: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(intake.Path, "CASE-2", "prepared", "dump.txt")); !strings.Contains(string(b), "opened with s3cret") {
		t.Errorf("the response did not reach the command: %q", b)
	}
	out, err := host.Pull(ctx, "alice", "kwprep", analysis.Slug, "CASE-2", false)
	if err != nil || !strings.Contains(out, "Copied CASE-2") {
		t.Fatalf("pull: %q %v", out, err)
	}

	for name, call := range map[string]func() error{
		"a command on a store granted no commands": func() error {
			_, err := host.RunCommand(ctx, "alice", "kwprep", analysis.Slug, "CASE-2", "extract", "")
			return err
		},
		"a pull into a store granted no pulls": func() error {
			_, err := host.Pull(ctx, "alice", "kwprep", intake.Slug, "CASE-1", false)
			return err
		},
		"an app with no grant": func() error {
			_, err := host.RunCommand(ctx, "alice", "other", intake.Slug, "CASE-2", "extract", "")
			return err
		},
		"a folder that is a path out": func() error {
			_, err := host.RunCommand(ctx, "alice", "kwprep", intake.Slug, "../../etc", "extract", "")
			return err
		},
		"a response that is an option": func() error {
			_, err := host.RunCommand(ctx, "alice", "kwprep", intake.Slug, "CASE-2", "extract", "--output=/x")
			return err
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s was allowed", name)
		}
	}
}

// The admin grants, lists and removes an app on a store's page; the store's
// settings form never carries the grants, so saving it cannot erase them.
func TestAnAdminGrantsAnAppOnTheStoresPage(t *testing.T) {
	app, intake, _ := pullFixture(t)
	prevAuth := AuthDB
	authDB := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(authDB, "admin-a", "pw-admin-123", true)
	AuthDB = func() Database { return authDB }
	t.Cleanup(func() { AuthDB = prevAuth })
	token := AuthCreateSession(authDB, "admin-a")
	do := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		w := httptest.NewRecorder()
		if strings.Contains(path, "app-grants") {
			app.handleAppGrants(w, r)
		} else {
			app.handleStores(w, r)
		}
		return w
	}
	if w := do(http.MethodPost, "/filestore/api/app-grants?slug="+intake.Slug, `{"app":"alice/kwprep","write":true,"commands":true}`); w.Code != http.StatusOK {
		t.Fatalf("grant: %d %s", w.Code, w.Body.String())
	}
	if w := do(http.MethodGet, "/filestore/api/app-grants?slug="+intake.Slug, ""); !strings.Contains(w.Body.String(), "read, write, run commands") {
		t.Errorf("list: %s", w.Body.String())
	}
	if w := do(http.MethodGet, "/filestore/api/stores?slug="+intake.Slug+"&view=form", ""); strings.Contains(w.Body.String(), "app_grants") {
		t.Errorf("the settings form carries the grants: %s", w.Body.String())
	}
	do(http.MethodPost, "/filestore/api/stores?slug="+intake.Slug, `{"name":"Intake renamed"}`)
	if st, _ := LoadStore(app.DB, intake.Slug); len(st.AppGrants) != 1 || st.Name != "Intake renamed" {
		t.Errorf("saving the settings dropped the grant: %+v", st)
	}
	do(http.MethodDelete, "/filestore/api/app-grants?slug="+intake.Slug+"&app=alice/kwprep", "")
	if st, _ := LoadStore(app.DB, intake.Slug); len(st.AppGrants) != 0 {
		t.Errorf("the grant was not removed: %+v", st.AppGrants)
	}
	var _ appstores.Host = storeHost{}
}
