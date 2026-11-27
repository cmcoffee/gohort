package anvil

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/appcheck"
	"github.com/cmcoffee/gohort/core/appdraft"
)

// try_page loads the working copy through a preview of its own on a copy of
// the person's records, takes the steps, and says what happened: the
// errors, how the steps went, what the page shows. The card carries the
// screenshot, which only the person it was taken for can load, and the
// preview ends with the load.
func TestTryPageLoadsTheWorkingCopyAndSaysWhatHappened(t *testing.T) {
	T, serve := appProjectWorld(t)
	id := openWx(t, serve)
	var p Project
	UserDB(T.DB, "alice").Get(projectTable, id, &p)
	base, _ := p.baseDir()
	os.WriteFile(filepath.Join(base, "page.html"), []byte("<!DOCTYPE html><html><body><h1>Wx two</h1></body></html>"), 0o644)

	prevB, prevAddr, prevCP := BrowserCheckPage, WebListenAddr, appdraft.CheckPreview
	t.Cleanup(func() { BrowserCheckPage, WebListenAddr, appdraft.CheckPreview = prevB, prevAddr, prevCP })
	WebListenAddr = "127.0.0.1:9"
	var mu sync.Mutex
	previews := map[string]AppSpec{}
	ended := 0
	appdraft.CheckPreview = func(editor string, spec AppSpec, copyRecords bool) (PageCheckCookie, func(), error) {
		if editor != "alice" || !copyRecords {
			t.Errorf("try_page's preview: editor %q, copied records %v", editor, copyRecords)
		}
		mu.Lock()
		defer mu.Unlock()
		previews["tok"] = spec
		return PageCheckCookie{Name: "test_preview", Value: "tok", Path: "/apps/wx/"}, func() {
			mu.Lock()
			defer mu.Unlock()
			delete(previews, "tok")
			ended++
		}, nil
	}
	var probes []string
	BrowserCheckPage = func(target string, cookies []PageCheckCookie, probe string, _ bool) (*PageCheckReport, error) {
		mu.Lock()
		spec, live := previews["tok"]
		mu.Unlock()
		if !live || !strings.HasSuffix(target, "/apps/wx/") {
			t.Fatalf("loaded %s with the preview live %v", target, live)
		}
		probes = append(probes, probe)
		rep := &PageCheckReport{BodyText: "Wx two", ConsoleErrors: []string{"TypeError: x is undefined"},
			FailedRequests: []string{"http://127.0.0.1:9/favicon.ico -> HTTP 404"}, Screenshot: []byte("jpeg"),
			// The page's live-update feeds stay open by design; the slow
			// data source does not.
			PendingRequests: []string{"http://127.0.0.1:9/api/changes?wait=1&live=a.1", "http://127.0.0.1:9/apps/wx/changes?records=b.2", "http://127.0.0.1:9/apps/wx/data/slow"}}
		if steps, ok := appcheck.DecodeStepsProbe(probe); ok {
			res := appcheck.StepsResult{OK: true}
			for i, st := range steps {
				if st.See != "" && !strings.Contains(string(spec.Sections), st.See) {
					res = appcheck.StepsResult{Step: i + 1, Saw: "the page shows Wx two"}
					break
				}
			}
			b, _ := json.Marshal(res)
			rep.ProbeJSON = string(b)
		}
		return rep, nil
	}

	var cards []agenthost.Block
	c := &toolCtx{user: "alice", udb: UserDB(T.DB, "alice"), project: p, base: base, sessionID: "s1",
		emitBlock: func(b agenthost.Block) { cards = append(cards, b) }}
	td, ok := toolNamed(t, c, "try_page")
	if !ok {
		t.Fatal("an app project has no try_page")
	}
	c.planning = true
	if _, ok := toolNamed(t, c, "try_page"); !ok {
		t.Error("plan mode withholds try_page, which only looks")
	}
	c.planning = false

	out, err := td.Handler(context.Background(), map[string]any{"steps": []any{map[string]any{"see": "Wx two"}, map[string]any{"see": "Total"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Step 2", "did not hold", "console error: TypeError", "What the page shows:\nWx two", "nothing real changed"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "favicon") {
		t.Errorf("a missing favicon reported as the app's:\n%s", out)
	}
	if strings.Contains(out, "changes?") {
		t.Errorf("the page's live-update feeds reported as waiting requests:\n%s", out)
	}
	if !strings.Contains(out, "request still waiting (slow, not failed): http://127.0.0.1:9/apps/wx/data/slow") {
		t.Errorf("a slow request of the app's own is not named as slow:\n%s", out)
	}
	if ended != 1 {
		t.Errorf("the load's preview ended %d times", ended)
	}
	if len(cards) != 2 || cards[0].Data["state"] != "running" || cards[1].Data["state"] != "failed" || cards[1].Data["kind"] != "page" {
		t.Fatalf("the cards: %+v", cards)
	}
	shot := cards[1].Data["shot"]
	if !strings.HasPrefix(shot, "api/shot/") {
		t.Fatalf("the card's screenshot: %q", shot)
	}

	if _, err := td.Handler(context.Background(), map[string]any{"steps": []any{map[string]any{"click": "Save", "see": "Saved"}}}); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("a step that is two steps: %v", err)
	}
	if len(probes) != 1 {
		t.Errorf("a refused step loaded the page anyway: %d loads", len(probes))
	}

	get := func(user string) int { return serve(user, "GET", "/"+shot, "").Code }
	if code := get("alice"); code != http.StatusOK {
		t.Errorf("alice's screenshot: %d", code)
	}
	if code := get("bob"); code != http.StatusNotFound {
		t.Errorf("bob loaded alice's screenshot: %d", code)
	}
}
