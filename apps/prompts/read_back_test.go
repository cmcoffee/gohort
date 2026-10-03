package prompts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/prompts"
)

// Each model reads the wording it would be sent: the open text when it is
// that model's version, or the shared one it reads for want of its own; its
// own saved wording otherwise.
func TestEachModelReadsBackTheWordingItWouldBeSent(t *testing.T) {
	_, b := variantWorld(t)
	SetPromptOverride(b.Key, "everyone reads this")

	if text, how := readBackText(b, "worker", "all", "open draft"); text != "open draft" || how != "the open text" {
		t.Fatalf("shared open, worker: %q / %q", text, how)
	}
	prompts.SetPromptTierOverrideBy("lead", b.Key, "the lead's own words", "cloud/flash", "edit")
	if text, how := readBackText(b, "lead", "all", "open draft"); text != "the lead's own words" || how != "its own saved wording" {
		t.Fatalf("shared open, lead with its own: %q / %q", text, how)
	}
	if text, how := readBackText(b, "lead", "lead", "open draft"); text != "open draft" || how != "the open text" {
		t.Fatalf("lead open, lead: %q / %q", text, how)
	}
	if text, how := readBackText(b, "worker", "lead", "open draft"); text != "everyone reads this" || how != "the shared saved wording" {
		t.Fatalf("lead open, worker: %q / %q", text, how)
	}
}

// A tool's description is asked about as a tool, a parameter's as a
// parameter, and a prompt block as instructions.
func TestReadBackAsksAboutWhatTheBlockIs(t *testing.T) {
	tool := readBackAsk(PromptBlock{Key: prompts.ToolBlockKey("machine")}, "builds a machine")
	if !strings.Contains(tool, "named machine") || !strings.Contains(tool, "reach for it first") {
		t.Fatalf("tool ask: %q", tool)
	}
	param := readBackAsk(PromptBlock{Key: prompts.ToolParamBlockKey("machine", "unattended")}, "runs with no one there")
	if !strings.Contains(param, "machine, takes a parameter named unattended") || !strings.Contains(param, "put in unattended") {
		t.Fatalf("param ask: %q", param)
	}
	block := readBackAsk(PromptBlock{Key: "some.block"}, "be terse")
	if !strings.Contains(block, "Your instructions include") || !strings.Contains(block, "be terse") {
		t.Fatalf("block ask: %q", block)
	}
	for _, s := range []string{readBackSystem, tool, param, block} {
		if strings.Contains(s, "—") {
			t.Fatalf("an em-dash in a prompt: %q", s)
		}
	}
}

func readBack(t *testing.T, app *PromptsApp, body map[string]string) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	app.handleReadBack(rec, asAdmin(httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(string(raw)))))
	var out map[string]any
	json.NewDecoder(rec.Body).Decode(&out)
	return out
}

// With no separate lead the lead's column says so instead of asking the
// worker twice, and a tier or block that is not one is refused.
func TestReadBackWithoutASeparateLead(t *testing.T) {
	app, b := variantWorld(t)
	if LeadIsDistinct() {
		t.Skip("a lead model is configured in this process")
	}
	out := readBack(t, app, map[string]string{"id": b.Key, "body": "x", "variant": "all", "tier": "lead"})
	if s, _ := out["skipped"].(string); !strings.Contains(s, "no separate lead") {
		t.Fatalf("lead without one: %v", out)
	}
	if out := readBack(t, app, map[string]string{"id": b.Key, "body": "x", "tier": "judge"}); out["error"] == nil {
		t.Fatalf("a tier that is not one: %v", out)
	}
	if out := readBack(t, app, map[string]string{"id": "no.such.block", "body": "x", "tier": "worker"}); out["error"] == nil {
		t.Fatalf("a block that is not one: %v", out)
	}
}
