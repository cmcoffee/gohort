package core

// Tests for the "monitor" artifact type: recipe shape (state, token, and
// instance-local delivery targets stripped; agent refs normalized to names),
// the paused-import review gate with a fresh webhook token, one-shot
// exclusion, embedded-secret refusal, and the dependency walk (watch tool /
// source hook + checker/wake agents).

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// monitorTestDB pins RootDB to a fresh mem store and stubs the agent-name
// resolver, restoring both on cleanup.
func monitorTestDB(t *testing.T, resolve func(owner, key string) (string, bool)) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	savedRoot, savedResolve := RootDB, ResolveAgentNameForExport
	RootDB = db
	ResolveAgentNameForExport = resolve
	t.Cleanup(func() { RootDB, ResolveAgentNameForExport = savedRoot, savedResolve })
	return db
}

func TestMonitorArtifact_ExportStripsAndNormalizes(t *testing.T) {
	db := monitorTestDB(t, func(owner, key string) (string, bool) {
		if owner != "alice" {
			t.Fatalf("resolver called with owner %q", owner)
		}
		if key == "agent-9" {
			return "Watcher", true
		}
		return "", false
	})
	SaveEventMonitor(db, EventMonitor{
		Name: "repo-watch", Owner: "alice", Kind: EventKindWatch,
		WakeBrief: "summarize the change", WakeAgent: "agent-9",
		WakeSession: "sess-1", DeliverChatID: "chat-2", Token: "tok",
		ToolName: "list_commits", ToolArgs: map[string]any{"repo": "gohort"},
		IntervalSeconds: 300,
		LastHash:        "abc", LastBody: "old", LastResult: "r",
		LastBreached: true, LastMatched: true, Paused: true,
		Created: time.Now(), NextCheck: time.Now(), LastFired: time.Now(),
		LastChecked: time.Now(), SchedulerID: "sched-1",
	})

	recipe, err := monitorArtifact{}.ExportArtifact(db, "repo-watch", "alice")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var got EventMonitor
	if err := json.Unmarshal(recipe, &got); err != nil {
		t.Fatalf("recipe: %v", err)
	}
	if got.WakeAgent != "Watcher" {
		t.Fatalf("wake-agent ID must normalize to the agent's name, got %q", got.WakeAgent)
	}
	if got.Owner != "" || got.Token != "" || got.WakeSession != "" || got.DeliverChatID != "" {
		t.Fatalf("owner/token/session targets must not travel: %+v", got)
	}
	if got.LastHash != "" || got.LastBody != "" || got.LastResult != "" || got.LastBreached || got.LastMatched ||
		got.Paused || got.SchedulerID != "" || !got.NextCheck.IsZero() || !got.LastFired.IsZero() ||
		!got.LastChecked.IsZero() || !got.Created.IsZero() {
		t.Fatalf("edge-trigger/scheduler state must not travel: %+v", got)
	}
	if got.ToolName != "list_commits" || got.IntervalSeconds != 300 || got.WakeBrief == "" {
		t.Fatalf("the WHEN+GATE+ACTION shape must travel intact: %+v", got)
	}
	// The stored monitor is untouched by normalization/stripping.
	stored, _ := GetEventMonitor(db, "alice", "repo-watch")
	if stored.WakeAgent != "agent-9" || stored.Token != "tok" {
		t.Fatalf("export must not mutate the stored monitor: %+v", stored)
	}
}

func TestMonitorArtifact_OneShotAndSecretsRefuse(t *testing.T) {
	db := monitorTestDB(t, nil)
	SaveEventMonitor(db, EventMonitor{
		Name: "await-reply", Owner: "alice", Kind: EventKindWatch,
		ToolName: "read_chat", OneShot: true,
	})
	SaveEventMonitor(db, EventMonitor{
		Name: "leaky", Owner: "alice", Kind: EventKindHTTP,
		URL: "https://api.example.com/v1?api_key=sk12345678abc", CompareOp: ">", Threshold: "5",
	})

	if _, err := (monitorArtifact{}).ExportArtifact(db, "await-reply", "alice"); err == nil {
		t.Fatal("a one-shot await must refuse export")
	}
	if _, err := (monitorArtifact{}).ExportArtifact(db, "leaky", "alice"); err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("a URL-embedded key must refuse export, got err=%v", err)
	}
	// One-shots don't list either; the leaky one does (refusal is per-export).
	sels := monitorArtifact{}.ListArtifacts(db)
	if len(sels) != 1 || sels[0].Name != "leaky" || sels[0].Owner != "alice" {
		t.Fatalf("list must skip one-shot awaits: %+v", sels)
	}
}

func TestMonitorArtifact_ImportLandsPausedFreshToken(t *testing.T) {
	db := monitorTestDB(t, nil)
	recipe, _ := json.Marshal(EventMonitor{
		Name: "ci-webhook", Kind: EventKindWebhook,
		WakeBrief: "react to CI", Token: "traveled-token",
		LastHash: "junk", SchedulerID: "junk",
	})

	name, skip, err := monitorArtifact{}.ImportArtifact(db, recipe, "bob")
	if err != nil || skip != "" || name != "ci-webhook" {
		t.Fatalf("import: name=%q skip=%q err=%v", name, skip, err)
	}
	got, ok := GetEventMonitor(db, "bob", "ci-webhook")
	if !ok {
		t.Fatal("imported monitor not found")
	}
	if !got.Paused {
		t.Fatal("an imported monitor must land paused for review")
	}
	if got.Token == "" || got.Token == "traveled-token" {
		t.Fatalf("a webhook import must mint a FRESH token, got %q", got.Token)
	}
	if got.LastHash != "" || got.SchedulerID != "" || got.Created.IsZero() {
		t.Fatalf("import must land with fresh local state: %+v", got)
	}

	// Same name again → skip; nameless → error; one-shot recipe → error.
	_, skip, err = monitorArtifact{}.ImportArtifact(db, recipe, "bob")
	if err != nil || !strings.Contains(skip, "already exists") {
		t.Fatalf("same-named import must skip: skip=%q err=%v", skip, err)
	}
	bad, _ := json.Marshal(EventMonitor{Kind: EventKindWatch})
	if _, _, err = (monitorArtifact{}).ImportArtifact(db, bad, "bob"); err == nil {
		t.Fatal("nameless recipe must error")
	}
	oneshot, _ := json.Marshal(EventMonitor{Name: "await", OneShot: true})
	if _, _, err = (monitorArtifact{}).ImportArtifact(db, oneshot, "bob"); err == nil {
		t.Fatal("one-shot recipe must error")
	}
	// A non-webhook import carries no token at all.
	poll, _ := json.Marshal(EventMonitor{Name: "poller", Kind: EventKindPoll, CheckAgent: "Checker", Check: "done?", IntervalSeconds: 120})
	if _, _, err := (monitorArtifact{}).ImportArtifact(db, poll, "bob"); err != nil {
		t.Fatalf("poll import: %v", err)
	}
	if p, _ := GetEventMonitor(db, "bob", "poller"); p.Token != "" {
		t.Fatalf("non-webhook import must not mint a token: %+v", p)
	}
}

func TestMonitorClosure_CarriesHookAndAgents(t *testing.T) {
	// The bridge story: a watch monitor over a hook-backed tool exports as
	// [monitor, source_hook], and its checker/wake agents are declared so a
	// missing one warns at import. Runs against the REAL registry.
	db := sourceHookTestDB(t) // pins RootDB + empties the hook registry
	savedResolve := ResolveAgentNameForExport
	ResolveAgentNameForExport = func(owner, key string) (string, bool) { return "", false }
	t.Cleanup(func() { ResolveAgentNameForExport = savedResolve })

	SaveSourceHook(db, SourceHook{
		Name: "StatusPage", Type: HookTypeAPI,
		Endpoint: "https://status.example.com/api", ToolName: "status_search",
	})
	SaveEventMonitor(db, EventMonitor{
		Name: "status-watch", Owner: "alice", Kind: EventKindWatch,
		ToolName: "status_search", IntervalSeconds: 600,
		WakeAgent: "Notifier",
	})

	deps := monitorArtifact{}.Dependencies(db, "status-watch", "alice")
	if len(deps) != 2 {
		t.Fatalf("expected [source_hook, agent], got %+v", deps)
	}
	if deps[0].Type != "source_hook" || deps[0].Name != "StatusPage" {
		t.Fatalf("hook-backed watch tool must fold in the hook: %+v", deps[0])
	}
	if deps[1].Type != "agent" || deps[1].Name != "Notifier" || deps[1].Owner != "alice" {
		t.Fatalf("wake agent must be declared: %+v", deps[1])
	}

	b, err := ExportArtifactBundle(db, []ArtifactSel{{Type: "monitor", Name: "status-watch", Owner: "alice"}})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	// The agent dep can't resolve on this install (no agent type store in
	// core tests) and is dropped best-effort; the hook rides along.
	if len(b.Artifacts) != 2 || b.Artifacts[0].Type != "monitor" || b.Artifacts[1].Type != "source_hook" {
		t.Fatalf("expected [monitor, source_hook], got %v", bundleNames(b))
	}
}
