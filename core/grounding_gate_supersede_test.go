package core

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// seqLLM returns a scripted response per call: an unsourced figure first, then
// a clean correction — the exact shape the grounding gate re-loops on.
type seqLLM struct {
	mu      sync.Mutex
	replies []string
	n       int
}

func (s *seqLLM) next() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.replies[s.n]
	if s.n < len(s.replies)-1 {
		s.n++
	}
	return r
}

func (s *seqLLM) Chat(ctx context.Context, msgs []Message, opts ...ChatOption) (*Response, error) {
	return &Response{Content: s.next()}, nil
}

func (s *seqLLM) ChatStream(ctx context.Context, msgs []Message, h StreamHandler, opts ...ChatOption) (*Response, error) {
	return s.Chat(ctx, msgs, opts...)
}

// TestGroundingGateEmitsSupersede: when the gate rejects an unsourced figure,
// it emits a Superseded step (so a streaming surface can wipe the bubble) and
// the loop returns the corrected, figure-free answer.
func TestGroundingGateEmitsSupersede(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(WebTable, tuneGroundingGate, float64(1)) // enable the dark-by-default gate
	SetTunablesDB(db)
	defer SetTunablesDB(nil)

	llm := &seqLLM{replies: []string{
		"Those three cover 80% of what you need.",               // unsourced percent -> gate fires
		"Those three cover the vast majority of what you need.", // corrected
	}}
	app := &AppCore{LLM: llm}

	var superseded []string
	var finalStep string
	cfg := AgentLoopConfig{
		MaxRounds: 5,
		Tools: []AgentToolDef{{
			Tool:    Tool{Name: "noop", Description: "placeholder so len(tools) > 0", Caps: []Capability{CapRead}},
			Handler: func(map[string]any) (string, error) { return "", nil },
		}},
		OnStep: func(s StepInfo) {
			if s.Superseded {
				superseded = append(superseded, s.Content)
			} else if s.Done {
				finalStep = s.Content
			}
		},
	}

	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "which SSH tools matter?"}}, cfg)
	if err != nil {
		t.Fatalf("loop error: %v", err)
	}

	// The gate emitted one supersede carrying the rejected figure.
	if len(superseded) != 1 {
		t.Fatalf("expected 1 superseded step, got %d: %v", len(superseded), superseded)
	}
	if !strings.Contains(superseded[0], "80%") {
		t.Errorf("superseded step should carry the rejected answer, got %q", superseded[0])
	}
	// The returned answer is the corrected one, with no figure.
	if strings.Contains(resp.Content, "80%") {
		t.Errorf("final answer still contains the unsourced figure: %q", resp.Content)
	}
	if !strings.Contains(finalStep, "vast majority") {
		t.Errorf("final Done step should be the corrected answer, got %q", finalStep)
	}
}
