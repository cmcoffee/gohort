package orchestrate

// consult_<agent> runs another agent on behalf of a drafting surface. It
// bounds that run with a timeout, and used to get the bound by rooting on
// context.Background() — which also discarded everything else the caller's
// context carried.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Cancelling the caller must reach the consultation. Rooted on Background it
// did not, and a stopped drafting turn left an agent running with nobody
// waiting on it.
func TestConsultDiesWithTheCallingTurn(t *testing.T) {
	// A real app with a real agent: ItemTools returns nothing without one, and
	// a skipped test proves nothing about the case it names.
	db := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(db, "alice")
	saved, err := saveAgent(udb, AgentRecord{
		Name: "Schema Keeper", Owner: "alice", Description: "knows the schema",
		OrchestratorPrompt: "Answer questions about the schema.",
	})
	if err != nil {
		t.Fatal(err)
	}
	// An LLM that blocks until its CONTEXT ends is what makes this test
	// discriminate. With a stub that returns immediately the handler finishes
	// fast either way, and the test passes just as happily against the bug it
	// is named for.
	s := agentReferenceSource{app: &OrchestrateApp{AppCore: AppCore{DB: db, LLM: blockUntilCancelled{}}}}
	defs := s.ItemTools("alice", saved.ID)
	if len(defs) != 1 {
		t.Fatalf("want one consult tool, got %d", len(defs))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already dead before the call

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = defs[0].Handler(ctx, map[string]any{"question": "anything"})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled caller left the consultation running — the handler is not " +
			"deriving from the caller's context, so nothing upstream can stop it")
	}
}

// blockUntilCancelled answers only when its context ends, so a consultation
// that ignored the caller's context runs for agentConsultTimeout instead.
type blockUntilCancelled struct{}

func (blockUntilCancelled) Chat(ctx context.Context, _ []Message, _ ...ChatOption) (*Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (b blockUntilCancelled) ChatStream(ctx context.Context, m []Message, _ StreamHandler, o ...ChatOption) (*Response, error) {
	return b.Chat(ctx, m, o...)
}

// The source-level property, because the posture half cannot be observed
// without a live network connector and a running agent: the handler must
// DERIVE its deadline from the caller, not replace the caller.
func TestConsultDerivesItsDeadlineFromTheCaller(t *testing.T) {
	src, err := os.ReadFile("agent_reference_source.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if strings.Contains(body, "context.WithTimeout(context.Background(), agentConsultTimeout)") {
		t.Error("the consult handler roots on Background — that discards the caller's network " +
			"connector (a Private-mode restriction stops at the tool boundary) and its cancellation")
	}
	if !strings.Contains(body, "context.WithTimeout(ctx, agentConsultTimeout)") {
		t.Error("the consult handler should bound the CALLER's context, keeping the same deadline")
	}
}
