package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// searchConnector saves a search connector named name, reaching srvURL (or
// nowhere), approved or not.
func searchConnector(t *testing.T, name, srvURL string, approved bool) {
	t.Helper()
	if srvURL == "" {
		srvURL = "https://find.example.test"
	}
	spec := map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": srvURL, "auth": map[string]any{"none": true},
			"operations": map[string]any{"find": map[string]any{"url": "/find?q={query}"}}},
		"provides": []any{map[string]any{"capability": "web.search", "steps": []any{map[string]any{"call": "find"}},
			"output": map[string]any{"results": map[string]any{"path": "hits", "fields": map[string]any{"title": "name", "url": "href"}}}}},
	}
	raw, _ := json.Marshal(spec)
	if err := SaveConnector(RootDB, Connector{Name: name, Kind: Kind, Spec: raw}); err != nil {
		t.Fatal(err)
	}
	if approved {
		if err := ApproveConnector(RootDB, name); err != nil {
			t.Fatal(err)
		}
	}
}

func inUse(name string) bool {
	c, _ := GetConnector(RootDB, name)
	return c.Approved
}

// A services connector is in use exactly while a page names it: Web Search's
// engine, a Research Source that is on, a flow's step, the image or music
// lists offered to agents. Taken off every page, it is out of use.
func TestAConnectorIsInUseOnlyWhilePagesNameIt(t *testing.T) {
	world(t)
	for _, n := range []string{"engine", "papers", "idle", "pics", "tunes", "stepper"} {
		searchConnector(t, n, "", true)
	}
	RootDB.Set(SearchTable, "provider", "service:engine")
	RootDB.Set(ImageTable, "agents", []string{"pics"})
	RootDB.Set(MusicTable, "agents", []string{"tunes"})
	RootDB.Set(FlowTable, "f", Flow{Name: "f", Steps: []FlowStep{{ID: "a", Connector: "stepper", Capability: "web.search"}}})
	if err := SaveResearchSource(RootDB, ResearchSource{Connector: "papers", Always: true}); err != nil {
		t.Fatal(err)
	}
	Settle(RootDB)
	uses := TieIns(RootDB)
	for name, page := range map[string]string{"engine": "Web Search", "papers": "Research Sources",
		"pics": "Image Generation (offered to agents)", "tunes": "Music (offered to agents)", "stepper": "Flow f"} {
		if !slices.Contains(uses[name], page) {
			t.Errorf("%s: pages %v, want %s", name, uses[name], page)
		}
		if !inUse(name) {
			t.Errorf("%s is named by %s and not in use", name, page)
		}
	}
	if inUse("idle") || len(uses["idle"]) != 0 {
		t.Errorf("a connector no page names is in use: %v", uses["idle"])
	}

	// Turning the research source off, or naming another engine, takes the
	// connector out of use.
	if err := SaveResearchSource(RootDB, ResearchSource{Connector: "papers", Always: true, Off: true}); err != nil {
		t.Fatal(err)
	}
	RootDB.Set(SearchTable, "provider", "duckduckgo")
	Settle(RootDB)
	if inUse("papers") || inUse("engine") {
		t.Errorf("taken off their pages, papers in use %v, engine in use %v", inUse("papers"), inUse("engine"))
	}
}

// A connector changed by someone not an administrator is held out of use,
// though a page names it, until an administrator accepts the change by
// saving a page that names it.
func TestAChangeIsHeldUntilAnAdministratorAccepts(t *testing.T) {
	world(t)
	searchConnector(t, "engine", "", true)
	RootDB.Set(SearchTable, "provider", "service:engine")
	Settle(RootDB)
	if !inUse("engine") {
		t.Fatal("named by Web Search, not in use")
	}
	MarkChanged(RootDB, "engine", "someone")
	Settle(RootDB)
	if inUse("engine") {
		t.Error("a change waiting on acceptance left the connector in use")
	}
	if ch, ok := PendingChange(RootDB, "engine"); !ok || ch.By != "someone" {
		t.Errorf("the change waiting: %+v %v", ch, ok)
	}
	Settle(RootDB, "other")
	if inUse("engine") {
		t.Error("accepting another connector accepted this one")
	}
	Settle(RootDB, "engine")
	if !inUse("engine") {
		t.Error("accepted, still out of use")
	}
	if _, ok := PendingChange(RootDB, "engine"); ok {
		t.Error("accepted, still waiting")
	}
}

// Once, the image and music connectors that were approved are offered to
// agents, so they keep what being approved gave them: every image one but
// the default, which agents already reach.
func TestTheFirstStartOffersWhatWasApproved(t *testing.T) {
	world(t)
	music := map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": "https://song.example.test", "auth": map[string]any{"none": true},
			"operations": map[string]any{"song": map[string]any{"method": "POST", "path": "/song", "body": map[string]any{"prompt": "{prompt}"}, "read": "file"}}},
		"provides": []any{map[string]any{"capability": "music.generate", "steps": []any{map[string]any{"call": "song"}},
			"output": map[string]any{"audio": map[string]any{"from": "body"}}}},
	}
	install(t, "tunes", music)
	img := map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": "https://img.example.test", "auth": map[string]any{"none": true},
			"operations": map[string]any{"draw": map[string]any{"method": "POST", "path": "/draw", "body": map[string]any{"prompt": "{prompt}"}}}},
		"provides": []any{map[string]any{"capability": "image.generate", "steps": []any{map[string]any{"call": "draw"}},
			"output": map[string]any{"image": map[string]any{"path": "b64", "as": "base64"}}}},
	}
	install(t, "sketcher", img)
	install(t, "painter", img)
	raw, _ := json.Marshal(img)
	if err := SaveConnector(RootDB, Connector{Name: "unused-art", Kind: Kind, Spec: raw}); err != nil {
		t.Fatal(err)
	}
	RootDB.Set(ImageTable, "provider", "painter")
	MigrateTieIns(RootDB)
	var images, songs []string
	RootDB.Get(ImageTable, "agents", &images)
	RootDB.Get(MusicTable, "agents", &songs)
	if !slices.Equal(images, []string{"sketcher"}) || !slices.Equal(songs, []string{"tunes"}) {
		t.Errorf("offered to agents: images %v, music %v", images, songs)
	}
	for _, n := range []string{"tunes", "sketcher", "painter"} {
		if !inUse(n) {
			t.Errorf("%s was approved and is offered or the default, but out of use", n)
		}
	}
	if inUse("unused-art") {
		t.Error("an unapproved connector came into use")
	}
	// Once: taken off the list afterwards, it is not put back.
	RootDB.Set(ImageTable, "agents", []string{})
	MigrateTieIns(RootDB)
	RootDB.Get(ImageTable, "agents", &images)
	if len(images) != 0 || inUse("sketcher") {
		t.Errorf("the seed ran twice: %v, in use %v", images, inUse("sketcher"))
	}
}

// An administrator's test on a settings page reaches the connector its form
// names before a save puts it to use, and only that test does: a call
// without its context, or of another connector, is still refused.
func TestAnAdministratorsPreviewReachesOnlyItsConnector(t *testing.T) {
	world(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"hits":[{"name":"A","href":"https://a.example/1"}]}`))
	}))
	defer srv.Close()
	searchConnector(t, "trial", srv.URL, false)
	searchConnector(t, "other", srv.URL, false)
	in := map[string]any{"query": "q"}
	if _, err := Call(context.Background(), nil, "trial", "web.search", in); err == nil || !strings.Contains(err.Error(), "not in use") {
		t.Fatalf("an unused connector answered: %v", err)
	}
	ctx, end := Preview(context.Background(), "trial")
	if _, err := Call(ctx, nil, "trial", "web.search", in); err != nil {
		t.Errorf("the preview did not reach it: %v", err)
	}
	if _, err := Call(context.Background(), nil, "trial", "web.search", in); err == nil {
		t.Error("a call outside the preview reached it while the preview ran")
	}
	if _, err := Call(ctx, nil, "other", "web.search", in); err == nil {
		t.Error("the preview reached a connector it does not name")
	}
	end()
	if _, err := Call(ctx, nil, "trial", "web.search", in); err == nil {
		t.Error("the preview's context still reaches it after it ended")
	}

	// A test of several connectors (a flow's steps) previews each.
	ctx, endTrial := Preview(context.Background(), "trial")
	both, endOther := Preview(ctx, "other")
	for _, n := range []string{"trial", "other"} {
		if _, err := Call(both, nil, n, "web.search", in); err != nil {
			t.Errorf("previewing two, %s: %v", n, err)
		}
	}
	endOther()
	if _, err := Call(both, nil, "other", "web.search", in); err == nil {
		t.Error("an ended inner preview still reaches its connector")
	}
	if _, err := Call(both, nil, "trial", "web.search", in); err != nil {
		t.Errorf("ending the inner preview ended the outer: %v", err)
	}
	endTrial()
}

// A model connector is read for a tier while an administrator's LLM test
// previews it, and not before or after.
func TestAModelConnectorIsReadOnlyUnderPreview(t *testing.T) {
	world(t)
	spec := map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": "https://chat.example.test", "auth": map[string]any{"none": true},
			"operations": map[string]any{"models": map[string]any{"path": "/models"}}},
		"provides": []any{map[string]any{"capability": "llm.chat", "dialect": "openai"}},
	}
	raw, _ := json.Marshal(spec)
	if err := SaveConnector(RootDB, Connector{Name: "chat", Kind: Kind, Spec: raw}); err != nil {
		t.Fatal(err)
	}
	if _, err := modelTarget("chat", false, nil); err == nil {
		t.Fatal("an unused model connector was read")
	}
	_, end := Preview(context.Background(), "chat")
	_, end2 := Preview(context.Background(), "chat")
	if _, err := modelTarget("chat", false, nil); err != nil {
		t.Errorf("under preview: %v", err)
	}
	end()
	if _, err := modelTarget("chat", false, nil); err != nil {
		t.Errorf("one of two previews ended, and the other lost it: %v", err)
	}
	end2()
	if _, err := modelTarget("chat", false, nil); err == nil {
		t.Error("read after every preview ended")
	}
}
