package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/selfupdate"
	"github.com/cmcoffee/snugforge/kvlite"
)

// updateFixture is an admin mux, an install of 0.9.377 in a temp folder, and
// a dist site offering 0.9.380. The restart is a stand-in: nothing here
// touches the running program or the host.
type updateFixture struct {
	do        func(token, method, path string) *httptest.ResponseRecorder
	admin     string
	bob       string
	u         *selfupdate.Updater
	restarted chan struct{}
	db        *DBase
}

func newUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stand-in binaries are shell scripts")
	}
	privFile, pubLine, err := selfupdate.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := selfupdate.ParsePrivateKey(privFile)
	keys, _ := selfupdate.ParseKeys(pubLine)
	bin := []byte("#!/bin/sh\necho v0.9.380\n")
	sum := sha256.Sum256(bin)
	path := "0.9.380/" + selfupdate.Platform() + "/gohort"
	manifest, _ := json.Marshal(selfupdate.Manifest{Product: "gohort", Version: "0.9.380", Files: map[string]selfupdate.File{
		selfupdate.Platform(): {Path: path, Size: int64(len(bin)), SHA256: hex.EncodeToString(sum[:])},
	}})
	sig := selfupdate.Sign(manifest, priv)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/gohort/") {
		case selfupdate.ManifestName:
			w.Write(manifest)
		case selfupdate.SignatureName:
			w.Write(sig)
		case path:
			w.Write(bin)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	exe := filepath.Join(t.TempDir(), "gohort")
	os.WriteFile(exe, []byte("#!/bin/sh\necho v0.9.377\n"), 0o755)
	f := &updateFixture{restarted: make(chan struct{}, 1)}
	f.u = &selfupdate.Updater{
		Product: "gohort", Version: "0.9.377",
		Source:  selfupdate.Source{URL: srv.URL + "/gohort", Keys: keys, Product: "gohort"},
		Install: selfupdate.Install{Exe: exe}, RestartDelay: time.Millisecond,
		Restart: func() { f.restarted <- struct{}{} },
	}
	prevUpdater := updater
	updater = func() *selfupdate.Updater { return f.u }
	t.Cleanup(func() { updater = prevUpdater })

	f.db = &DBase{Store: kvlite.MemStore()}
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = f.db
	AuthDB = func() Database { return f.db }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	AuthSetUser(f.db, "root", "pw", true)
	AuthSetUser(f.db, "bob", "pw", false)
	f.admin, f.bob = AuthCreateSession(f.db, "root"), AuthCreateSession(f.db, "bob")
	a := &AdminApp{db: f.db}
	mux := http.NewServeMux()
	a.registerUpdateRoutes(mux)
	a.registerActivityRoutes(mux)
	f.do = func(token, method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	return f
}

func (f *updateFixture) actions(t *testing.T) []string {
	t.Helper()
	var rows []map[string]string
	w := f.do(f.admin, "GET", "/api/update/actions")
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	var out []string
	for _, r := range rows {
		out = append(out, r["Key"]+": "+r["Label"])
		if r["Key"] != selfupdate.ActionCheck && r["confirm"] == "" {
			t.Errorf("%s asks for no confirmation", r["Label"])
		}
	}
	return out
}

func (f *updateFixture) status(t *testing.T) map[string]string {
	t.Helper()
	var st map[string]string
	json.Unmarshal(f.do(f.admin, "GET", "/api/update/status").Body.Bytes(), &st)
	return st
}

// Check, then Update, from the page: the section offers only what there is
// to do, the update is installed and restarts, and the Activity Log says who
// updated from what to what.
func TestTheUpdateSectionChecksThenInstalls(t *testing.T) {
	f := newUpdateFixture(t)
	if got := strings.Join(f.actions(t), " | "); got != "check: Check for update" {
		t.Fatalf("before a check the section offers %q", got)
	}
	if st := f.status(t); !strings.HasPrefix(st["last_check"], "Not checked") || st["updates_here"] != "Offered." {
		t.Fatalf("before a check: %+v", st)
	}
	w := f.do(f.admin, "POST", "/api/update?do=check")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "0.9.380 is available") {
		t.Fatalf("check: %d %s", w.Code, w.Body.String())
	}
	if got := strings.Join(f.actions(t), " | "); got != "check: Check for update | update: Update to 0.9.380" {
		t.Fatalf("after a check the section offers %q", got)
	}
	w = f.do(f.admin, "POST", "/api/update?do=update")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Installed 0.9.380") {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	select {
	case <-f.restarted:
	case <-time.After(2 * time.Second):
		t.Fatal("no restart after the update")
	}
	if b, _ := os.ReadFile(f.u.Install.Exe); !strings.Contains(string(b), "v0.9.380") {
		t.Error("the new binary is not in place")
	}
	if got := strings.Join(f.actions(t), " | "); got != "check: Check for update" {
		t.Errorf("while waiting for the restart the section offers %q", got)
	}
	if st := f.status(t); !strings.Contains(st["updates_here"], "waiting for gohort to restart") || st["previous"] != "0.9.377" {
		t.Errorf("after installing: %+v", st)
	}
	events := activity.List(f.db, activity.Filter{Kind: activity.Update})
	if len(events) != 1 || events[0].User != "root" || events[0].Detail != "updated gohort from 0.9.377 to 0.9.380" {
		t.Errorf("the activity log has %+v", events)
	}
	if p := f.do(f.admin, "GET", "/api/update/progress?key=update"); !strings.Contains(p.Body.String(), "Installed 0.9.380") {
		t.Errorf("the update's outcome is not kept for a page arriving after: %s", p.Body.String())
	}

	// The restarted program, 0.9.380, offers the rollback.
	f.u = &selfupdate.Updater{Product: "gohort", Version: "0.9.380", Source: f.u.Source, Install: f.u.Install,
		RestartDelay: time.Millisecond, Restart: func() { f.restarted <- struct{}{} }}
	if got := strings.Join(f.actions(t), " | "); got != "check: Check for update | rollback: Roll back to 0.9.377" {
		t.Fatalf("after the restart the section offers %q", got)
	}
	if w := f.do(f.admin, "POST", "/api/update?do=rollback"); w.Code != 200 {
		t.Fatalf("rollback: %d %s", w.Code, w.Body.String())
	}
	<-f.restarted
	if events := activity.List(f.db, activity.Filter{Kind: activity.Update}); len(events) != 2 || events[0].Detail != "rolled gohort back from 0.9.380 to 0.9.377" {
		t.Errorf("the activity log has %+v", events)
	}
}

func TestTheUpdateSectionIsAdminOnlyAndRefusesWhatItCannotDo(t *testing.T) {
	f := newUpdateFixture(t)
	for _, p := range []string{"/api/update/status", "/api/update/actions", "/api/update/progress?key=check"} {
		if w := f.do(f.bob, "GET", p); w.Code != http.StatusForbidden {
			t.Errorf("a non-admin read %s: %d", p, w.Code)
		}
	}
	if w := f.do(f.bob, "POST", "/api/update?do=update"); w.Code != http.StatusForbidden {
		t.Errorf("a non-admin updated: %d", w.Code)
	}
	if w := f.do(f.admin, "POST", "/api/update?do=reinstall"); w.Code != http.StatusBadRequest {
		t.Errorf("an unknown action: %d", w.Code)
	}
	// Nothing checked yet, nothing newer known: an update press still
	// checks first, and a failure is an error the row shows, not a success.
	f.u.Source.Keys = nil
	if w := f.do(f.admin, "POST", "/api/update?do=update"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "no release key") {
		t.Errorf("an update with no key to check it: %d %s", w.Code, w.Body.String())
	}
	if events := activity.List(f.db, activity.Filter{Kind: activity.Update}); len(events) != 0 {
		t.Errorf("a refused update was logged: %+v", events)
	}
	if st := f.status(t); !strings.HasPrefix(st["keys"], "None") {
		t.Errorf("keys line %q", st["keys"])
	}

	f.u.Blocked = privateBuildReasonForTest
	if got := f.actions(t); len(got) != 0 {
		t.Errorf("a blocked install offers %v", got)
	}
	if st := f.status(t); !strings.HasPrefix(st["updates_here"], "Not offered: ") {
		t.Errorf("a blocked install says %q", st["updates_here"])
	}
}

const privateBuildReasonForTest = "this build has the private apps in it"

// The section sits on Overview right after the Activity Log.
func TestSoftwareUpdateIsOnOverviewAfterTheActivityLog(t *testing.T) {
	secs := (&AdminApp{}).systemSections()
	if len(secs) < 3 || secs[2].Title != "Software Update" {
		t.Fatalf("System's sections open %q, %q, %q", secs[0].Title, secs[1].Title, secs[2].Title)
	}
	if sectionTab("Software Update", "") != "Overview" || sectionRank["Software Update"] != sectionRank["Activity Log"]+1 {
		t.Errorf("Software Update is on %q at %d", sectionTab("Software Update", ""), sectionRank["Software Update"])
	}
}
