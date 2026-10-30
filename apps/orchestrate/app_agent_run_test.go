package orchestrate

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
	mustSaveAgent(t, udb, AgentRecord{ID: "qm", Name: "Quartermaster", Owner: "alice", OwningApp: "voidrunner"})
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
	SavePipelineDef(udb, PipelineDef{ID: "p2", Owner: "alice", Name: "loot", OwningApp: "voidrunner", Stages: []PipelineStage{{Name: "a", Kind: StageWorker}}})
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
