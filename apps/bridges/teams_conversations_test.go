package bridges

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/teamsbots"
)

// convWorld is a bot, new-bot defaults, that has been in a group chat, a
// channel (two threads of it) and a one-to-one chat.
func convWorld(t *testing.T) (*Bridges, teamsbots.Bot) {
	t.Helper()
	T := linkWorld(t)
	accept, mention := teamsbots.NewBotDefaults()
	bot, err := teamsbots.Save(teamsbots.Bot{Owner: "alice", Name: "Wren", AppID: botApp, ServiceHost: "https://smba.trafficmanager.net", Accept: accept, Mention: mention}, "s", false)
	if err != nil {
		t.Fatal(err)
	}
	for id, bc := range map[string]botConv{
		"19:group@thread.v2":               {ConvType: "groupChat", Title: "Lunch crew", Updated: "2026-11-05T09:00:00Z"},
		"19:chan@thread.tacv2;messageid=1": {ConvType: "channel", Title: "Platform / deploys", Updated: "2026-11-05T10:00:00Z"},
		"19:chan@thread.tacv2;messageid=2": {ConvType: "channel", Title: "Platform / deploys (old name)", Updated: "2026-11-05T08:00:00Z"},
		"a:dm-1":                           {ConvType: "personal", Title: "Ada Lovelace", Updated: "2026-11-05T07:00:00Z"},
		"19:someone-elses@thread.v2":       {ConvType: "groupChat", Title: "Not ours", Updated: "2026-11-05T11:00:00Z", Connector: "bob\x00teams"},
	} {
		if bc.Connector == "" {
			bc.Connector = bot.Key()
		}
		T.setBotConv(id, bc)
	}
	return T, bot
}

func convCall(user, rawURL string) *jsonapi.Call {
	return &jsonapi.Call{W: httptest.NewRecorder(), R: httptest.NewRequest(http.MethodGet, rawURL, nil), User: user}
}

func convStatus(err error) int {
	var je *jsonapi.Error
	if errors.As(err, &je) {
		return je.Code
	}
	if err != nil {
		return 500
	}
	return 200
}

// One conversation's setting overrides its bot's rule there and nowhere else.
func TestAConversationsSettingOverridesItsBot(t *testing.T) {
	T, bot := convWorld(t)
	group, channel := "19:group@thread.v2", "19:chan@thread.tacv2;messageid=1"
	if b, muted := T.answerIn(bot, group, "groupChat"); muted || !b.NeedsMention("groupChat") {
		t.Error("with no setting, a group follows the bot")
	}
	T.DB.Set(botConvAnswerTable, answerKey(bot, group), answerAll)
	if b, _ := T.answerIn(bot, group, "groupChat"); b.NeedsMention("groupChat") {
		t.Error("a group set to every message still needed a mention")
	}
	// A channel turned on by itself while channels stay off for the bot, and
	// every thread of it with it.
	T.DB.Set(botConvAnswerTable, answerKey(bot, "19:chan@thread.tacv2"), answerMention)
	if b, _ := T.answerIn(bot, channel, "channel"); !b.Accepts("channel") || !b.NeedsMention("channel") {
		t.Error("a channel turned on by itself does not answer when named")
	}
	if b, _ := T.answerIn(bot, "19:other@thread.tacv2", "channel"); b.Accepts("channel") {
		t.Error("turning on one channel turned on another")
	}
	T.DB.Set(botConvAnswerTable, answerKey(bot, group), answerOff)
	if _, muted := T.answerIn(bot, group, "groupChat"); !muted {
		t.Error("a group switched off still answers")
	}
}

// The list is the bot's conversations, a channel's threads as one, newest
// first; the setting is the owner's, for a conversation the bot was in.
func TestTheBotListsItsConversationsAndTakesASetting(t *testing.T) {
	T, _ := convWorld(t)
	got, err := T.handleTeamsBotConversations(convCall("alice", "/x?service=teams"))
	if err != nil {
		t.Fatal(err)
	}
	rows := got.([]map[string]any)
	if len(rows) != 3 || rows[0]["id"] != "19:chan@thread.tacv2" || rows[0]["type"] != "Team channel" || rows[0]["title"] != "Platform / deploys" {
		t.Fatalf("rows: %v", rows)
	}
	for _, r := range rows {
		if r["title"] == "Not ours" {
			t.Error("another bot's conversation was listed")
		}
	}

	set := func(user, id, answer string) int {
		_, err := T.handleTeamsBotConversationAnswer(convCall(user, "/x?service=teams&id="+id), struct {
			Answer string `json:"answer"`
		}{answer})
		return convStatus(err)
	}
	if code := set("alice", "19:chan@thread.tacv2", answerAll); code != 200 {
		t.Errorf("set: %d", code)
	}
	if code := set("alice", "19:never@thread.v2", answerAll); code != 404 {
		t.Errorf("a conversation the bot was never in: %d", code)
	}
	if code := set("alice", "19:someone-elses@thread.v2", answerAll); code != 404 {
		t.Errorf("another bot's conversation: %d", code)
	}
	if code := set("bob", "19:chan@thread.tacv2", answerAll); code != 404 {
		t.Errorf("someone else's bot: %d", code)
	}
	if code := set("alice", "19:chan@thread.tacv2", "loud"); code != 400 {
		t.Errorf("an unknown setting: %d", code)
	}
	if code := set("alice", "19:chan@thread.tacv2", ""); code != 200 || T.DB.Get(botConvAnswerTable, answerKey(teamsbots.Bot{Owner: "alice", Service: "teams"}, "19:chan@thread.tacv2"), new(string)) {
		t.Error("clearing the setting left it in place")
	}
}
