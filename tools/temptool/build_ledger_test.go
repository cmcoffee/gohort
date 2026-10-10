package temptool

import (
	"reflect"
	"testing"
	"time"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/core/buildledger"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A tool test lands in the build ledger with the KIND of failure, so runs
// that broke the same way cluster. A repeat the test cache serves is not a
// run and is not recorded twice.
func TestToolTestLandsInBuildLedger(t *testing.T) {
	buildledger.SetStore(&DBase{Store: kvlite.MemStore()})
	defer buildledger.SetStore(nil)
	sess := newTestSession()
	sess.AgentID = "builder"
	injectBrokenMoltbook(t, sess)

	args := map[string]any{"name": "moltbook"}
	if _, err := testGrouped(args, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := testGrouped(args, sess); err != nil {
		t.Fatal(err)
	}
	rep := buildledger.Read(time.Time{})
	if rep.Verifications != 1 || rep.Fails != 1 {
		t.Fatalf("ledger has %d verifications, %d failed; want the one failed run", rep.Verifications, rep.Fails)
	}
	ep := rep.Recent[0]
	if ep.Owner != "alice" || ep.Kind != buildledger.KindTool || ep.Target != "moltbook" || ep.Green {
		t.Fatalf("episode = %+v", ep)
	}
	if !reflect.DeepEqual(ep.Classes, []string{"unsent-param"}) {
		t.Fatalf("classes = %v, want [unsent-param]", ep.Classes)
	}
}

// A shell tool tested without cases found nothing wrong and proved nothing:
// unproven, not failed.
func TestShellTestWithoutCasesIsUnproven(t *testing.T) {
	buildledger.SetStore(&DBase{Store: kvlite.MemStore()})
	defer buildledger.SetStore(nil)
	sess := newTestSession()
	if err := sess.AppendTempTool(&TempTool{Name: "echoer", CommandTemplate: "echo hi"}); err != nil {
		t.Fatal(err)
	}
	if _, err := testGrouped(map[string]any{"name": "echoer"}, sess); err != nil {
		t.Fatal(err)
	}
	rep := buildledger.Read(time.Time{})
	if rep.Unproven != 1 || rep.Fails != 0 || len(rep.Classes) != 1 || rep.Classes[0].Class != "no-cases" {
		t.Fatalf("report = %+v", rep)
	}
}

// A run with no error can still have failed: dispatch returns a shell tool's
// non-zero exit and an api tool's error status as output. An api tool whose
// pipe replaced the status line cannot say either way.
func TestCheckRunReadsTheOutputNotJustTheError(t *testing.T) {
	shell := TempTool{Name: "s", CommandTemplate: "python3 x.py"}
	api := TempTool{Name: "a", Mode: TempToolModeAPI, CommandTemplate: "https://x.test/"}
	piped := api
	piped.ResponsePipe = ".items"
	for _, c := range []struct {
		name string
		tt   TempTool
		out  string
		want RunCheck
	}{
		{"shell clean", shell, "made 3 rows", RunCheck{Known: true, OK: true}},
		{"shell exit", shell, "Traceback...\n[exit: exit status 1]", RunCheck{Known: true, Class: "run-exit"}},
		{"shell silent", shell, "  ", RunCheck{Known: true, Class: "run-hollow"}},
		{"api 2xx", api, "HTTP 200 OK\n{}", RunCheck{Known: true, OK: true}},
		{"api 404", api, "HTTP 404 Not Found\n{}", RunCheck{Known: true, Class: "live-status"}},
		{"api piped", piped, "[]", RunCheck{}},
	} {
		got := CheckRun(c.tt, c.out)
		if got.Known != c.want.Known || got.OK != c.want.OK || got.Class != c.want.Class {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		if got.Known && !got.OK && got.Why == "" {
			t.Errorf("%s: a failure with no reason", c.name)
		}
	}
}

// The builder fired the last waiting write by hand, the log said "tool
// verified", and the ledger the graders read stayed red: only test wrote to
// it. A direct 2xx of the last unfired write now closes the episode green.
func TestDirectWriteGreensTheLedger(t *testing.T) {
	buildledger.SetStore(&DBase{Store: kvlite.MemStore()})
	defer buildledger.SetStore(nil)
	f, sess := newFakeAPI(t, "tr")
	if _, err := createGrouped(map[string]any{
		"name": "translate", "description": "d", "mode": "toolbox", "credential": "tr",
		"actions": []any{
			map[string]any{"name": "post", "url_template": f.srv.URL + "/translate", "method": "POST",
				"body_template": `{"text": {text}}`,
				"params":        map[string]any{"text": map[string]any{"type": "string"}}},
		},
	}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := testGrouped(map[string]any{"name": "translate"}, sess); err != nil {
		t.Fatalf("test: %v", err)
	}
	if rep := buildledger.Read(time.Time{}); rep.Green != 0 {
		t.Fatalf("an unfired write must not be green yet: %+v", rep)
	}
	tt := sess.LookupTempTool("translate")
	if _, err := dispatchTempTool(sess, tt, map[string]any{"action": "post", "text": "hola"}); err != nil {
		t.Fatalf("direct call: %v", err)
	}
	rep := buildledger.Read(time.Time{})
	if rep.Green != 1 || len(rep.Recent) != 1 || !rep.Recent[0].Green || rep.Recent[0].Target != "translate" {
		t.Fatalf("the direct 2xx must close a green episode: %+v", rep)
	}
}
