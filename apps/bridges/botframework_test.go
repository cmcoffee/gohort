package bridges

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func bfSpec() BotFrameworkSpec {
	return BotFrameworkSpec{
		Service: "teams", AppID: "app-1",
		Credential: "bf", ServiceHost: "https://smba.trafficmanager.net",
	}
}

// A channel message that @mentions the bot, in the shape Teams actually sends.
const channelActivityJSON = `{
  "type": "message",
  "id": "1700000000001",
  "timestamp": "2026-08-28T10:00:00.000Z",
  "serviceUrl": "https://smba.trafficmanager.net/amer/",
  "text": "<at>gohort</at> what is the deploy status?",
  "from": {"id": "29:abc", "name": "Craig Coffee", "aadObjectId": "aad-craig"},
  "recipient": {"id": "28:app-1", "name": "gohort"},
  "conversation": {"id": "19:thread@thread.tacv2", "conversationType": "channel", "tenantId": "tenant-9"},
  "channelData": {"tenant": {"id": "tenant-9"}, "team": {"id": "T", "name": "Platform"}, "channel": {"id": "C", "name": "deploys"}}
}`

func parseActivity(t *testing.T, raw string) botActivity {
	t.Helper()
	var a botActivity
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

// --- mapping -----------------------------------------------------------------

func TestActivityToInboundChannelMessage(t *testing.T) {
	req, err := activityToInbound(parseActivity(t, channelActivityJSON), bfSpec())
	if err != nil || req == nil {
		t.Fatalf("req = %v, err = %v", req, err)
	}
	// The mention is markup, not something the agent should have to read past.
	if req.Text != "what is the deploy status?" {
		t.Errorf("text = %q, want the mention stripped", req.Text)
	}
	if req.ChatID != "19:thread@thread.tacv2" {
		t.Errorf("chat id = %q", req.ChatID)
	}
	// The Entra object id is stable where the channel-scoped "29:…" id is not.
	if req.Handle != "aad-craig" {
		t.Errorf("handle = %q, want the aad object id", req.Handle)
	}
	if req.DisplayName != "Craig Coffee" {
		t.Errorf("display name = %q", req.DisplayName)
	}
	if req.ConversationName != "Platform / deploys" {
		t.Errorf("conversation name = %q", req.ConversationName)
	}
	if req.MsgID != "1700000000001" {
		t.Errorf("msg id = %q — without it every re-delivery reads as new", req.MsgID)
	}
	if req.Timestamp == "" {
		t.Error("timestamp dropped; replayed history would read as live")
	}
}

func TestActivityToInboundPersonalMessage(t *testing.T) {
	raw := `{"type":"message","id":"m1","serviceUrl":"https://smba.trafficmanager.net/amer/",
	  "text":"hello","from":{"id":"29:x","name":"Craig","aadObjectId":"aad-craig"},
	  "conversation":{"id":"a:1abc","conversationType":"personal","tenantId":"tenant-9"}}`
	req, err := activityToInbound(parseActivity(t, raw), bfSpec())
	if err != nil || req == nil {
		t.Fatalf("req = %v, err = %v", req, err)
	}
	// A 1:1 has no channel or conversation name, so the person names the thread.
	if req.ConversationName != "Craig" {
		t.Errorf("conversation name = %q, want the sender", req.ConversationName)
	}
	if req.ChatID != "a:1abc" {
		t.Errorf("chat id = %q", req.ChatID)
	}
}

func TestActivityToInboundIgnoresNonMessages(t *testing.T) {
	for _, typ := range []string{"conversationUpdate", "typing", "invoke", "messageReaction"} {
		raw := `{"type":"` + typ + `","id":"m1","text":"x",
		  "conversation":{"id":"c1","conversationType":"personal"}}`
		req, err := activityToInbound(parseActivity(t, raw), bfSpec())
		if err != nil {
			t.Errorf("%s: unexpected error %v", typ, err)
		}
		if req != nil {
			t.Errorf("%s was routed as a message", typ)
		}
	}
}

// An @mention with no words after it is a nudge, not a turn.
func TestActivityToInboundIgnoresMentionOnlyText(t *testing.T) {
	raw := `{"type":"message","id":"m1","text":"<at>gohort</at>   ",
	  "from":{"id":"29:x"},"conversation":{"id":"c1","conversationType":"channel"}}`
	req, err := activityToInbound(parseActivity(t, raw), bfSpec())
	if err != nil {
		t.Fatal(err)
	}
	if req != nil {
		t.Errorf("a mention with no message routed: %q", req.Text)
	}
}

func TestActivityToInboundRequiresConversationID(t *testing.T) {
	raw := `{"type":"message","id":"m1","text":"hi","conversation":{"conversationType":"personal"}}`
	if _, err := activityToInbound(parseActivity(t, raw), bfSpec()); err == nil {
		t.Fatal("an activity with no conversation id was accepted")
	}
}

func TestActivityToInboundHonoursChannelTypeGate(t *testing.T) {
	spec := bfSpec()
	spec.AcceptChannelTypes = []string{"personal"}
	req, err := activityToInbound(parseActivity(t, channelActivityJSON), spec)
	if err != nil {
		t.Fatal(err)
	}
	if req != nil {
		t.Error("a channel message routed despite a personal-only connector")
	}
}

// A valid signature proves Microsoft sent it, not that it came from the tenant
// this connector was approved for.
func TestActivityToInboundHonoursTenantPin(t *testing.T) {
	spec := bfSpec()
	spec.TenantID = "tenant-other"
	_, err := activityToInbound(parseActivity(t, channelActivityJSON), spec)
	if err == nil || !strings.Contains(err.Error(), "tenant") {
		t.Fatalf("err = %v, want a tenant mismatch", err)
	}

	spec.TenantID = "TENANT-9" // same tenant, different case
	if _, err := activityToInbound(parseActivity(t, channelActivityJSON), spec); err != nil {
		t.Errorf("a matching tenant was rejected: %v", err)
	}
}

// --- mention stripping -------------------------------------------------------

func TestStripMentions(t *testing.T) {
	tests := []struct{ in, want string }{
		{"<at>gohort</at> hello", "hello"},
		{"hey <at>gohort</at> ship it", "hey ship it"},
		{`<at id="0">gohort bot</at> status?`, "status?"},
		{"<at>a</at> <at>b</at> both of you", "both of you"},
		{"<AT>gohort</AT> upper", "upper"},
		{"no mention here", "no mention here"},
		{"a  b   c", "a b c"},
		{"<at>only</at>", ""},
	}
	for _, tc := range tests {
		if got := stripMentions(tc.in); got != tc.want {
			t.Errorf("stripMentions(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The agent's own words must survive: an angle bracket in prose is not markup.
func TestStripMentionsLeavesOrdinaryAngleBrackets(t *testing.T) {
	in := "compare a < b and b > c"
	if got := stripMentions(in); got != in {
		t.Errorf("got %q, want it untouched", got)
	}
}

// --- outbound shape ----------------------------------------------------------

func TestBotReplyActivity(t *testing.T) {
	spec := bfSpec()
	conv := botConv{ServiceURL: "https://smba.trafficmanager.net/amer", LastActivity: "m-42"}

	flat := botReplyActivity("hello", spec, conv)
	if flat["type"] != "message" || flat["text"] != "hello" {
		t.Errorf("activity = %v", flat)
	}
	// Saying "markdown" would promise formatting the outbound chokepoint already
	// stripped, since teams is registered as not rendering it.
	if flat["textFormat"] != "plain" {
		t.Errorf("textFormat = %v, want plain", flat["textFormat"])
	}
	if _, threaded := flat["replyToId"]; threaded {
		t.Error("replyToId set without reply_in_thread")
	}

	spec.ReplyInThread = true
	if got := botReplyActivity("hello", spec, conv)["replyToId"]; got != "m-42" {
		t.Errorf("replyToId = %v, want m-42", got)
	}
	// Nothing to thread under: post at the root rather than send a bad id.
	if _, threaded := botReplyActivity("hi", spec, botConv{ServiceURL: "x"})["replyToId"]; threaded {
		t.Error("replyToId set with no known activity id")
	}
}

// --- auth header -------------------------------------------------------------

func TestBearerToken(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Bearer abc.def.ghi", "abc.def.ghi"},
		{"bearer abc", "abc"},
		{"BEARER   abc  ", "abc"},
		{"Basic abc", ""},
		{"abc", ""},
		{"", ""},
		{"Bearer", ""},
	}
	for _, tc := range tests {
		if got := bearerToken(tc.in); got != tc.want {
			t.Errorf("bearerToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- conversation record -----------------------------------------------------

// The record is what makes a reply possible at all, so what it refuses matters
// as much as what it stores.
func TestRememberBotConv(t *testing.T) {
	T := &Bridges{AppCore{DB: OpenCache()}}
	spec := bfSpec()
	a := parseActivity(t, channelActivityJSON)

	if err := T.rememberBotConv("teams-bot", spec, a); err != nil {
		t.Fatalf("remember: %v", err)
	}
	conv, ok := T.getBotConv("19:thread@thread.tacv2")
	if !ok {
		t.Fatal("nothing recorded")
	}
	// The trailing slash has to go, or the send url gains a double slash.
	if conv.ServiceURL != "https://smba.trafficmanager.net/amer" {
		t.Errorf("service url = %q", conv.ServiceURL)
	}
	if conv.LastActivity != "1700000000001" {
		t.Errorf("last activity = %q, want the id to thread under", conv.LastActivity)
	}
	if conv.UserAadID != "aad-craig" {
		t.Errorf("user aad id = %q — a proactive open would have nobody to address", conv.UserAadID)
	}
	if conv.TenantID != "tenant-9" || conv.ConvType != "channel" || conv.Connector != "teams-bot" {
		t.Errorf("record = %+v", conv)
	}
}

// A learned reply address that could point anywhere is a token-exfiltration
// route. The credential's allow-list would refuse the send too, but failing
// here says so once, with the host named.
func TestRememberBotConvRejectsForeignServiceURL(t *testing.T) {
	T := &Bridges{AppCore{DB: OpenCache()}}
	a := parseActivity(t, channelActivityJSON)
	a.ServiceURL = "https://attacker.example/amer/"

	err := T.rememberBotConv("teams-bot", bfSpec(), a)
	if err == nil || !strings.Contains(err.Error(), "service_host") {
		t.Fatalf("err = %v, want a service_host refusal", err)
	}
	if _, ok := T.getBotConv("19:thread@thread.tacv2"); ok {
		t.Error("a refused conversation was recorded anyway")
	}
}

// A near-miss host must not pass on a prefix match alone.
func TestRememberBotConvRejectsHostPrefixTrick(t *testing.T) {
	T := &Bridges{AppCore{DB: OpenCache()}}
	a := parseActivity(t, channelActivityJSON)
	a.ServiceURL = "https://smba.trafficmanager.net.attacker.example/amer/"

	if err := T.rememberBotConv("teams-bot", bfSpec(), a); err == nil {
		t.Fatal("a look-alike host was accepted")
	}
}

func TestRememberBotConvRequiresServiceURL(t *testing.T) {
	T := &Bridges{AppCore{DB: OpenCache()}}
	a := parseActivity(t, channelActivityJSON)
	a.ServiceURL = ""

	if err := T.rememberBotConv("teams-bot", bfSpec(), a); err == nil {
		t.Fatal("an activity with no serviceUrl was accepted")
	}
}

// Microsoft may move a conversation between regional hosts, and the id to
// thread under is by definition the newest one, so every inbound rewrites.
func TestRememberBotConvUpdatesOnEveryInbound(t *testing.T) {
	T := &Bridges{AppCore{DB: OpenCache()}}
	a := parseActivity(t, channelActivityJSON)
	if err := T.rememberBotConv("teams-bot", bfSpec(), a); err != nil {
		t.Fatal(err)
	}
	a.ID = "1700000000002"
	a.ServiceURL = "https://smba.trafficmanager.net/emea/"
	if err := T.rememberBotConv("teams-bot", bfSpec(), a); err != nil {
		t.Fatal(err)
	}
	conv, _ := T.getBotConv("19:thread@thread.tacv2")
	if conv.LastActivity != "1700000000002" || conv.ServiceURL != "https://smba.trafficmanager.net/emea" {
		t.Errorf("record did not follow the conversation: %+v", conv)
	}
}
