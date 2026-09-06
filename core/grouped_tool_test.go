package core

import (
	"strings"
	"testing"
)

func runHintTool() *GroupedTool {
	g := NewGroupedTool("tool_def", "manage tools")
	g.AddAction("list", &GroupedToolAction{
		Description: "list tools",
		Handler:     func(map[string]any, *ToolSession) (string, error) { return "ok", nil },
	})
	g.AddAction("get", &GroupedToolAction{
		Description: "read one tool",
		Params:      map[string]ToolParam{"name": {Type: "string", Description: "tool name"}},
		Required:    []string{"name"},
		Handler:     func(map[string]any, *ToolSession) (string, error) { return "ok", nil },
	})
	return g
}

// action="help" ignores every other param. A model that writes
// help(name="get_top_stories") is asking about ONE tool and gets the generic
// authoring spec — a SUCCESS answering a different question, so the miss is
// invisible. Observed live: an agent asked twice, got the manual twice,
// concluded a published tool needed building, and ran the verifier three times
// (a real dispatch, three live fetches) before it thought to just call it.
func TestHelpWithParamsFlagsTheMissAndRoutesIt(t *testing.T) {
	g := runHintTool()
	out, err := g.Run(map[string]any{"action": "help", "name": "get_top_stories"})
	if err != nil {
		t.Fatalf("help should still return the spec: %v", err)
	}
	if !strings.Contains(out, "NOTE:") || !strings.Contains(out, "name") {
		t.Errorf("the ignored param should be named up front, got:\n%s", out)
	}
	// Route the caller to the action that actually takes "name".
	if !strings.Contains(out, `action="get"`) {
		t.Errorf("help should point at the action declaring the ignored param, got:\n%s", out)
	}
	// The banner heads the output — a model skims the top of a long dump.
	if idx := strings.Index(out, "NOTE:"); idx != 0 {
		t.Errorf("banner must lead the output, found at offset %d", idx)
	}
	// The spec itself is still there; the caller did ask for help.
	if !strings.Contains(out, "tool_def — usage:") {
		t.Errorf("the usage spec should still follow the banner, got:\n%s", out)
	}
}

// A bare help call is a legitimate request for the manual and must stay clean —
// no banner, no scolding.
func TestBareHelpHasNoBanner(t *testing.T) {
	g := runHintTool()
	out, err := g.Run(map[string]any{"action": "help"})
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	if strings.Contains(out, "NOTE:") {
		t.Errorf("a bare help call should return the spec unadorned, got:\n%s", out)
	}
}

// An agent that inspected a custom tool through tool_def (list → get → test)
// then reached for action="run" got back only the list of valid actions, and
// spent the rest of the turn improvising: inventing routes, then trying to run
// the tool's underlying script by hand with guessed workspace paths. The one
// fact that would have unstuck it — call the tool directly by name, which the
// loop's lazy fallback resolves whether or not it is in the visible catalog —
// was never stated.
func TestUnknownRunActionPointsAtCallingDirectly(t *testing.T) {
	g := runHintTool()
	for _, action := range []string{"run", "execute", "invoke", "call", "use"} {
		_, err := g.Run(map[string]any{"action": action})
		if err == nil {
			t.Fatalf("action %q should be rejected", action)
		}
		if !strings.Contains(err.Error(), "DIRECTLY by its own name") {
			t.Errorf("action %q should point at calling the tool directly, got: %v", action, err)
		}
	}
}

// A plain typo is not a request to execute something — it gets the ordinary
// error without the extra sentence, so the hint keeps its meaning.
func TestUnknownNonRunActionStaysTerse(t *testing.T) {
	g := runHintTool()
	_, err := g.Run(map[string]any{"action": "lsit"})
	if err == nil {
		t.Fatal("a typo action should be rejected")
	}
	if strings.Contains(err.Error(), "DIRECTLY by its own name") {
		t.Errorf("a typo should not get the run hint, got: %v", err)
	}
	if !strings.Contains(err.Error(), "list") {
		t.Errorf("the error should still name the valid actions, got: %v", err)
	}
}

// A bare call is the shape a dropped-argument call arrives in. Answering it
// with the usage spec returns a long, successful-looking result that contains
// no data — the model cannot tell it from an answer, and re-calls. One observed
// turn burned 79 seconds that way across three tools.
func TestBareGroupedCallIsAnErrorNotHelp(t *testing.T) {
	g := runHintTool()
	out, err := g.Run(map[string]any{})
	if err == nil {
		t.Fatalf("a bare call must ERROR, not answer with help — got %d chars of result", len(out))
	}
	msg := err.Error()
	// Actionable on its own: name the actions, say plainly nothing happened.
	for _, want := range []string{"nothing was done", "list", "get", "help"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q: %s", want, msg)
		}
	}
	// And name the dropped-argument case, which is the actual cause when a
	// model that DID construct arguments lands here.
	if !strings.Contains(msg, "did not arrive") {
		t.Errorf("error should raise the dropped-argument possibility: %s", msg)
	}
}

// Explicit probing still works — discovery was never the problem.
func TestExplicitHelpStillReturnsTheSpec(t *testing.T) {
	g := runHintTool()
	out, err := g.Run(map[string]any{"action": "help"})
	if err != nil {
		t.Fatalf("action=help must still work: %v", err)
	}
	if !strings.Contains(out, "list") {
		t.Errorf("help should list the actions: %s", out)
	}
}

// newTestPostTool builds a grouped tool mirroring moltbook's "post" action
// so we exercise the exact shape that sent the Builder agent into a
// re-send loop (submolt_name typed as submolta_name).
func newTestPostTool() *GroupedTool {
	gt := NewGroupedTool("moltbook", "test")
	gt.AddAction("post", &GroupedToolAction{
		Description: "Create a new post.",
		Params: map[string]ToolParam{
			"submolt_name": {Type: "string", Description: "target submolt"},
			"title":        {Type: "string", Description: "post title"},
			"content":      {Type: "string", Description: "post body"},
			"client_id":    {Type: "string", Description: "idempotency key"},
			"type":         {Type: "string", Description: "post type"},
		},
		Required: []string{"submolt_name", "title", "client_id"},
		Handler: func(args map[string]any, sess *ToolSession) (string, error) {
			return "ok", nil
		},
	})
	return gt
}

func TestGroupedTool_TypoOnRequiredParamIsNamed(t *testing.T) {
	gt := newTestPostTool()
	_, err := gt.Run(map[string]any{
		"action":        "post",
		"submolta_name": "general", // the typo that caused the loop
		"title":         "hi",
		"client_id":     "abc",
	})
	if err == nil {
		t.Fatal("expected error for missing required submolt_name")
	}
	msg := err.Error()
	if !strings.Contains(msg, `you supplied "submolta_name"`) {
		t.Errorf("error should name the wrong key; got: %s", msg)
	}
	if !strings.Contains(msg, `did you mean "submolt_name"`) {
		t.Errorf("error should suggest the intended param; got: %s", msg)
	}
}

func TestGroupedTool_PlainMissingHasNoTypoHint(t *testing.T) {
	gt := newTestPostTool()
	// submolt_name simply omitted (no near-miss key supplied) — the
	// message must NOT invent a "did you mean".
	_, err := gt.Run(map[string]any{
		"action":    "post",
		"title":     "hi",
		"client_id": "abc",
	})
	if err == nil {
		t.Fatal("expected error for missing required submolt_name")
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Errorf("no near-miss key was supplied; should not suggest one; got: %s", err.Error())
	}
}

func TestGroupedTool_UnrelatedExtraKeyNotFlagged(t *testing.T) {
	// A genuinely unrelated stray key (not a typo of any param) shouldn't
	// produce a spurious suggestion — only the real missing-param message.
	def := &GroupedToolAction{
		Params: map[string]ToolParam{
			"submolt_name": {Type: "string"},
			"title":        {Type: "string"},
		},
	}
	if got := def.nearestParamName("xyzzy"); got != "" {
		t.Errorf("unrelated key matched %q; want no suggestion", got)
	}
	// Transposition-style typo should still resolve.
	if got := def.nearestParamName("titel"); got != "title" {
		t.Errorf("titel should suggest title; got %q", got)
	}
}

// TestGroupedTool_AllMissingParamsReportedAtOnce pins the batch validation:
// every missing required param lands in ONE error, with the typo hint, so
// the model converges in a single retry instead of discovering its mistakes
// serially (observed live: a reply_to_comment missing post_id was "fixed"
// into one missing content, and the model abandoned the action after two
// rounds of one-at-a-time errors).
func TestGroupedTool_AllMissingParamsReportedAtOnce(t *testing.T) {
	gt := newTestPostTool()
	_, err := gt.Run(map[string]any{
		"action":        "post",
		"submolta_name": "general", // typo — the real param is missing
		"title":         "",        // present but empty
	})
	if err == nil {
		t.Fatal("expected error for missing/empty required params")
	}
	msg := err.Error()
	for _, want := range []string{`"submolt_name"`, `"client_id"`, `non-empty "title"`, `"submolta_name"`, "did you mean"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("batch error should mention %s; got: %s", want, msg)
		}
	}
	// And the fixed call goes straight through.
	if out, err := gt.Run(map[string]any{
		"action": "post", "submolt_name": "general", "title": "hi", "client_id": "abc",
	}); err != nil || out != "ok" {
		t.Fatalf("complete call should succeed; out=%q err=%v", out, err)
	}
}

// TestSerialFireWiring guards the serial-fire plumbing the agent loop relies
// on: a GroupedTool marked serial-fire must report SerialFirePerBatch (and NOT
// SingleFirePerBatch), and ChatToolToAgentToolDef must propagate that onto the
// AgentToolDef. rebuildToolMaps keys off exactly these two flags — a serial
// tool that leaked into the single-fire set would have its excess batched
// calls dropped instead of run in order, reintroducing the tool_def
// delete-then-create footgun this replaced.
func TestSerialFireWiring(t *testing.T) {
	serial := NewGroupedTool("t_serial", "serial authoring tool")
	serial.AddAction("noop", &GroupedToolAction{
		Description: "noop",
		Handler:     func(args map[string]any, sess *ToolSession) (string, error) { return "ok", nil },
	})
	serial.SetSerialFirePerBatch(true)

	if !serial.SerialFirePerBatch() {
		t.Fatal("SerialFirePerBatch() should be true after SetSerialFirePerBatch(true)")
	}
	if serial.SingleFirePerBatch() {
		t.Fatal("a serial-fire tool must NOT also report single-fire")
	}

	def := ChatToolToAgentToolDefWithSession(serial, nil)
	if !def.SerialFirePerBatch {
		t.Error("AgentToolDef.SerialFirePerBatch not propagated from the tool")
	}
	if def.SingleFirePerBatch {
		t.Error("AgentToolDef.SingleFirePerBatch should be false for a serial-fire tool")
	}

	// A plain single-fire tool must still register as single-fire only.
	single := NewGroupedTool("t_single", "single-fire tool")
	single.AddAction("noop", &GroupedToolAction{
		Description: "noop",
		Handler:     func(args map[string]any, sess *ToolSession) (string, error) { return "ok", nil },
	})
	single.SetSingleFirePerBatch(true)
	sdef := ChatToolToAgentToolDefWithSession(single, nil)
	if !sdef.SingleFirePerBatch || sdef.SerialFirePerBatch {
		t.Errorf("single-fire tool mis-wired: single=%v serial=%v", sdef.SingleFirePerBatch, sdef.SerialFirePerBatch)
	}
}
