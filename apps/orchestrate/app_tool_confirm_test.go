package orchestrate

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// appToolConfirmation is what decides whether a host-app tool stops for the
// user, so it has to be exact about which names it claims.
func TestAppToolConfirmationMatchesOnlyDeclaringTools(t *testing.T) {
	turn := &chatTurn{appTools: []AgentToolDef{
		{Tool: Tool{Name: "run"}, Confirmation: &ToolConfirmation{Prompt: "Run a command in demo?"}},
		{Tool: Tool{Name: "read_file"}},
		{Tool: Tool{Name: "padded"}, Confirmation: &ToolConfirmation{Prompt: "   "}},
	}}
	if got := turn.appToolConfirmation("run"); !got.Asks() || got.Prompt != "Run a command in demo?" {
		t.Fatalf("declaring tool got %+v", got)
	}
	if turn.appToolConfirmation("read_file").Asks() {
		t.Fatal("a tool that did not opt in is gated")
	}
	if turn.appToolConfirmation("nonesuch").Asks() {
		t.Fatal("an unknown name is gated")
	}
	// A whitespace-only prompt is not a question; treating it as one would
	// park the loop behind an empty card.
	if turn.appToolConfirmation("padded").Asks() {
		t.Fatal("a whitespace-only prompt was honored as a question")
	}
}

// A nil turn must answer "no prompt" rather than panicking: confirmFuncFor is
// handed to worker loops too, and a nil receiver there would take down a run.
func TestAppToolConfirmationNilTurn(t *testing.T) {
	var turn *chatTurn
	if turn.appToolConfirmation("run").Asks() {
		t.Fatal("a nil turn reported a gated tool")
	}
}

// The whole point of the escalation is that nobody watching means nobody
// approving. A run with no SSE writer must DENY, not fall through to allow.
func TestEscalationFailsClosedWithoutAViewer(t *testing.T) {
	turn := &chatTurn{user: "u"} // sse is nil — a headless dispatch
	allowed := turn.escalateToolConfirm(toolConfirmRequest{
		tool:    "run",
		prompt:  "Run a command in demo?",
		detail:  "command: rm -rf build",
		because: "the run tool requires approval for each call",
	})
	if allowed {
		t.Fatal("an unattended run was auto-approved")
	}
}

// confirmFuncFor is the policy in one place: a declaring app tool escalates,
// and anything else keeps the pre-existing always-allow behavior. Checked
// through the real hook rather than the helper, because it is the ORDER of
// those two branches that decides which sentence a user reads.
func TestConfirmFuncEscalatesAppToolsAndAllowsTheRest(t *testing.T) {
	turn := &chatTurn{user: "u", appTools: []AgentToolDef{
		{Tool: Tool{Name: "delete_file"}, Confirmation: &ToolConfirmation{Prompt: "Delete a file from demo?"}},
		{Tool: Tool{Name: "read_file"}},
	}}
	confirm := turn.confirmFuncFor(nil)

	// The declaring tool reaches the escalation, which (no viewer) denies.
	// A "true" here would mean the branch was never taken.
	if confirm("delete_file", "path: x.go") {
		t.Fatal("a tool with a ConfirmPrompt was allowed without asking")
	}
	// A non-declaring, non-credential tool is untouched by this change.
	if !confirm("read_file", "path: x.go") {
		t.Fatal("an ordinary tool started prompting")
	}
	if !confirm("some_unrelated_tool", "") {
		t.Fatal("a tool outside the app set started prompting")
	}
}

// The card the user sees must carry the tool's own sentence, not a generated
// one — that is the entire reason the field holds prose.
func TestEscalationCardCarriesTheDeclaredPrompt(t *testing.T) {
	buf := &syncBuf{}
	turn := &chatTurn{user: "u", sse: &sseWriter{live: buf}}

	// Answer the escalation as soon as it parks, so the test does not sit on
	// the five-minute timeout. Deny, because what is asserted is the CARD.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			answered := false
			toolConfirms.Range(func(k, v any) bool {
				p := v.(*pendingToolConfirm)
				if p.user == "u" {
					toolConfirms.Delete(k)
					p.ch <- false
					answered = true
					return false
				}
				return true
			})
			if answered {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	if turn.escalateToolConfirm(toolConfirmRequest{
		tool: "run", prompt: "Run a command in demo?", detail: "command: go build ./...",
		because: "the run tool requires approval for each call",
	}) {
		t.Fatal("a denied call reported as allowed")
	}
	<-done

	out := buf.String()
	if !strings.Contains(out, "Run a command in demo?") {
		t.Fatalf("the card did not carry the tool's declared sentence:\n%s", out)
	}
	if !strings.Contains(out, "go build ./...") {
		t.Fatalf("the card did not show what would run:\n%s", out)
	}
	if !strings.Contains(out, "\"kind\":\"confirm\"") {
		t.Fatalf("no confirm frame was emitted:\n%s", out)
	}
}

// syncBuf is a writer the escalation can stream into from its own goroutine
// while the test reads it from another.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
