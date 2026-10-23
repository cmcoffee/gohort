package core

import (
	"strings"
	"testing"
)

// argCall is an assistant message whose one tool call sent body as an
// argument, and the tool's short reply.
func argCall(id, body string) []Message {
	return []Message{
		{Role: "assistant", ToolCalls: []ToolCall{{ID: id, Name: "app_def", Args: map[string]any{"action": "update", "sections": []any{map[string]any{"html": body}}}}}},
		{Role: "user", ToolResults: []ToolResult{{ID: id, Content: "Updated."}}},
	}
}

// Every earlier call goes back to the model with its arguments whole, and on
// an authoring thread that is where the weight is: a page or a file sent as
// one argument. The estimate counted none of it, so a thread far past the
// provider's window read as fitting, and nothing could shrink it. It is
// counted now, and compaction cuts the bulk of older calls' arguments, the
// newest kept whole and the stored turn untouched.
func TestOldToolCallArgumentsAreCountedAndCut(t *testing.T) {
	page := strings.Repeat("<div>row</div>", 4000) // ~56k bytes, ~14k tokens
	var msgs []Message
	for i := 0; i < 6; i++ {
		msgs = append(msgs, argCall(string(rune('a'+i)), page)...)
	}
	before := EstimateMessagesTokens(msgs)
	if before < 6*14000 || before > 6*15000 {
		t.Fatalf("the arguments are counted, as sent (no \\u003c escapes): estimate %d", before)
	}
	if est := estimatePromptTokens(msgs, ""); est < 6*14000 {
		t.Fatalf("the prompt estimate counts them too: %d", est)
	}
	stored := msgs[0].ToolCalls[0].Args // the map the stored turn holds

	compactHistory(msgs, "", 1000, 120000, false)
	// The newest four calls are kept whole; the older two are cut.
	if after := EstimateMessagesTokens(msgs); after > before-2*13000 {
		t.Fatalf("compaction cut the older arguments: %d -> %d", before, after)
	}
	first, _ := msgs[0].ToolCalls[0].Args["sections"].([]any)
	if html, _ := first[0].(map[string]any)["html"].(string); !strings.Contains(html, "elided to fit context") {
		t.Errorf("an old call's page is replaced by a marker: %.80s", html)
	}
	if msgs[0].ToolCalls[0].Name != "app_def" || msgs[0].ToolCalls[0].Args["action"] != "update" {
		t.Error("the call keeps its name and short arguments")
	}
	last := msgs[len(msgs)-2].ToolCalls[0].Args["sections"].([]any)[0].(map[string]any)["html"].(string)
	if last != page {
		t.Error("the newest call is kept whole")
	}
	if s := stored["sections"].([]any)[0].(map[string]any)["html"].(string); s != page {
		t.Error("the stored turn's arguments were changed")
	}
}

// The recovery's last rung cuts old arguments before what was said.
func TestRecoveryCutsArgumentsBeforeText(t *testing.T) {
	page := strings.Repeat("x", 40000)
	msgs := []Message{{Role: "user", Content: strings.Repeat("words ", 200)}}
	for i := 0; i < 4; i++ {
		msgs = append(msgs, argCall(string(rune('a'+i)), page)...)
	}
	elideOldMessageText(msgs, 25000, 2)
	if !strings.Contains(msgs[1].ToolCalls[0].Args["sections"].([]any)[0].(map[string]any)["html"].(string), "elided") {
		t.Error("an old call's argument is cut")
	}
	if strings.Contains(msgs[0].Content, "elided") {
		t.Error("what was said is kept while cutting arguments is enough")
	}
}

// A call's arguments go to the model as written: an HTML page sent as an
// argument is not turned into \u003c escapes, which made it two to three
// times its size on every later round.
func TestToolArgumentsAreSentWithoutHTMLEscapes(t *testing.T) {
	got := toolArgsJSON(map[string]any{"html": "<div class=\"a\">x & y</div>"})
	if strings.Contains(got, "\\u003c") || !strings.Contains(got, "<div") || !strings.Contains(got, "x & y") {
		t.Errorf("got %s", got)
	}
	if toolArgsJSON(nil) != "null" && toolArgsJSON(nil) != "{}" {
		t.Errorf("no arguments: %q", toolArgsJSON(nil))
	}
}

// The newest messages can be the bulk: a few whole files read the round
// before. Every recovery rung spares them, so a retry re-sent most of what
// was refused (25 of 31 messages folded, still 770 KB). The last rung cuts
// the largest bodies anywhere, tool results first, the newest included,
// keeping each one's end with a note, until it fits; what was said is cut
// only when nothing else is left.
func TestRecoveryShrinksTheNewestWhenTheyAreTheBulk(t *testing.T) {
	file := strings.Repeat("line of a large file\n", 12000) // ~250 KB
	ask := "Please look at these and tell me what is wrong. " + strings.Repeat("context ", 1500)
	msgs := []Message{
		{Role: "user", Content: ask},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "a", Name: "read_file"}, {ID: "b", Name: "read_file"}, {ID: "c", Name: "read_file"}}},
		{Role: "user", ToolResults: []ToolResult{{ID: "a", Content: file}, {ID: "b", Content: file}, {ID: "c", Content: file}}},
	}
	budget := 40000
	if EstimateMessagesTokens(msgs) <= budget {
		t.Fatal("the fixture must start over budget")
	}
	if n := shrinkLargestBodies(msgs, budget); n <= 0 {
		t.Fatal("nothing was cut")
	}
	if got := EstimateMessagesTokens(msgs); got > budget {
		t.Fatalf("still over: %d > %d", got, budget)
	}
	for _, tr := range msgs[2].ToolResults {
		if !strings.Contains(tr.Content, "cut to fit context") || !strings.HasSuffix(tr.Content, "line of a large file\n") {
			t.Errorf("each result keeps its end with a note: %.120s", tr.Content)
		}
	}
	if msgs[0].Content != ask {
		t.Error("what was said is kept while cutting results is enough")
	}

	// Nothing left above the floor: it stops rather than loop.
	small := []Message{{Role: "user", Content: "hi"}}
	if n := shrinkLargestBodies(small, 0); n != 0 {
		t.Errorf("nothing to cut, cut %d", n)
	}
}
