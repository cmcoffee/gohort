package codewriter

import (
	"context"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/appagents"
)

// The Code Writer is an app agent reached through CodeWriter: hidden from the
// picker, and offered exactly the tools its turn hands it.
func TestTheCodeWriterIsAnAppAgent(t *testing.T) {
	spec, ok := appagents.AppAgentByID(codeWriterAgentID)
	if !ok {
		t.Fatal("the Code Writer is not registered")
	}
	if !spec.Hidden || !spec.ExactTools {
		t.Errorf("the Code Writer is not hidden and exact-tooled: %+v", spec)
	}
	if !strings.Contains(spec.Prompt, "propose_code") || !strings.Contains(spec.Prompt, "Editor (as it is now)") {
		t.Error("the prompt does not say how the editor arrives and how a change goes back")
	}
	// One mode: the agent decides when to propose, and an unclear request
	// gets a proposal it can be applied or ignored, not a guess either way.
	if !strings.Contains(spec.Prompt, "When it is unclear which they want") {
		t.Error("the prompt does not say what to do when it is unclear whether a change is wanted")
	}
}

// A turn may propose a change to any file but a regex, whose answer is the
// reply: a passage of it, or the whole file. Whether it does is the agent's
// call.
func TestATurnMayProposeAChange(t *testing.T) {
	pub := &agenthost.Publisher{}
	names := func(send chatSendFields) string {
		var out []string
		for _, td := range editTools(pub, send) {
			out = append(out, td.Tool.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names(chatSendFields{Editor: chatEditor{Lang: "go"}}); got != "propose_edit,propose_code" {
		t.Errorf("an ordinary turn offered %q", got)
	}
	if got := names(chatSendFields{Editor: chatEditor{Lang: "regex"}}); got != "" {
		t.Errorf("a regex turn offered %q", got)
	}
}

// propose_code puts the whole proposed file in the conversation as the card
// the editor opens as a diff, and refuses an empty one.
func TestProposeCodePutsTheFileInACard(t *testing.T) {
	var got []agenthost.Block
	pub := &agenthost.Publisher{}
	pub.Bind(func(b agenthost.Block) { got = append(got, b) })
	tool := proposeCodeTool(pub)
	if _, err := tool.Handler(context.Background(), map[string]any{"code": "  ", "summary": "nothing"}); err == nil {
		t.Error("an empty file was proposed")
	}
	if len(got) != 0 {
		t.Fatal("a refused proposal put a card in the conversation")
	}
	if _, err := tool.Handler(context.Background(), map[string]any{"code": "select 1\nwhere x\n", "summary": " Add a filter "}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d cards", len(got))
	}
	b := got[0]
	if b.Type != "text_proposal" || b.ID == "" || b.Text != "Add a filter" || b.Data["find"] != "" || b.Data["replace"] != "select 1\nwhere x" {
		t.Errorf("the card is %+v", b)
	}
	// A reload collapses cards that share a title, so a proposal carries
	// none: each one keeps its own place in the thread.
	if b.Title != "" {
		t.Errorf("the card has a title (%q); a reload would merge every proposal into one", b.Title)
	}
}

// The newest message carries the editor as the send had it: its name,
// language, contents and context, and what the turn may do with them.
func TestTheTurnCarriesTheEditor(t *testing.T) {
	T := &CodeWriterAgent{}
	ctx := context.Background()
	turn, tools := T.editorTurnContext(ctx, "alice", chatSendFields{
		Message: "add a filter",
		Editor:  chatEditor{Name: "report.sql", Lang: "sql", Code: "select 1", Context: "table t(x int)"},
	}, WriterRecord{})
	for _, want := range []string{"Editor (as it is now)", "Name: report.sql", "Language: sql", "sql\nselect 1\n", "Reference context", "table t(x int)", "working in sql"} {
		if !strings.Contains(turn, want) {
			t.Errorf("the turn is missing %q:\n%s", want, turn)
		}
	}
	if len(tools) != 0 {
		t.Errorf("a turn with no sources carried %d source tools", len(tools))
	}
	empty, _ := T.editorTurnContext(ctx, "alice", chatSendFields{Editor: chatEditor{Lang: "go"}}, WriterRecord{})
	if !strings.Contains(empty, "The editor is empty") || !strings.Contains(empty, "(not named yet)") {
		t.Errorf("an empty editor reads as:\n%s", empty)
	}
	re, _ := T.editorTurnContext(ctx, "alice", chatSendFields{Editor: chatEditor{Lang: "regex", Code: "+abc\n-xyz", Context: "three letters"}}, WriterRecord{})
	for _, want := range []string{"Test samples", "+abc", "Target description", "regular expression"} {
		if !strings.Contains(re, want) {
			t.Errorf("a regex turn is missing %q:\n%s", want, re)
		}
	}
}

// A passage the user selected and asked about reaches the turn with how to
// change it, beside the whole file; a regex turn, which has no edit tools,
// is not told to propose one.
func TestTheTurnCarriesTheSelectedPassage(t *testing.T) {
	T := &CodeWriterAgent{}
	ctx := context.Background()
	sel := agenthost.Selection{Text: "where x > 1", Start: 9, End: 20}
	turn, _ := T.editorTurnContext(ctx, "alice", chatSendFields{
		Message: "make this inclusive", Selection: sel,
		Editor: chatEditor{Lang: "sql", Code: "select * where x > 1"},
	}, WriterRecord{})
	if !strings.Contains(turn, "select * where x > 1") || !strings.Contains(turn, "selected this passage") ||
		!strings.Contains(turn, "\nwhere x > 1\n") || !strings.Contains(turn, "propose_edit") {
		t.Errorf("the turn should carry the file and the selected passage:\n%s", turn)
	}
	plain, _ := T.editorTurnContext(ctx, "alice", chatSendFields{Editor: chatEditor{Lang: "sql", Code: "select 1"}}, WriterRecord{})
	if strings.Contains(plain, "selected this passage") {
		t.Errorf("nothing selected says nothing about a passage:\n%s", plain)
	}
	re, _ := T.editorTurnContext(ctx, "alice", chatSendFields{Selection: sel, Editor: chatEditor{Lang: "regex", Code: "+abc"}}, WriterRecord{})
	if strings.Contains(re, "propose_edit") {
		t.Errorf("a regex turn is not told to propose an edit:\n%s", re)
	}
	if !strings.Contains(codeWriterPrompt, "propose_edit") || !strings.Contains(codeWriterPrompt, "selected a passage") {
		t.Error("the prompt should say how a passage is changed")
	}
}
