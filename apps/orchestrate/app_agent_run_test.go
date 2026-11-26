package orchestrate

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/appreach"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An app's backend runs its own agents and pipelines: the app's agent_id /
// pipeline_id, or one made for it (owning_app), and nothing else its owner
// has. An app having a brain is a smaller promise than its scripts driving
// every agent in the account.
func TestAnAppRunsOnlyItsOwnAgentsAndPipelines(t *testing.T) {
	T := &OrchestrateApp{}
	T.DB = &DBase{Store: kvlite.MemStore()}
	udb := UserDB(T.DB, "alice")
	mustSaveAgent(t, udb, AgentRecord{ID: "dm", Name: "Voidrunner DM", Owner: "alice"})
	mustSaveAgent(t, udb, AgentRecord{ID: "qm", Name: "Quartermaster", Owner: "alice", OwningApp: "voidrunner", appEdit: "Builder"})
	mustSaveAgent(t, udb, AgentRecord{ID: "mail", Name: "Mailer", Owner: "alice"})

	if a, err := T.appOwnAgent("alice", "voidrunner", "Voidrunner DM", ""); err != nil || a.ID != "dm" {
		t.Errorf("the app's own agent: %+v %v", a, err)
	}
	if a, err := T.appOwnAgent("alice", "voidrunner", "dm", "Quartermaster"); err != nil || a.ID != "qm" {
		t.Errorf("an agent made for the app: %+v %v", a, err)
	}
	if _, err := T.appOwnAgent("alice", "voidrunner", "dm", "Mailer"); err == nil || !strings.Contains(err.Error(), "not one of this app's agents") {
		t.Errorf("another of the owner's agents was reachable: %v", err)
	}
	if _, err := T.appOwnAgent("alice", "voidrunner", "", ""); err == nil || !strings.Contains(err.Error(), "no agent to run") {
		t.Errorf("no agent_id and none named: %v", err)
	}

	SavePipelineDef(udb, PipelineDef{ID: "p1", Owner: "alice", Name: "turn", Stages: []PipelineStage{{Name: "a", Kind: StageWorker}}})
	SavePipelineDefBy(udb, PipelineDef{ID: "p2", Owner: "alice", Name: "loot", OwningApp: "voidrunner", Stages: []PipelineStage{{Name: "a", Kind: StageWorker}}}, "create", "Builder")
	SavePipelineDef(udb, PipelineDef{ID: "p3", Owner: "alice", Name: "research", Stages: []PipelineStage{{Name: "a", Kind: StageWorker}}})
	if d, err := T.appOwnPipeline("alice", "voidrunner", "p1", ""); err != nil || d.ID != "p1" {
		t.Errorf("the app's own pipeline: %+v %v", d, err)
	}
	if d, err := T.appOwnPipeline("alice", "voidrunner", "p1", "loot"); err != nil || d.ID != "p2" {
		t.Errorf("a pipeline made for the app: %+v %v", d, err)
	}
	if _, err := T.appOwnPipeline("alice", "voidrunner", "p1", "research"); err == nil || !strings.Contains(err.Error(), "not one of this app's pipelines") {
		t.Errorf("another of the owner's pipelines was reachable: %v", err)
	}
}

// A save says when a script runs an agent or a pipeline it has not declared,
// a call made inside a library it imports included, or when the app has none
// to run.
func TestSaveNotesUndeclaredRunAgent(t *testing.T) {
	spec := AppSpec{Slug: "game", AgentID: "dm",
		Libraries: map[string]string{"engine": "from gohort import run_agent\ndef turn(s):\n    return run_agent(s)\n"},
		DataSources: []AppDataSource{
			{Name: "direct", Script: "from gohort import run_agent\nprint(run_agent('x'))", Capabilities: []string{"fetch"}},
			{Name: "via-lib", Script: "from engine import turn\nprint(turn('x'))"},
			{Name: "fine", Script: "from engine import turn\nprint(turn('x'))", Capabilities: []string{"run_agent"}},
			{Name: "pipe", Script: "from gohort import run_pipeline\nprint(run_pipeline('x'))", Capabilities: []string{"run_pipeline"}},
		}}
	notes := strings.Join(appToolCapNotes("u", spec), "\n")
	for _, want := range []string{
		`data source "direct" calls run_agent(...) but does not declare "run_agent"`,
		`data source "via-lib" calls run_agent(...) but does not declare "run_agent"`,
		`data source "pipe" calls run_pipeline(...), and the app has no pipeline`,
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("missing note %q in:\n%s", want, notes)
		}
	}
	if strings.Contains(notes, `"fine"`) {
		t.Errorf("a declared call was noted:\n%s", notes)
	}
}

// What an app script's agent run makes comes back to the script as files in
// its folder. The reply was text alone, so a song the agent rendered reached
// the script as a sentence saying it was made, which it could not keep on the
// song's record.
func TestAnAppScriptGetsTheFilesItsAgentMade(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	t.Cleanup(func() { SetWorkspacesDir("") })
	df := newDispatchFixture(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "r1", Name: "apptest_render", Args: map[string]any{"take": "slow take"}}}},
		FakeTurn{Content: "Rendered the slow take.", Repeat: true})
	dir := filepath.Join(t.TempDir(), "run-1")
	text, files, _, err := df.app.AppAgentRun(context.Background(), "u", "u", "studio", df.agent.ID, "", "render it", dir)
	if err != nil {
		t.Fatal(err)
	}
	if text != "Rendered the slow take." {
		t.Errorf("reply: %q", text)
	}
	// Written and attached, it is one file, not two.
	if len(files) != 1 || files[0] != "take.mp3" {
		t.Fatalf("files: %v", files)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "take.mp3")); err != nil || string(b) != "ID3 slow take" {
		t.Errorf("the take in the script's folder: %q %v", b, err)
	}
}

// apptest_render renders a take the way a music tool does: it writes the
// file in the agent's folder and attaches it, without asking first.
type appTestRender struct{}

func (appTestRender) Name() string { return "apptest_render" }
func (appTestRender) Desc() string { return "Render a take." }
func (appTestRender) Params() map[string]ToolParam {
	return map[string]ToolParam{"take": {Type: "string", Description: "Which take."}}
}
func (appTestRender) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("needs a session")
}
func (appTestRender) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	raw := []byte("ID3 " + stringArg(args, "take"))
	if err := os.WriteFile(filepath.Join(sess.WorkspaceDir, "take.mp3"), raw, 0600); err != nil {
		return "", err
	}
	sess.AppendVideo(base64.StdEncoding.EncodeToString(raw))
	return "rendered take.mp3", nil
}

func init() { RegisterChatTool(appTestRender{}) }

// Media a run attached without writing a file (a tool that hands its audio
// straight back) is written out under a name its bytes give it.
func TestAttachedMediaIsWrittenForTheScript(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run-2")
	returned := &ToolSession{}
	returned.AppendVideo(base64.StdEncoding.EncodeToString([]byte("ID3\x04\x00 a take")))
	returned.AppendImage(base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n cover")))
	returned.AppendFile(FileAttachment{Name: "lyrics/final.pdf", Data: base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 words"))})
	got := handToScript("Composer", returned, dir)
	if strings.Join(got, ",") != "attached.png,attached.mp3,final.pdf" {
		t.Fatalf("files: %v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "attached.mp3")); string(b) != "ID3\x04\x00 a take" {
		t.Errorf("the take: %q", b)
	}
	if got := handToScript("Composer", &ToolSession{}, filepath.Join(t.TempDir(), "run-3")); got != nil {
		t.Errorf("a run that made nothing handed back %v", got)
	}
}

// A script's agent run acts as the app's owner whoever is using the app: what
// it returns goes to the owner's script, so it must not have reached the
// person's own accounts. When someone else is using the app the run carries
// the non-owner mark, so the owner's guardrails judge it as their request.
func TestAnAppScriptsAgentRunsAsTheOwner(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	t.Cleanup(func() { SetWorkspacesDir("") })
	for _, tc := range []struct {
		viewer   string
		nonOwner bool
	}{{"bob", true}, {"u", false}} {
		appTestWho = appTestWhoSeen{}
		df := newDispatchFixture(t,
			FakeTurn{ToolCalls: []ToolCall{{ID: "w1", Name: "apptest_whoami", Args: map[string]any{}}}},
			FakeTurn{Content: "Done.", Repeat: true})
		if _, _, _, err := df.app.AppAgentRun(context.Background(), "u", tc.viewer, "studio", df.agent.ID, "", "who are you", t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if !appTestWho.ran || appTestWho.user != "u" {
			t.Errorf("used by %s, the run acted as %q (ran=%v), want the owner", tc.viewer, appTestWho.user, appTestWho.ran)
		}
		if appTestWho.nonOwner != tc.nonOwner {
			t.Errorf("used by %s, the non-owner mark is %v, want %v", tc.viewer, appTestWho.nonOwner, tc.nonOwner)
		}
	}
}

// The app's credential rules, put on the run's context by the apps host,
// reach the sessions the agent's tools run on, so its calls meet the app's
// choices (core/appreach) and not the owner's full reach.
func TestAnAppScriptsAgentRunCarriesTheAppsRules(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	t.Cleanup(func() { SetWorkspacesDir("") })
	appTestWho = appTestWhoSeen{}
	df := newDispatchFixture(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "w1", Name: "apptest_whoami", Args: map[string]any{}}}},
		FakeTurn{Content: "Done.", Repeat: true})
	ctx := appreach.With(context.Background(), appreach.Rules{Person: "bob"})
	if _, _, _, err := df.app.AppAgentRun(ctx, "u", "bob", "studio", df.agent.ID, "", "who are you", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if !appTestWho.ran || appTestWho.user != "u" || appTestWho.person != "bob" {
		t.Errorf("the tool's session: %+v, want the owner's run carrying bob's rules", appTestWho)
	}
	to := carryHandOff(ctx, context.Background())
	if r, ok := appreach.From(to); !ok || r.Person != "bob" {
		t.Errorf("a hand-off to a context of its own dropped the rules: %+v %v", r, ok)
	}
}

type appTestWhoSeen struct {
	ran, nonOwner bool
	user, person  string
}

var appTestWho appTestWhoSeen

// apptest_whoami says whose run it is in.
type appTestWhoami struct{}

func (appTestWhoami) Name() string                       { return "apptest_whoami" }
func (appTestWhoami) Desc() string                       { return "Say who is running." }
func (appTestWhoami) Params() map[string]ToolParam       { return map[string]ToolParam{} }
func (appTestWhoami) Run(map[string]any) (string, error) { return "", fmt.Errorf("needs a session") }
func (appTestWhoami) RunWithSession(_ map[string]any, sess *ToolSession) (string, error) {
	r, _ := appreach.From(sess.Context())
	appTestWho = appTestWhoSeen{ran: true, user: sess.Username, nonOwner: nonOwnerRequester(sess.Context()), person: r.Person}
	return "ok", nil
}

func init() { RegisterChatTool(appTestWhoami{}) }

// Everyone using a shared app has their own memory of its agent. What Bob
// tells it lands in his copy, the one the app's chat keeps for him, never in
// the owner's, and a run for Cara or for the owner does not hear it. The run
// itself stays the owner's. Before, every person's facts pooled into the
// owner's memory and each read back the others' (a PIN told to the app by
// one person was recalled for the next).
func TestAnAppsAgentRemembersEachPersonApart(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	t.Cleanup(func() { SetWorkspacesDir("") })
	df := newDispatchFixture(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "m1", Name: "remember", Args: map[string]any{
			"content": "Bob's dog is called Biscuit.", "pin": true, "origin": "told", "domain": "self"}}}},
		FakeTurn{Content: "OK.", Repeat: true})
	ns := factsNamespace(df.agent.ID)
	knows := func(user string) bool {
		for _, f := range ListMemoryFacts(UserDB(df.app.DB, user), ns) {
			if strings.Contains(f.Note, "Biscuit") {
				return true
			}
		}
		return false
	}
	if _, _, _, err := df.app.AppAgentRun(context.Background(), "u", "bob", "studio", df.agent.ID, "", "Remember that my dog is Biscuit.", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if !knows("bob") {
		t.Error("what bob told the app's agent is not in his own copy of its memory")
	}
	if knows("u") {
		t.Error("what bob told the app's agent went into the owner's memory")
	}
	for _, who := range []string{"cara", "u"} {
		from := df.llm.Calls()
		if _, _, _, err := df.app.AppAgentRun(context.Background(), "u", who, "studio", df.agent.ID, "", "What is my dog called?", t.TempDir()); err != nil {
			t.Fatal(err)
		}
		for i := from; i < df.llm.Calls(); i++ {
			if strings.Contains(df.llm.Config(i).SystemPrompt+df.llm.Prompt(i), "Biscuit") {
				t.Errorf("a run for %s was shown what bob told the app's agent", who)
			}
		}
	}
}

// The person an app's run remembers for keeps it in a dispatch session of its
// own, and only when they are not the one the run is for already.
func TestAnAppsRunForSomeoneElseHasItsOwnSession(t *testing.T) {
	ctx := withAppMemoryPerson(context.Background(), "bob")
	if got := appMemorySessSuffix(ctx, "u"); got != ":for:bob" {
		t.Errorf("suffix for bob's run on u's account = %q", got)
	}
	if got := appMemorySessSuffix(ctx, "bob"); got != "" {
		t.Errorf("a run already bob's got a suffix %q", got)
	}
	if got := appMemorySessSuffix(context.Background(), "u"); got != "" {
		t.Errorf("an ordinary run got a suffix %q", got)
	}
}

// An app's script continuing a thread with its agent: the next call in the
// same thread hears the last, while another person, the same person from
// another key, and a fresh start do not. The thread is kept in the person's
// own store, never the owner's, and stays out of their session list.
func TestAnAppsThreadIsTheCallersOwn(t *testing.T) {
	SetWorkspacesDir(t.TempDir())
	t.Cleanup(func() { SetWorkspacesDir("") })
	df := newDispatchFixture(t, FakeTurn{Content: "Noted.", Repeat: true})
	run := func(who, key, thread string, fresh bool, prompt string) string {
		t.Helper()
		ctx := agenthost.WithAppThread(context.Background(), agenthost.AppThread{Name: thread, Key: key, Fresh: fresh})
		from := df.llm.Calls()
		if _, _, _, err := df.app.AppAgentRun(ctx, "u", who, "studio", df.agent.ID, "", prompt, t.TempDir()); err != nil {
			t.Fatal(err)
		}
		var seen strings.Builder
		for i := from; i < df.llm.Calls(); i++ {
			seen.WriteString(df.llm.Config(i).SystemPrompt + df.llm.Prompt(i))
		}
		return seen.String()
	}
	run("bob", "k1", "chat", false, "My locker is number 4417.")
	if got := run("bob", "k1", "chat", false, "Which locker?"); !strings.Contains(got, "4417") {
		t.Error("the next call in bob's thread did not hear the last")
	}
	for _, c := range []struct{ who, key, thread string }{{"cara", "k9", "chat"}, {"bob", "k2", "chat"}, {"bob", "k1", "other"}} {
		if got := run(c.who, c.key, c.thread, false, "Which locker?"); strings.Contains(got, "4417") {
			t.Errorf("%s on key %s in thread %q heard bob's k1 thread", c.who, c.key, c.thread)
		}
	}
	if got := run("bob", "k1", "chat", true, "Which locker?"); strings.Contains(got, "4417") {
		t.Error("a fresh start still heard the thread")
	}
	sid, _ := appThreadSessionID("studio", "k1", "chat")
	if _, ok := loadChatSession(UserDB(df.app.DB, "bob"), df.agent.ID, sid); !ok {
		t.Error("bob's thread is not in his own store")
	}
	if _, ok := loadChatSession(UserDB(df.app.DB, "u"), df.agent.ID, sid); ok {
		t.Error("bob's thread was kept in the owner's store")
	}
	for _, s := range listChatSessions(UserDB(df.app.DB, "bob"), df.agent.ID) {
		if strings.HasPrefix(s.ID, appThreadPrefix) {
			t.Errorf("an app thread is in bob's session list: %s", s.ID)
		}
	}
	// Without a thread a call stands alone, as before.
	run("bob", "k1", "", false, "My bike is red.")
	if got := run("bob", "k1", "", false, "What colour is my bike?"); strings.Contains(got, "bike is red") {
		t.Error("a call with no thread heard the one before")
	}
}

// The daily pass removes a thread left unused for appThreadIdle, and keeps
// one in use and every other session.
func TestAnIdleAppThreadIsRemoved(t *testing.T) {
	df := newDispatchFixture(t)
	udb := UserDB(df.app.DB, "bob")
	old, _ := appThreadSessionID("studio", "", "old")
	live, _ := appThreadSessionID("studio", "k1", "live")
	now := time.Now()
	for _, s := range []ChatSession{
		{ID: old, AgentID: df.agent.ID, Created: now.Add(-40 * 24 * time.Hour), LastAt: now.Add(-31 * 24 * time.Hour)},
		{ID: live, AgentID: df.agent.ID, Created: now.Add(-40 * 24 * time.Hour), LastAt: now.Add(-2 * 24 * time.Hour)},
		{ID: "ordinary", AgentID: df.agent.ID, Created: now.Add(-90 * 24 * time.Hour), LastAt: now.Add(-90 * 24 * time.Hour)},
	} {
		udb.Set(sessionTable(df.agent.ID), s.ID, s)
	}
	if n := sweepIdleAppThreads(udb, now); n != 1 {
		t.Errorf("removed %d, want 1", n)
	}
	for id, want := range map[string]bool{old: false, live: true, "ordinary": true} {
		if _, ok := loadChatSession(udb, df.agent.ID, id); ok != want {
			t.Errorf("%s kept=%v, want %v", id, ok, want)
		}
	}
	if _, err := appThreadSessionID("studio", "", strings.Repeat("x", appThreadNameMax+1)); err == nil {
		t.Error("an over-long thread name was taken")
	}
}
