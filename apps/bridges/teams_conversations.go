package bridges

// One Teams conversation's own setting, over its bot's per-type rule: mute
// the bot in a noisy channel, let it answer everything in a small team, or
// turn on one channel while channels stay off (docs/design/outside-audience.md).
// Kept per bot and conversation, a channel's threads sharing their channel's.

import (
	"net/http"
	"sort"
	"strings"

	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/teamsbots"
	"github.com/cmcoffee/gohort/core/ui"
)

// botConvAnswerTable holds the setting, keyed bot key + "|" + base conversation.
const botConvAnswerTable = "bridges_bot_conv_answers"

// What a conversation's setting may be. Empty follows the bot.
const (
	answerOff     = "off"     // never answers here
	answerAll     = "all"     // every message, named or not
	answerMention = "mention" // only when @mentioned, even where the bot says otherwise
)

// baseConversation is the conversation a channel thread belongs to: its id
// without the ";messageid=…" Teams adds for a reply in a thread.
func baseConversation(id string) string {
	base, _, _ := strings.Cut(strings.TrimSpace(id), ";")
	return base
}

func answerKey(bot teamsbots.Bot, convID string) string {
	return bot.Key() + "|" + baseConversation(convID)
}

// conversationAnswer is the owner's setting for one conversation, or "".
func (T *Bridges) conversationAnswer(bot teamsbots.Bot, convID string) string {
	var a string
	T.DB.Get(botConvAnswerTable, answerKey(bot, convID), &a)
	return a
}

// answerIn is the bot as it answers in one conversation, with that
// conversation's setting applied over its per-type rule, and whether the
// owner switched it off there.
func (T *Bridges) answerIn(bot teamsbots.Bot, convID, convType string) (teamsbots.Bot, bool) {
	switch T.conversationAnswer(bot, convID) {
	case answerOff:
		return bot, true
	case answerAll:
		bot.Accept, bot.Mention = nil, nil
	case answerMention:
		bot.Accept, bot.Mention = nil, []string{strings.TrimSpace(convType)}
	}
	return bot, false
}

// handleTeamsBotConversations is GET ?service=: the conversations the bot has
// been added to or heard in, a channel's threads as one, each with its setting.
func (T *Bridges) handleTeamsBotConversations(c *jsonapi.Call) (any, error) {
	bot, ok := teamsbots.Load(c.User, strings.TrimSpace(c.R.URL.Query().Get("service")))
	if !ok {
		return nil, jsonapi.NotFound("you have no such Teams bot")
	}
	byBase := map[string]map[string]any{}
	latest := map[string]string{}
	for _, k := range T.DB.Keys(botConvTable) {
		var bc botConv
		if !T.DB.Get(botConvTable, k, &bc) || bc.Connector != bot.Key() {
			continue
		}
		base := baseConversation(convOfBotConvKey(k))
		if bc.Updated <= latest[base] {
			continue // the newest thread names the conversation
		}
		latest[base] = bc.Updated
		byBase[base] = map[string]any{
			"id":      base,
			"title":   firstNonEmpty(bc.Title, "(unnamed)"),
			"type":    conversationTypeLabel(bc.ConvType),
			"updated": bc.Updated,
			"answer":  T.conversationAnswer(bot, base),
		}
	}
	rows := make([]map[string]any, 0, len(byBase))
	for _, r := range byBase {
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i]["updated"].(string) > rows[j]["updated"].(string) })
	return rows, nil
}

// conversationTypeLabel says a conversation type in words.
func conversationTypeLabel(ct string) string {
	switch ct {
	case "personal":
		return "One-to-one chat"
	case "groupChat":
		return "Group chat"
	case "channel":
		return "Team channel"
	}
	return ct
}

// handleTeamsBotConversationAnswer is POST ?service=&id= {answer}: set one
// conversation's setting, "" to follow the bot again.
func (T *Bridges) handleTeamsBotConversationAnswer(c *jsonapi.Call, body struct {
	Answer string `json:"answer"`
}) (any, error) {
	q := c.R.URL.Query()
	bot, ok := teamsbots.Load(c.User, strings.TrimSpace(q.Get("service")))
	if !ok {
		return nil, jsonapi.NotFound("you have no such Teams bot")
	}
	conv := baseConversation(q.Get("id"))
	if conv == "" {
		return nil, jsonapi.BadRequest("which conversation")
	}
	// Only one the bot has been in: a setting for a conversation it has never
	// seen is a key nobody will ever look up, or someone else's.
	known := false
	for _, k := range T.DB.Keys(botConvTable) {
		var bc botConv
		if T.DB.Get(botConvTable, k, &bc) && bc.Connector == bot.Key() && baseConversation(convOfBotConvKey(k)) == conv {
			known = true
			break
		}
	}
	if !known {
		return nil, jsonapi.NotFound("the bot has not been in that conversation")
	}
	switch a := strings.TrimSpace(body.Answer); a {
	case "":
		T.DB.Unset(botConvAnswerTable, answerKey(bot, conv))
	case answerOff, answerAll, answerMention:
		T.DB.Set(botConvAnswerTable, answerKey(bot, conv), a)
	default:
		return nil, jsonapi.BadRequest("a conversation answers as the bot does, every message, only when @mentioned, or not at all")
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// teamsBotConversationsUI is the Conversations window on a bot's row.
func teamsBotConversationsUI() ui.RowAction {
	return ui.ModalAction("Conversations", ui.Table{
		Source:    "/bridges/api/teams-bots/conversations?service={service}",
		RowKey:    "id",
		EmptyText: "It has not been added anywhere or heard from anyone yet.",
		Columns: []ui.Col{
			{Field: "title", Label: "Conversation", Flex: 3},
			{Field: "type", Label: "Kind", Flex: 1},
		},
		RowActions: []ui.RowAction{{Type: "select", Field: "answer", Label: "Answers",
			PostTo: "/bridges/api/teams-bots/conversations?service={service}&id={id}",
			Options: []ui.SelectOption{
				{Value: "", Label: "As the bot is set"},
				{Value: answerAll, Label: "Every message"},
				{Value: answerMention, Label: "Only when @mentioned"},
				{Value: answerOff, Label: "Not at all"},
			}}},
	})
}
