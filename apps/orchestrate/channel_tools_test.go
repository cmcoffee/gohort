package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestRelativeAge(t *testing.T) {
	now := time.Date(2026, 7, 15, 7, 37, 0, 0, time.UTC)
	cases := []struct {
		then time.Time
		want string
	}{
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-20 * time.Minute), "20m ago"},
		{now.Add(-6 * time.Hour), "6h ago"},
		{now.Add(-3 * 24 * time.Hour), "3d ago"},
		{now.Add(2 * time.Hour), "just now"}, // future/skew -> not negative
	}
	for _, c := range cases {
		if got := relativeAge(now, c.then); got != c.want {
			t.Errorf("relativeAge(now, %s) = %q, want %q", c.then, got, c.want)
		}
	}
}

// localChatTime turns a UTC bridge timestamp into local time + relative age,
// and leaves unparseable input alone.
func TestLocalChatTime(t *testing.T) {
	out := localChatTime("2026-07-15T02:00:30Z")
	if out == "2026-07-15T02:00:30Z" || out == "" {
		t.Fatalf("UTC timestamp was not localized: %q", out)
	}
	if !containsAgo(out) {
		t.Errorf("expected a relative-age suffix, got %q", out)
	}
	if got := localChatTime("not a timestamp"); got != "not a timestamp" {
		t.Errorf("unparseable input should pass through, got %q", got)
	}
	if got := localChatTime(""); got != "" {
		t.Errorf("empty input should stay empty, got %q", got)
	}
}

func containsAgo(s string) bool {
	for _, sub := range []string{"ago", "just now"} {
		if len(s) >= len(sub) {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

// namedThreads is a transport whose conversations go by the phone's names,
// not the owner's channel names.
type namedThreads struct{}

func (namedThreads) Threads(string) []ChannelThreadInfo {
	return []ChannelThreadInfo{
		{ChatID: "chat-fam", Service: "sms", DisplayName: "Mom, Dad & me"},
		{ChatID: "chat-pat", Service: "sms", DisplayName: "Pat"},
		{ChatID: "im-1", Service: "imessage", DisplayName: "Alex", Handle: "+15550000001"},
		{ChatID: "im-2", Service: "imessage", DisplayName: "Sam", Handle: "+15550000002"},
	}
}
func (namedThreads) Messages(string, string, int) []ChannelLine { return nil }
func (namedThreads) Members(string, string) []ChannelMember     { return nil }
func (namedThreads) Deliver(string, string, string, string, string, string, []string) error {
	return nil
}

// The owner names a conversation by its channel ("send it to Family"); the
// transport names it something else. The chat tools know both: list_chats says
// which channel each conversation is on, and send_message takes a channel's
// name for the conversation it is bound to. A channel over a whole service
// names no one conversation, so naming it asks which instead of guessing.
func TestAgentsKnowConversationsByTheirChannelNames(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	prevThreads, had := ActiveChannelThreads()
	RegisterChannelThreads(namedThreads{})
	t.Cleanup(func() {
		if had {
			RegisterChannelThreads(prevThreads)
		} else {
			RegisterChannelThreads(nil)
		}
	})
	SaveChannel(RootDB, Channel{ID: "c1", Owner: "owner", Name: "Family", Service: "sms", Address: "chat-fam", AgentID: "a1"})
	SaveChannel(RootDB, Channel{ID: "c2", Owner: "owner", Name: "Everything", Service: "imessage", AgentID: "a1"})
	SaveChannel(RootDB, Channel{ID: "c3", Owner: "owner", Name: "Texts", Service: "sms", AgentID: "a1"})
	tools := map[string]AgentToolDef{}
	for _, td := range channelChatTools(&ToolSession{Username: "owner"}, "owner", "a1") {
		tools[td.Tool.Name] = td
	}
	ctx := context.Background()

	out, err := tools["list_chats"].Handler(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	// A conversation bound to its own channel goes by that one; the rest by the
	// whole-service channel of their own service.
	for _, want := range []string{`Mom, Dad & me on the "Family" channel`, `Pat on the "Texts" channel`, `Alex (+15550000001) on the "Everything" channel`} {
		if !strings.Contains(out, want) {
			t.Errorf("list_chats does not say %q:\n%s", want, out)
		}
	}

	for _, to := range []string{"Family", "the family channel"} {
		// A proactive send queues for the owner's approval, naming where it goes.
		out, err := tools["send_message"].Handler(ctx, map[string]any{"to": to, "text": "Dinner at six."})
		if err != nil || !strings.Contains(out, "chat-fam") {
			t.Errorf("send_message to %q did not reach the Family conversation: %q %v", to, out, err)
		}
	}

	_, err = tools["send_message"].Handler(ctx, map[string]any{"to": "Everything", "text": "Hi."})
	if err == nil || !strings.Contains(err.Error(), "names no one recipient") || !strings.Contains(err.Error(), "Alex") || !strings.Contains(err.Error(), "Sam") {
		t.Errorf("a whole-service channel was not answered with its conversations: %v", err)
	}
	if _, err := tools["send_message"].Handler(ctx, map[string]any{"to": "Nobody", "text": "Hi."}); err == nil || !strings.Contains(err.Error(), "no recipient") {
		t.Errorf("an unknown name was not refused: %v", err)
	}
}
