// Teams bots (core/teamsbots): a person's own Microsoft Teams bot, which
// answers in Teams with their agents.
//
// Bot Framework is push-only and has no poll API, so there is no tick to hang
// anything on: Microsoft POSTs one Activity per request to the bot's webhook
// (core/webhooks, checked "botframework"), and replies go out on a short timer
// that drains the shared outbox.
//
// Two things make it a channel type of its own rather than a connector:
//
//  1. AUTH IS A TOKEN, NOT A SECRET. Each request carries a bearer JWT signed by
//     Microsoft's rotating keys, whose audience is the bot's app id.
//  2. THE REPLY ADDRESS IS LEARNED. An activity carries the serviceUrl its
//     conversation lives behind, which is regional and per-conversation. So the
//     bridge remembers a conversation the first time it hears from it, and that
//     record is what makes a reply possible at all.
//
// Everything after the front door is the existing machinery: an activity becomes
// a hookRequest and goes through ingestInbound, so dedup, channel binding, agent
// dispatch, threads, retention and the loop guard all apply unchanged.
package bridges

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/teamsbots"
	"github.com/cmcoffee/gohort/core/webhooks"
	"github.com/cmcoffee/snugforge/jwcrypt"
)

const botConvTable = "bridges_bot_conversations" // conversation id → botConv

// botOutboundTick is how often a bound agent's reply is looked for. Inbound is
// instant; this is the only latency on the way back.
const botOutboundTick = 3 * time.Second

// botConv is what the bridge remembers about a conversation so it can speak
// into it later. Written on every inbound, because every field can change:
// Microsoft may move a conversation between regional serviceUrls, and the
// activity id to thread under is by definition the newest one.
type botConv struct {
	ServiceURL   string `json:"service_url"` // where replies POST; regional, learned
	TenantID     string `json:"tenant_id,omitempty"`
	ConvType     string `json:"conv_type,omitempty"`     // personal | channel | groupChat
	LastActivity string `json:"last_activity,omitempty"` // replyToId for threaded replies
	UserAadID    string `json:"user_aad_id,omitempty"`   // what a future proactive open needs
	Connector    string `json:"connector,omitempty"`     // the bot that heard it: owner \x00 service
	Updated      string `json:"updated,omitempty"`
}

func (T *Bridges) getBotConv(chatID string) (botConv, bool) {
	var bc botConv
	ok := T.DB.Get(botConvTable, chatID, &bc)
	return bc, ok
}

func (T *Bridges) setBotConv(chatID string, bc botConv) { T.DB.Set(botConvTable, chatID, bc) }

// --- inbound -----------------------------------------------------------------

// botActivity is the subset of a Bot Framework Activity this bridge reads.
type botActivity struct {
	Type       string `json:"type"` // message | conversationUpdate | typing | invoke | …
	ID         string `json:"id"`
	Timestamp  string `json:"timestamp"`
	ServiceURL string `json:"serviceUrl"`
	ChannelID  string `json:"channelId"`
	Text       string `json:"text"`
	From       struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		AadObjectID string `json:"aadObjectId"`
	} `json:"from"`
	Recipient struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"recipient"`
	Conversation struct {
		ID               string `json:"id"`
		Name             string `json:"name"`
		ConversationType string `json:"conversationType"`
		TenantID         string `json:"tenantId"`
	} `json:"conversation"`
	ChannelData struct {
		Tenant  struct{ ID string }       `json:"tenant"`
		Team    struct{ ID, Name string } `json:"team"`
		Channel struct{ ID, Name string } `json:"channel"`
	} `json:"channelData"`
}

// tenant returns the tenant id from wherever this activity carried it.
func (a botActivity) tenant() string {
	return firstNonEmpty(strings.TrimSpace(a.Conversation.TenantID), strings.TrimSpace(a.ChannelData.Tenant.ID))
}

// title names the thread for the dashboard: the channel or team it arrived in,
// else the conversation's own name, else the person for a 1:1.
func (a botActivity) title() string {
	if ch := strings.TrimSpace(a.ChannelData.Channel.Name); ch != "" {
		if team := strings.TrimSpace(a.ChannelData.Team.Name); team != "" {
			return team + " / " + ch
		}
		return ch
	}
	return firstNonEmpty(strings.TrimSpace(a.Conversation.Name), strings.TrimSpace(a.From.Name))
}

// atTagRE matches the <at>…</at> spans Teams wraps around a mention.
var atTagRE = regexp.MustCompile(`(?is)<at\b[^>]*>.*?</at>`)

// stripMentions removes the mention markup Teams puts in the text of any
// message that addresses the bot. Without this every channel turn reaches the
// agent starting with a literal "<at>gohort</at>", which is markup the agent
// then has to reason about and sometimes echoes back.
func stripMentions(text string) string {
	return strings.TrimSpace(collapseSpaces(atTagRE.ReplaceAllString(text, " ")))
}

// collapseSpaces squeezes the runs of whitespace a removed mention leaves.
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// activityToInbound maps a message activity onto the Bridges inbound contract,
// or reports why it should be ignored. A nil request with a nil error means
// "not an error, just nothing to route" — the same convention the graph
// provider's extract uses for events it does not care about.
func activityToInbound(a botActivity, bot teamsbots.Bot) (*hookRequest, error) {
	// Only messages are conversation. conversationUpdate fires when the app is
	// installed or someone joins, typing is noise, invoke is a card action this
	// bridge does not implement yet.
	if !strings.EqualFold(strings.TrimSpace(a.Type), "message") {
		return nil, nil
	}
	if !bot.Accepts(a.Conversation.ConversationType) {
		return nil, nil
	}
	// A single-tenant bot must not act on another tenant's traffic even if the
	// token verified: the signature proves Microsoft sent it, not that it came
	// from the bot's own tenant.
	if bot.TenantID != "" && !strings.EqualFold(a.tenant(), bot.TenantID) {
		return nil, fmt.Errorf("an activity from tenant %q, and the bot belongs to %q", a.tenant(), bot.TenantID)
	}

	chatID := strings.TrimSpace(a.Conversation.ID)
	if chatID == "" {
		return nil, fmt.Errorf("activity carries no conversation id")
	}
	text := stripMentions(a.Text)
	if text == "" {
		return nil, nil // an @mention and nothing else, or an attachment-only post
	}

	return &hookRequest{
		ChatID: chatID,
		// Prefer the Entra object id: it is stable across Teams installs and is
		// the same identity other Microsoft surfaces use, where the channel-scoped
		// "29:…" id is not.
		Handle:           firstNonEmpty(strings.TrimSpace(a.From.AadObjectID), strings.TrimSpace(a.From.ID)),
		DisplayName:      strings.TrimSpace(a.From.Name),
		ConversationName: a.title(),
		Text:             text,
		MsgID:            strings.TrimSpace(a.ID),
		Timestamp:        strings.TrimSpace(a.Timestamp),
	}, nil
}

// botframeworkCheck is the "botframework" check of a Teams bot's webhook:
// the bearer token Microsoft signed, for the bot's app id and this very
// activity, then the activity read into a message for the bot's service.
//
// The 2xx answers are deliberate: a bot waiting for approval, or bridges
// switched off, acknowledges rather than erroring, so Microsoft does not
// retry a message nobody is going to act on.
func (T *Bridges) botframeworkCheck(r *http.Request, h webhooks.Webhook, body []byte) (int, string, error) {
	bot, ok := teamsbots.Load(h.Owner, h.Channel())
	if !ok {
		return http.StatusAccepted, "not passed on: there is no Teams bot for " + h.Channel(), nil
	}
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		return 0, "", fmt.Errorf("it came with no Bot Framework token")
	}
	// The audience check is the load-bearing half: Bot Framework signs for every
	// bot in the world, and what makes a token this bot's is its app id.
	claims, err := teamsbots.Verifier().Verify(r.Context(), token, bot.AppID)
	if err != nil {
		return 0, "", fmt.Errorf("its token is not this bot's: %v", err)
	}
	var a botActivity
	if err := json.Unmarshal(body, &a); err != nil {
		return http.StatusAccepted, "passed over: an activity that does not read", nil // a retry would not parse either
	}
	// The token must be FOR this activity, not only for this bot: Microsoft
	// signs the serviceUrl it means replies to go to, and a key it publishes
	// says which channels it may sign for.
	if err := botTokenFitsActivity(r.Context(), token, claims, a); err != nil {
		return 0, "", err
	}
	switch {
	case bot.AwaitingApproval:
		return http.StatusAccepted, "not passed on: the bot waits for an administrator's approval", nil
	case bot.AdminDisabled:
		return http.StatusAccepted, "not passed on: an administrator switched the bot off", nil
	case !T.config().Enabled:
		return http.StatusAccepted, "not passed on: bridges are switched off", nil
	}
	req, err := activityToInbound(a, bot)
	if err != nil {
		return http.StatusAccepted, "passed over: " + err.Error(), nil
	}
	if req == nil {
		return http.StatusOK, "passed over: not a message for the bot", nil
	}
	// Remember how to answer BEFORE routing: the agent's reply may be enqueued
	// while this request is still in flight, and without the record the reply
	// loop has nowhere to send it.
	if err := T.rememberBotConv(bot, a); err != nil {
		return http.StatusAccepted, "not passed on: " + err.Error(), nil
	}
	T.ingestInbound(BridgeKey{Owner: bot.Owner, Service: bot.Service, Enabled: true, Name: bot.Name}, *req)
	return http.StatusOK, "passed to " + bot.Service, nil
}

// botTokenFitsActivity holds a verified token to the activity it came with,
// as Bot Framework's own rules require: its serviceurl claim names the
// activity's serviceUrl, and the signing key's endorsements, when published,
// include the activity's channel. Without the first, a token minted for one
// conversation's reply address carried an activity pointing replies (and the
// bot's credential) somewhere else.
func botTokenFitsActivity(ctx context.Context, token string, claims jwcrypt.Claims, a botActivity) error {
	if want, ok := claims["serviceurl"].(string); ok && strings.TrimSpace(want) != "" {
		if !strings.EqualFold(strings.TrimRight(strings.TrimSpace(want), "/"), strings.TrimRight(strings.TrimSpace(a.ServiceURL), "/")) {
			return fmt.Errorf("the token is for serviceUrl %q, the activity names %q", want, a.ServiceURL)
		}
	}
	hdr, err := jwcrypt.DecodeJWTHeader(token)
	if err != nil || hdr.KeyID == "" {
		return nil
	}
	key, ok := teamsbots.Verifier().KeyByID(ctx, hdr.KeyID)
	if !ok || key == nil {
		return nil
	}
	raw, ok := key.Extra["endorsements"]
	if !ok {
		return nil
	}
	var endorsed []string
	if json.Unmarshal(raw, &endorsed) != nil {
		return fmt.Errorf("the signing key's endorsements are unreadable")
	}
	ch := strings.TrimSpace(a.ChannelID)
	for _, e := range endorsed {
		if strings.EqualFold(strings.TrimSpace(e), ch) {
			return nil
		}
	}
	return fmt.Errorf("the signing key is not endorsed for channel %q", ch)
}

// rememberBotConv records where and how to answer this conversation, refusing a
// serviceUrl outside the bot's service host: its reply token goes only there.
func (T *Bridges) rememberBotConv(bot teamsbots.Bot, a botActivity) error {
	svcURL := strings.TrimRight(strings.TrimSpace(a.ServiceURL), "/")
	if svcURL == "" {
		return fmt.Errorf("the activity carries no serviceUrl")
	}
	if !bot.Within(svcURL) {
		return fmt.Errorf("serviceUrl %q is outside the bot's service host %q", svcURL, bot.ServiceHost)
	}
	T.setBotConv(a.Conversation.ID, botConv{
		ServiceURL:   svcURL,
		TenantID:     a.tenant(),
		ConvType:     strings.TrimSpace(a.Conversation.ConversationType),
		LastActivity: strings.TrimSpace(a.ID),
		UserAadID:    strings.TrimSpace(a.From.AadObjectID),
		Connector:    bot.Key(),
		Updated:      now(),
	})
	return nil
}

// bearerToken pulls the token out of an Authorization header.
func bearerToken(header string) string {
	const prefix = "bearer "
	h := strings.TrimSpace(header)
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// --- outbound ----------------------------------------------------------------

var teamsOnce sync.Once

// startTeams starts the one reply loop every Teams bot shares: each tick, each
// bot an administrator lets answer sends what its owner's agents wrote back.
// Read from the store every tick, so an approval or a switch-off from Admin
// takes effect without a restart.
func (T *Bridges) startTeams() {
	teamsOnce.Do(func() {
		go func() {
			for {
				T.deliverTeams(context.Background())
				time.Sleep(botOutboundTick)
			}
		}()
	})
}

// deliverTeams is one pass of the reply loop: every bot an administrator
// lets answer sends what its owner's agents wrote back.
func (T *Bridges) deliverTeams(ctx context.Context) {
	for _, bot := range teamsbots.All() {
		if bot.Usable() {
			T.deliverBotOutbound(ctx, bot)
		}
	}
}

// deliverBotOutbound posts each queued reply back into its conversation.
//
// A send failure re-queues the failed item and everything behind it, so nothing
// is lost and order is kept. An item whose conversation is UNKNOWN to this bot
// is different: there is no address for it and no tick will ever produce one,
// so it is dropped loudly rather than re-queued forever.
func (T *Bridges) deliverBotOutbound(ctx context.Context, bot teamsbots.Bot) {
	items := T.drainOutbox(bot.Service, bot.Owner)
	for i, it := range items {
		if ctx.Err() != nil {
			for _, r := range items[i:] {
				T.enqueueOutbox(r)
			}
			return
		}
		if strings.TrimSpace(it.Text) == "" {
			continue
		}
		conv, ok := T.getBotConv(it.ChatID)
		if !ok || conv.ServiceURL == "" || conv.Connector != bot.Key() {
			Warn("[bridges] Teams reply for %s dropped: the bot %q has not heard from that conversation, so there is nowhere to send it", it.ChatID, bot.Name)
			continue
		}
		if err := teamsbots.Send(ctx, bot, conv.ServiceURL, it.ChatID, botReplyActivity(it.Text, bot, conv)); err != nil {
			Warn("[bridges] Teams reply failed (bot=%s chat=%s): %v, re-queued %d item(s)", bot.Name, it.ChatID, err, len(items)-i)
			for _, r := range items[i:] {
				T.enqueueOutbox(r)
			}
			return
		}
	}
}

// botReplyActivity builds the outbound Activity.
//
// textFormat is "plain" to match what the outbound chokepoint already did to the
// text: enqueueOutbox flattens markdown for any service the transport registry
// says does not render it, and teams is registered that way. Saying "markdown"
// here would only promise formatting that was stripped upstream.
func botReplyActivity(text string, bot teamsbots.Bot, conv botConv) map[string]any {
	act := map[string]any{
		"type":       "message",
		"text":       text,
		"textFormat": "plain",
	}
	if bot.ReplyInThread && conv.LastActivity != "" {
		act["replyToId"] = conv.LastActivity
	}
	return act
}
