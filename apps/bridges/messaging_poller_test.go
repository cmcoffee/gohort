package bridges

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A representative Microsoft Graph channel messages/delta response: a deltaLink
// (a top-level key that itself contains a dot), one real message, and one with an
// empty body that must be dropped.
const graphDeltaFixture = `{
  "@odata.deltaLink": "https://graph.microsoft.com/v1.0/teams/T/channels/C/messages/delta?$deltatoken=XYZ",
  "value": [
    {
      "id": "1700000000001",
      "createdDateTime": "2026-07-20T18:00:00Z",
      "channelIdentity": {"teamId": "T", "channelId": "19:abc@thread.tacv2"},
      "from": {"user": {"id": "u-1", "displayName": "Ada Lovelace"}},
      "body": {"contentType": "html", "content": "<p>Hello <at>bot</at>, status?</p>"}
    },
    {
      "id": "1700000000002",
      "channelIdentity": {"channelId": "19:abc@thread.tacv2"},
      "from": {"user": {"id": "u-2", "displayName": "Bob"}},
      "body": {"contentType": "html", "content": ""}
    }
  ]
}`

func TestTeamsPresetMappingAndCursor(t *testing.T) {
	spec, err := ApplyRestMessagingPreset("teams", RestMessagingSpec{Credential: "ms_graph"},
		map[string]string{"team_id": "T", "channel_id": "C"})
	if err != nil {
		t.Fatalf("ApplyRestMessagingPreset: %v", err)
	}

	// Vars substituted into the preset URLs.
	wantPoll := "https://graph.microsoft.com/v1.0/teams/T/channels/C/messages/delta"
	if spec.PollURL != wantPoll {
		t.Errorf("PollURL = %q, want %q", spec.PollURL, wantPoll)
	}
	if spec.SendURL != "https://graph.microsoft.com/v1.0/teams/T/channels/C/messages" {
		t.Errorf("SendURL not substituted: %q", spec.SendURL)
	}
	if !spec.StripHTML || spec.Map.ChatID == "" || spec.NextURLPath != "@odata.deltaLink" {
		t.Fatalf("preset did not populate spec: %+v", spec)
	}

	var root any
	if err := json.Unmarshal([]byte(graphDeltaFixture), &root); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	// @odata.deltaLink is a literal top-level key containing a dot — the cursor
	// path must resolve it whole, not split on the dot.
	next := jsonPathString(root, spec.NextURLPath)
	if next == "" || next[:5] != "https" {
		t.Errorf("deltaLink cursor path resolved to %q", next)
	}

	// Nested dot-path.
	if got := jsonPathString(root, "value"); got != "" {
		t.Errorf("array path should not stringify: %q", got)
	}

	msgs := messagesFromResponse(root, spec)
	if len(msgs) != 1 {
		t.Fatalf("mapped %d messages, want 1 (empty-body message must drop)", len(msgs))
	}
	m := msgs[0]
	if m.ChatID != "19:abc@thread.tacv2" {
		t.Errorf("chat_id = %q", m.ChatID)
	}
	if m.Handle != "u-1" || m.DisplayName != "Ada Lovelace" {
		t.Errorf("sender = %q / %q", m.Handle, m.DisplayName)
	}
	// HTML stripped + entities unescaped.
	if m.Text != "Hello bot, status?" {
		t.Errorf("text = %q, want %q", m.Text, "Hello bot, status?")
	}
}

// Slack conversations.history: newest-first, no channel on each message (chat_id
// is fixed), cursor = the newest message's ts.
const slackHistoryFixture = `{
  "ok": true,
  "messages": [
    {"type": "message", "user": "U777", "text": "deploy done?", "ts": "1700000002.000200"},
    {"type": "message", "user": "U888", "text": "older", "ts": "1700000001.000100"}
  ],
  "has_more": false
}`

func TestSlackPresetMappingAndCursor(t *testing.T) {
	spec, err := ApplyRestMessagingPreset("slack", RestMessagingSpec{Credential: "slack_bot"},
		map[string]string{"channel_id": "C123"})
	if err != nil {
		t.Fatalf("ApplyRestMessagingPreset: %v", err)
	}
	if spec.PollURL != "https://slack.com/api/conversations.history?channel=C123" {
		t.Errorf("PollURL = %q", spec.PollURL)
	}
	if spec.ChatIDConst != "C123" {
		t.Errorf("ChatIDConst = %q, want C123 (chat_id_const var substitution)", spec.ChatIDConst)
	}
	if err := ValidateConnector(Connector{Kind: RestMessagingConnectorKind, Owner: "u", Spec: mustJSON(t, spec)}); err != nil {
		// Validate must accept chat_id_const in place of map.chat_id. The credential
		// isn't registered in a unit test, so a "credential" error is expected and
		// fine; anything else is a real shape rejection.
		if !isCredError(err) {
			t.Errorf("Validate rejected slack spec on shape: %v", err)
		}
	}

	var root any
	if err := json.Unmarshal([]byte(slackHistoryFixture), &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Cursor is the newest message's ts via array index.
	if got := jsonPathString(root, spec.CursorPath); got != "1700000002.000200" {
		t.Errorf("cursor path %q = %q", spec.CursorPath, got)
	}

	msgs := messagesFromResponse(root, spec)
	if len(msgs) != 2 {
		t.Fatalf("mapped %d, want 2", len(msgs))
	}
	for _, m := range msgs {
		if m.ChatID != "C123" {
			t.Errorf("chat_id = %q, want fixed C123", m.ChatID)
		}
	}
	if msgs[0].Handle != "U777" || msgs[0].Text != "deploy done?" || msgs[0].MsgID != "1700000002.000200" {
		t.Errorf("first msg mismapped: %+v", msgs[0])
	}
}

// Update = partial patch: MergeRestMessagingSpec keeps every existing field the
// overlay leaves empty, and only the provided fields change.
func TestMergeRestMessagingSpecPartialPatch(t *testing.T) {
	existing, _ := ApplyRestMessagingPreset("teams", RestMessagingSpec{Credential: "graph"},
		map[string]string{"team_id": "T", "channel_id": "C"})

	over := RestMessagingSpec{IntervalSecs: 60} // change ONLY the interval
	over.Map.Text = "text2"                     // and one map field

	merged := MergeRestMessagingSpec(existing, over)

	if merged.IntervalSecs != 60 {
		t.Errorf("interval not patched: %d", merged.IntervalSecs)
	}
	if merged.Map.Text != "text2" {
		t.Errorf("map.text not patched: %q", merged.Map.Text)
	}
	// Untouched fields survive.
	if merged.PollURL != existing.PollURL || merged.NextURLPath != existing.NextURLPath {
		t.Errorf("untouched fields lost: poll=%q next=%q", merged.PollURL, merged.NextURLPath)
	}
	if merged.Map.ChatID != existing.Map.ChatID || merged.Map.Sender != existing.Map.Sender {
		t.Errorf("untouched map fields lost: %+v", merged.Map)
	}
	if !merged.StripHTML {
		t.Errorf("StripHTML lost on patch")
	}
}

func TestResolveJSONPath(t *testing.T) {
	var node any
	_ = json.Unmarshal([]byte(`{"from":{"user":{"displayName":"Ada"}},"@odata.deltaLink":"L","n":3,"messages":[{"ts":"9"},{"ts":"8"}]}`), &node)
	cases := map[string]string{
		"from.user.displayName": "Ada",
		"@odata.deltaLink":      "L",
		"n":                     "3",
		"messages.0.ts":         "9", // array index
		"messages.1.ts":         "8",
		"messages.5.ts":         "", // out of range
		"from.user.missing":     "",
		"nope":                  "",
	}
	for path, want := range cases {
		if got := jsonPathString(node, path); got != want {
			t.Errorf("path %q = %q, want %q", path, got, want)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// isCredError reports whether a Validate error is only the "credential not found"
// case (no SecureAPI store in a unit test), which is not a shape failure.
func isCredError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "credential")
}

func TestRenderSendBodyEscapes(t *testing.T) {
	out := renderSendBody(`{"body":{"content":"{text}"}}`, "chat-1", `he said "hi"`+"\nline2")
	// Must remain valid JSON after substitution.
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("send body not valid JSON after substitution: %v\n%s", err, out)
	}
}
