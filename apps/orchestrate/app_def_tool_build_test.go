package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// The pipeline section used to set four URLs and nothing else, so a
// declaratively authored run surface had no Copy Link, no export, no Suggest.
// These pin the knobs that closed that, and — more importantly — the refusals,
// since every one of them replaces a button that would have rendered fine and
// failed on click.

func pipelineSpec() AppSpec {
	return AppSpec{
		Slug: "x", RecordKey: "id", PipelineID: "p",
		Actions:     []AppAction{{Name: "save-run", Script: "print('{}')"}},
		DataSources: []AppDataSource{{Name: "Topic Ideas", Script: "print('[]')"}},
	}
}

func buildPipelineSection(t *testing.T, m map[string]any) (ui.PipelinePanel, error) {
	t.Helper()
	m["kind"] = "pipeline"
	sec, err := buildAppSection(pipelineSpec(), m, nil)
	if err != nil {
		return ui.PipelinePanel{}, err
	}
	panel, ok := sec.Body.(ui.PipelinePanel)
	if !ok {
		t.Fatalf("pipeline section body = %T, want ui.PipelinePanel", sec.Body)
	}
	return panel, nil
}

func TestPipelineToolbarCopyDefaultsToThisRunsLink(t *testing.T) {
	panel, err := buildPipelineSection(t, map[string]any{
		"toolbar": []any{map[string]any{"label": "Copy Link", "method": "copy"}},
	})
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if len(panel.Actions) != 1 {
		t.Fatalf("got %d toolbar buttons, want 1", len(panel.Actions))
	}
	// The whole Copy Link button, without the author spelling out a
	// placeholder they can get subtly wrong for no gain.
	if got := panel.Actions[0].URL; got != "?session={id}" {
		t.Errorf("copy url = %q, want %q", got, "?session={id}")
	}
}

func TestPipelineToolbarPostBindsToADeclaredAction(t *testing.T) {
	panel, err := buildPipelineSection(t, map[string]any{
		"toolbar": []any{map[string]any{
			"label": "Save", "method": "post", "url": "action/save-run?id={id}"}},
	})
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if panel.Actions[0].Method != "post" {
		t.Errorf("method = %q, want post", panel.Actions[0].Method)
	}
}

func TestPipelineToolbarRefusesAnUndeclaredTarget(t *testing.T) {
	_, err := buildPipelineSection(t, map[string]any{
		"toolbar": []any{map[string]any{
			"label": "Save", "method": "post", "url": "action/save_run"}},
	})
	if err == nil {
		t.Fatal("expected a refusal: action names are slugified, so save_run is not save-run")
	}
	// The declared names belong in the error; without them the author's next
	// move is a blind guess or a round trip through action=get.
	if !strings.Contains(err.Error(), "save-run") {
		t.Errorf("error should name the declared actions, got: %v", err)
	}
}

func TestPipelineToolbarRefusesGETOnAnActionEndpoint(t *testing.T) {
	_, err := buildPipelineSection(t, map[string]any{
		"toolbar": []any{map[string]any{
			"label": "Save", "method": "open", "url": "action/save-run"}},
	})
	if err == nil || !strings.Contains(err.Error(), "POST") {
		t.Fatalf("an action endpoint answers POST only; got err=%v", err)
	}
}

// The four the panel supports and a custom app cannot serve. Each fails at
// CLICK time otherwise, which is the worst place to find out.
func TestPipelineToolbarRefusesMethodsWithNoEndpointBehindThem(t *testing.T) {
	for _, method := range []string{"stream", "modal", "related", "load"} {
		_, err := buildPipelineSection(t, map[string]any{
			"toolbar": []any{map[string]any{"label": "X", "method": method, "url": "whatever"}},
		})
		if err == nil {
			t.Errorf("method %q should be refused", method)
			continue
		}
		// The refusal has to explain itself; "unsupported" sends the author
		// looking for a typo that isn't there.
		if len(err.Error()) < 80 || !strings.Contains(err.Error(), method) {
			t.Errorf("method %q refused without a reason: %v", method, err)
		}
	}
}

func TestPipelineToolbarClientTakesAHandlerNameNotAPath(t *testing.T) {
	if _, err := buildPipelineSection(t, map[string]any{
		"toolbar": []any{map[string]any{
			"label": "Print", "method": "client", "url": "data/print"}},
	}); err == nil {
		t.Fatal("a client button's url is a registered handler NAME, not a path")
	}
	panel, err := buildPipelineSection(t, map[string]any{
		"toolbar": []any{map[string]any{
			"label": "Print", "method": "client", "url": "print_transcript"}},
	})
	if err != nil {
		t.Fatalf("a bare handler name should be accepted: %v", err)
	}
	if panel.Actions[0].URL != "print_transcript" {
		t.Errorf("client url = %q", panel.Actions[0].URL)
	}
}

func TestPipelineSuggestBindsToADataSource(t *testing.T) {
	panel, err := buildPipelineSection(t, map[string]any{
		"suggest_script": "Topic Ideas",
	})
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	// Data-source names are slugified into their endpoint.
	if panel.PrefillURL != "data/topic-ideas" {
		t.Errorf("prefill_url = %q, want data/topic-ideas", panel.PrefillURL)
	}
	if panel.PrefillLabel != "Suggest" {
		t.Errorf("prefill_label = %q, want the default", panel.PrefillLabel)
	}
	// Defaults to the field it is going to fill.
	if panel.PrefillTarget != "topic" {
		t.Errorf("prefill_target = %q, want the section's input field", panel.PrefillTarget)
	}
}

// Both of these render a button that fetches, runs the script, and drops the
// answer nowhere. Silent, so they are refused instead.
func TestPipelineSuggestRefusesTargetsAndScriptsThatDoNotExist(t *testing.T) {
	if _, err := buildPipelineSection(t, map[string]any{
		"suggest_script": "no-such-source",
	}); err == nil {
		t.Error("expected a refusal for an undeclared data source")
	}
	if _, err := buildPipelineSection(t, map[string]any{
		"suggest_script": "Topic Ideas", "suggest_target": "nonexistent",
	}); err == nil {
		t.Error("expected a refusal for a target that names no field")
	}
	if _, err := buildPipelineSection(t, map[string]any{
		"suggest_label": "Ideas",
	}); err == nil {
		t.Error("expected a refusal: a label with no script renders no button at all")
	}
}

// A client button with nothing anywhere to register its handler renders fine
// and toasts an error on click. Note it, do not refuse it: the html section is
// a sibling, and buildAppSection cannot see siblings.
func TestClientButtonWithoutAnHTMLSectionIsNoted(t *testing.T) {
	pipelineSec := map[string]any{"kind": "pipeline", "toolbar": []any{
		map[string]any{"label": "Print", "method": "client", "url": "print_transcript"}}}

	notes := appShapeNotes([]any{pipelineSec}, true)
	if !strings.Contains(strings.Join(notes, " "), "uiRegisterClientAction") {
		t.Errorf("expected a note about nothing registering the handler, got %v", notes)
	}

	// With an html section present the note goes away — and it stays away when
	// the html section comes AFTER, because the runtime looks a client handler
	// up at click time. Only block renderers are snapshotted at mount.
	withHTML := appShapeNotes([]any{pipelineSec,
		map[string]any{"kind": "html", "html": "<div></div>"}}, true)
	if strings.Contains(strings.Join(withHTML, " "), "uiRegisterClientAction") {
		t.Errorf("an html section AFTER the pipeline still registers client actions in time: %v", withHTML)
	}
}

// A key the builder reads must be listed in sectionKeys, or the author is told
// their working toolbar was thrown away.
func TestNewPipelineKeysAreNotReportedAsIgnored(t *testing.T) {
	notes := unknownSectionKeyNotes([]any{map[string]any{
		"kind": "pipeline", "toolbar": []any{}, "suggest_script": "s",
		"suggest_label": "l", "suggest_target": "topic",
	}})
	for _, n := range notes {
		if strings.Contains(n, "toolbar") || strings.Contains(n, "suggest_") {
			t.Errorf("a key the builder reads was reported as ignored: %s", n)
		}
	}
}

// meta draws what the PIPELINE promotes. The section only chooses the label,
// the style and the colors.
func TestPipelineMetaFieldsRenderOnTheRow(t *testing.T) {
	panel, err := buildPipelineSection(t, map[string]any{
		"meta": []any{
			map[string]any{"field": "verdict", "style": "text", "truncate": float64(240)},
			map[string]any{"field": "winner", "style": "pill",
				"variants": map[string]any{"For": "#3fb950", "against": "#f85149"}},
		},
	})
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if len(panel.SessionMetaFields) != 2 {
		t.Fatalf("got %d meta fields, want 2", len(panel.SessionMetaFields))
	}
	if panel.SessionMetaFields[0].Truncate != 240 {
		t.Errorf("truncate = %d", panel.SessionMetaFields[0].Truncate)
	}
	// Variants are matched against a lowercased value at render time, so the
	// keys are lowercased here or "For" never matches.
	if got := panel.SessionMetaFields[1].Variants["for"]; got != "#3fb950" {
		t.Errorf("variant keys must be lowercased for matching, got %v", panel.SessionMetaFields[1].Variants)
	}
}

func TestPipelineMetaRefusesAStyleThatDrawsNothing(t *testing.T) {
	if _, err := buildPipelineSection(t, map[string]any{
		"meta": []any{map[string]any{"field": "winner", "style": "chip"}},
	}); err == nil {
		t.Error("expected a refusal for an unknown style")
	}
	if _, err := buildPipelineSection(t, map[string]any{
		"meta": []any{map[string]any{"style": "pill"}},
	}); err == nil {
		t.Error("expected a refusal for a meta entry with no field")
	}
	// Only a pill is colored by value, so variants anywhere else is an
	// instruction that quietly does nothing.
	if _, err := buildPipelineSection(t, map[string]any{
		"meta": []any{map[string]any{"field": "winner", "style": "text",
			"variants": map[string]any{"for": "#3fb950"}}},
	}); err == nil {
		t.Error("expected a refusal for variants on a non-pill style")
	}
}

// The app and the pipeline are edited separately, so this is a note rather
// than a refusal — but the common case is a guessed name, and it should be
// caught while the author is looking.
func TestMetaFieldsThePipelineDoesNotPromoteAreNoted(t *testing.T) {
	sections := []any{map[string]any{"kind": "pipeline", "meta": []any{
		map[string]any{"field": "winner"}, map[string]any{"field": "guessed"}}}}

	notes := strings.Join(appSessionMetaNotes(sections, []string{"judge.winner"}), " ")
	if !strings.Contains(notes, "guessed") {
		t.Errorf("expected the unpromoted field to be named, got %q", notes)
	}
	if strings.Contains(notes, "winner,") {
		t.Errorf("a promoted field should not be reported as missing: %q", notes)
	}

	// Nothing promoted at all is the more common mistake, and the fix is on
	// the pipeline rather than the app, so the note has to say which.
	none := strings.Join(appSessionMetaNotes(sections, nil), " ")
	if !strings.Contains(none, "session_meta") {
		t.Errorf("expected the note to name the pipeline-side fix, got %q", none)
	}

	if n := appSessionMetaNotes(sections, []string{"judge.winner", "judge.guessed"}); len(n) != 0 {
		t.Errorf("everything promoted should be quiet, got %v", n)
	}
}

// An editable workbench is a document the person writes with the agent: it
// opens in an editor that saves by merging into the record, its selection
// offers the section's quick actions, and the "Add to" button goes, since
// every change comes as a proposal. A plain workbench is as it was.
func TestAnEditableWorkbenchIsCoauthored(t *testing.T) {
	spec := AppSpec{AgentID: "lyricist", RecordKey: "id"}
	m := map[string]any{
		"kind": "workbench", "body_field": "lyrics", "item_noun": "song", "editable": true,
		"selection_actions": []any{
			map[string]any{"label": "Tighten", "prompt": "Tighten this passage."},
			"Rhyme it",
			map[string]any{"prompt": "no label"},
		},
	}
	wb, err := buildWorkbench(spec, m)
	if err != nil {
		t.Fatal(err)
	}
	if !wb.ProposeEdits || wb.EditURL != "record?id={id}" || wb.EditField != "lyrics" {
		t.Errorf("an editor over the body, saved by merge: %+v", wb)
	}
	if wb.CoAuthor || wb.CoAuthorVerb != "" {
		t.Error("no Add to button on a document the agent only proposes to")
	}
	want := []ui.SelectionAction{{Label: "Tighten", Prompt: "Tighten this passage."}, {Label: "Rhyme it"}}
	if len(wb.SelectionActions) != 2 || wb.SelectionActions[0] != want[0] || wb.SelectionActions[1] != want[1] {
		t.Errorf("quick actions %+v", wb.SelectionActions)
	}
	if chat, ok := wb.Chat.(ui.AgentLoopPanel); !ok || !strings.Contains(chat.EmptyText, "Highlight part of the song") {
		t.Errorf("the chat says how to start: %+v", wb.Chat)
	}
	if notes := unknownSectionKeyNotes([]any{m}); len(notes) != 0 {
		t.Errorf("editable and selection_actions are workbench keys: %v", notes)
	}

	plain, err := buildWorkbench(spec, map[string]any{"kind": "workbench", "body_field": "lyrics"})
	if err != nil {
		t.Fatal(err)
	}
	if plain.ProposeEdits || plain.EditURL != "" || !plain.CoAuthor {
		t.Errorf("a plain workbench is as it was: %+v", plain)
	}
}

// A workbench whose records keep files lists them under the document from
// the field it names, and document_actions put their prompt in the chat.
func TestAWorkbenchKeepsFilesAndHasDocumentActions(t *testing.T) {
	spec := AppSpec{AgentID: "lyricist", RecordKey: "id"}
	m := map[string]any{
		"kind": "workbench", "body_field": "lyrics", "item_noun": "song", "editable": true,
		"files_field": "takes", "files_label": "Takes",
		"document_actions": []any{map[string]any{"label": "Render", "prompt": "Render this song and keep the take."}, "Check the meter"},
	}
	wb, err := buildWorkbench(spec, m)
	if err != nil {
		t.Fatal(err)
	}
	if wb.FilesField != "takes" || wb.FilesLabel != "Takes" || wb.FileURL != "file?id={id}&name={name}" {
		t.Errorf("files: %+v", wb)
	}
	if len(wb.ViewerActions) != 2 || wb.ViewerActions[0].Kind != "compose" || wb.ViewerActions[0].Compose != "Render this song and keep the take." || wb.ViewerActions[1].Compose != "Check the meter" {
		t.Errorf("document actions: %+v", wb.ViewerActions)
	}
	if notes := unknownSectionKeyNotes([]any{m}); len(notes) != 0 {
		t.Errorf("files_field, files_label and document_actions are workbench keys: %v", notes)
	}
	for _, clash := range []string{"lyrics", "title", "id"} {
		if _, err := buildWorkbench(spec, map[string]any{"kind": "workbench", "body_field": "lyrics", "files_field": clash}); err == nil {
			t.Errorf("files_field %q shares a field with the workbench", clash)
		}
	}
	plain, _ := buildWorkbench(spec, map[string]any{"kind": "workbench", "body_field": "lyrics"})
	if plain.FilesField != "" || plain.FileURL != "" || len(plain.ViewerActions) != 0 {
		t.Errorf("a workbench without them has neither: %+v", plain)
	}
}
