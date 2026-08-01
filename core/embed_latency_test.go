package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// 45 seconds of a 50-second turn were spent here, and the log showed a silent
// gap between two unrelated lines — the single largest latency contributor in
// the system had no telemetry at all. Finding it meant reading the source to
// work out what could even live in that window.
func TestEmbedIsTimedAndWarnsWhenSlow(t *testing.T) {
	src := mustCoreFile(t, "embeddings.go")
	call := src[strings.Index(src, "client := &http.Client{Timeout:"):]
	if !strings.Contains(call[:900], "time.Since(started)") {
		t.Fatal("the embed call is untimed again; a blocking call on the critical path must report its duration")
	}
	if !strings.Contains(call[:900], "SLOW") {
		t.Error("a slow embed must be visible WITHOUT debug logging — that is the case an operator needs")
	}
	if !strings.Contains(call[:900], "shares a server with the worker model") {
		t.Error("the warning should name the likeliest cause; a bare duration sends the reader back to guessing")
	}
	// Well under the hint budget, so a hint about to be abandoned still leaves
	// a trace saying why.
	if embedSlowWarn >= RecallHintTimeout() {
		t.Errorf("the slow-warn threshold (%s) must sit below the recall-hint budget (%s), or the abandoned case logs nothing",
			embedSlowWarn, RecallHintTimeout())
	}
}

// The caller's deadline has to win over the 60s ceiling, or bounding the hint
// phase accomplishes nothing.
func TestEmbedHonorsTheCallersDeadline(t *testing.T) {
	// Only has to outlast the caller's budget — the assertion is that the CLIENT
	// gives up, so a long sleep would just tax every run of the suite (Close
	// waits on the handler).
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
	}))
	defer slow.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := EmbedWith(ctx, EmbeddingConfig{Enabled: true, Endpoint: slow.URL}, "some text")
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("a caller deadline that expires must fail the embed, not wait out the client ceiling")
	}
	if elapsed > time.Second {
		t.Errorf("the caller's 150ms budget was ignored — took %s; the 60s ceiling must not override it", elapsed)
	}
}

// Recall hints are optional, so their budget is a latency cap. Borrowing the
// ingest timeout is what made one message cost a minute.
func TestRecallHintBudgetIsShortAndItsOwn(t *testing.T) {
	if got := RecallHintTimeout(); got > 10*time.Second {
		t.Errorf("recall hints are an optional nudge; a %s budget is a latency bug waiting to happen", got)
	}
	hints := mustAppFile(t, "../apps/orchestrate/recall_hints.go")
	if strings.Contains(hints, "knowledgeIngestTimeout()") {
		t.Error("the hint phase is back on the INGEST timeout — sized for bulk work, not for a turn the user is waiting on")
	}
	if !strings.Contains(hints, "RecallHintTimeout()") {
		t.Error("the hint phase must use its own budget")
	}
}

func mustCoreFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func mustAppFile(t *testing.T, path string) string { return mustCoreFile(t, path) }

// Instrumenting only the embed proved the embed innocent (44-202ms) and left
// the rest of the 45-second window exactly as dark as before. Every step of the
// phase reports its own cost now, so the next slow turn names its own culprit
// instead of requiring another read of the source.
func TestRecallHintPhaseTimesEveryStep(t *testing.T) {
	src := mustAppFile(t, "../apps/orchestrate/recall_hints.go")
	for _, step := range []string{"embed=", "knowledge=", "memory=", "graph=", "total="} {
		if !strings.Contains(src, step) {
			t.Errorf("the phase timing line is missing %q — a step with no number cannot be the one you rule out", step)
		}
	}
	// Hit counts alongside the durations: a slow scan reads differently
	// depending on whether it returned 3 rows or 30,000.
	if !strings.Contains(src, "len(kn)") || !strings.Contains(src, "len(mem)") {
		t.Error("log the hit counts too — duration alone cannot distinguish a big corpus from a slow store")
	}
	if !strings.Contains(src, "SLOW agent=") {
		t.Error("over-budget turns must warn without DEBUG on")
	}
	if !strings.Contains(src, "RecallHintTimeout()") {
		t.Error("the SLOW threshold must be the budget itself, so the two cannot drift apart")
	}
}
