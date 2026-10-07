package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A stage can be drawn as a card of its values rather than as text: what a
// debate's verdict or a scored item looks like, declared as data so an app
// Builder writes can have it without any code.

// A card stage's block says how it is drawn, and its fields follow as the
// block closes, so the surface draws values instead of the text rendering.
func TestACardStageSendsItsLayoutAndFields(t *testing.T) {
	def := PipelineDef{Name: "verdict", Stages: []PipelineStage{{
		Name: "judge", Kind: StageAgent, Agent: "judge", Prompt: "Judge {input}",
		Output: []PipelineField{{Name: "verdict"}, {Name: "confidence"}},
		Render: "card", Card: map[string]string{"title": "verdict", "badges": "confidence"},
	}}}
	events := collectEvents(t, def, func(ctx context.Context, agent, prompt string) (string, error) {
		return `{"verdict":"Ship it","confidence":"high"}`, nil
	})
	var block, fields *PipelineEvent
	for i := range events {
		switch events[i].Kind {
		case "block":
			block = &events[i]
		case "fields":
			fields = &events[i]
		}
	}
	if block == nil || block.Type != "card" || block.Card["title"] != "verdict" {
		t.Fatalf("the block must name its renderer and carry its layout: %+v", block)
	}
	if fields == nil || fields.ID != block.ID || fields.Fields["verdict"] != "Ship it" || fields.Fields["confidence"] != "high" {
		t.Fatalf("the stage's fields must follow on the same block: %+v", fields)
	}
}

// A stage that does not ask for a card is drawn exactly as before.
func TestAStageWithoutRenderKeepsItsKind(t *testing.T) {
	def := PipelineDef{Name: "plain", Stages: []PipelineStage{{Name: "ask", Kind: StageAgent, Agent: "a", Prompt: "{input}"}}}
	for _, ev := range collectEvents(t, def, func(context.Context, string, string) (string, error) { return "an answer", nil }) {
		if ev.Kind == "block" && (ev.Type != string(StageAgent) || ev.Card != nil) {
			t.Errorf("a plain stage's block changed: %+v", ev)
		}
		if ev.Kind == "fields" {
			t.Errorf("a stage with no declared output has no fields to send: %+v", ev)
		}
	}
}

// A panel drawn as cards shows each voice in each round as its own card, the
// way a debate shows its arguments; the stage's own block is the heading.
func TestAPanelDrawnAsCardsHasOneCardPerVoicePerRound(t *testing.T) {
	def := PipelineDef{Name: "argue", Stages: []PipelineStage{{
		Name: "debate", Kind: StagePanel, Count: 2, Panel: []string{"For", "Against"},
		Prompt: "You are {voice}. {input}",
		Render: "card", Card: map[string]string{"accent": "voice", "body": "text"},
	}}}
	events := collectEvents(t, def, func(ctx context.Context, voice, prompt string) (string, error) {
		return voice + " argues its case", nil
	})
	var heading *PipelineEvent
	cards := map[string]map[string]any{}
	for i, ev := range events {
		if ev.Kind == "block" && ev.Title == "debate" {
			heading = &events[i]
		}
		if ev.Kind == "fields" && ev.Fields["voice"] != nil {
			cards[ev.ID] = ev.Fields
		}
	}
	if heading == nil || heading.Type != "text" {
		t.Fatalf("the panel's own block becomes a heading: %+v", heading)
	}
	for _, ev := range events {
		if ev.Kind == "chunk" && ev.ID == heading.ID {
			t.Errorf("the heading must not repeat what the cards show: %q", ev.Text)
		}
	}
	if len(cards) != 4 {
		t.Fatalf("two voices over two rounds is four cards, got %d", len(cards))
	}
	seen := map[string]bool{}
	for _, f := range cards {
		seen[fmt.Sprintf("%v/%v", f["voice"], f["round"])] = true
		if !strings.HasSuffix(f["text"].(string), "argues its case") {
			t.Errorf("a card carries what its voice said: %v", f)
		}
	}
	if len(seen) != 4 {
		t.Errorf("each voice once per round, got %v", seen)
	}
}

// A card that names a field the stage never produces would draw an empty space
// where its author expects a value; refused when the pipeline is saved.
func TestCardLayoutsAreCheckedOnSave(t *testing.T) {
	stage := func(s PipelineStage) PipelineDef {
		if s.Name == "" {
			s.Name = "s"
		}
		if s.Kind == "" {
			s.Kind = StageWorker
		}
		if s.Prompt == "" {
			s.Prompt = "x"
		}
		return PipelineDef{Name: "d", Stages: []PipelineStage{s}}
	}
	out := []PipelineField{{Name: "verdict"}, {Name: "why"}}
	for _, bad := range []PipelineDef{
		stage(PipelineStage{Output: out, Card: map[string]string{"title": "verdict"}}),                  // card without render=card
		stage(PipelineStage{Output: out, Render: "card", Card: map[string]string{"title": "missing"}}),  // unknown field
		stage(PipelineStage{Output: out, Render: "card", Card: map[string]string{"footer": "verdict"}}), // unknown part
		stage(PipelineStage{Output: out, Render: "card", Card: map[string]string{"title": "verdict, why"}}),
		stage(PipelineStage{Render: "Card <b>"}),
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("should be refused: %+v", bad.Stages[0])
		}
	}
	for _, good := range []PipelineDef{
		stage(PipelineStage{Output: out, Render: "card", Card: map[string]string{"title": "verdict", "badges": "why, verdict", "body": "why", "accent": "verdict"}}),
		stage(PipelineStage{Kind: StagePanel, Panel: []string{"a", "b"}, Count: 1, Render: "card", Card: map[string]string{"accent": "voice", "body": "text"}}),
		stage(PipelineStage{Render: "my_block"}),
	} {
		if err := good.Validate(); err != nil {
			t.Errorf("should be accepted: %v", err)
		}
	}
}

// A stored run keeps each block's card and fields, so opening it later draws
// the same card it streamed; live, the fields go out as block_meta.
func TestARunKeepsItsCards(t *testing.T) {
	s := runSurface(t, func(ctx context.Context, input string, _ map[string]string, sink PipelineSink) (string, error) {
		sink(PipelineEvent{Kind: "block", ID: "b1", Type: "card", Title: "judge", Card: map[string]string{"title": "verdict"}})
		sink(PipelineEvent{Kind: "fields", ID: "b1", Fields: map[string]any{"verdict": "Ship it"}})
		sink(PipelineEvent{Kind: "block_done", ID: "b1"})
		return "done", nil
	})
	id, rec := startRun(t, s)
	if !strings.Contains(rec.Body.String(), "event: block_meta") || !strings.Contains(rec.Body.String(), `"verdict":"Ship it"`) {
		t.Errorf("the fields must stream as block_meta:\n%s", rec.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if run, _ := LoadPipelineRun(s.DB, s.User, s.OwnerID, id); !run.Running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := httptest.NewRecorder()
	(&AppCore{}).ServeRuns(got, httptest.NewRequest(http.MethodGet, "/sessions/"+id, nil), s, "sessions/"+id)
	var one struct {
		Blocks []PipelineRunBlock
	}
	_ = json.Unmarshal(got.Body.Bytes(), &one)
	if len(one.Blocks) != 1 || one.Blocks[0].Card["title"] != "verdict" || one.Blocks[0].Fields["verdict"] != "Ship it" {
		t.Errorf("the stored block must keep its card and fields: %+v", one.Blocks)
	}
}
