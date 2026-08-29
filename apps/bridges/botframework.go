// The bridges half of the bot_framework connector kind (core/connector_botframework.go).
//
// Bot Framework is push-only and has no poll API, so there is no tick to hang
// anything on: Microsoft POSTs one Activity per request to a public per-connector
// route, and replies go out on a short timer that drains the shared outbox.
//
// Two things make it different from the rest_messaging providers next door, and
// both are why it is a kind of its own rather than a third webhook_provider:
//
//  1. AUTH IS A TOKEN, NOT A SECRET. Each request carries a bearer JWT signed by
//     Microsoft's rotating keys. There is nothing to paste into an admin form —
//     the connector's app id is the audience, and core/jwks does the rest.
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
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const botConvTable = "bridges_bot_conversations" // conversation id → botConv

// maxActivityBody caps an inbound activity. Teams messages are small; a cap
// keeps a malformed or hostile POST from being read into memory whole.
const maxActivityBody = 1 << 20

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
	Connector    string `json:"connector,omitempty"`
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
func activityToInbound(a botActivity, spec BotFrameworkSpec) (*hookRequest, error) {
	// Only messages are conversation. conversationUpdate fires when the app is
	// installed or someone joins, typing is noise, invoke is a card action this
	// bridge does not implement yet.
	if !strings.EqualFold(strings.TrimSpace(a.Type), "message") {
		return nil, nil
	}
	if !spec.Accepts(a.Conversation.ConversationType) {
		return nil, nil
	}
	// A single-tenant bot must not act on another tenant's traffic even if the
	// token verified: the signature proves Microsoft sent it, not that it came
	// from the tenant this connector was approved for.
	if spec.TenantID != "" && !strings.EqualFold(a.tenant(), spec.TenantID) {
		return nil, fmt.Errorf("activity from tenant %q, connector is pinned to %q", a.tenant(), spec.TenantID)
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

// handleBotActivity is the public inbound route for every bot_framework
// connector: /bridges/api/bot/<connector-name>.
//
// The gate order matches handleWebhook next door, and the 202s are deliberate:
// an unapproved or switched-off bridge acknowledges rather than erroring, so
// Microsoft does not retry a message nobody is going to act on.
func (T *Bridges) handleBotActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/bot/"), "/")
	if name == "" {
		http.Error(w, "connector name required in path", http.StatusNotFound)
		return
	}
	c, ok := GetConnector(RootDB, name)
	if !ok || c.Kind != BotFrameworkConnectorKind {
		http.Error(w, "no such bot connector", http.StatusNotFound)
		return
	}
	var spec BotFrameworkSpec
	if err := json.Unmarshal(c.Spec, &spec); err != nil {
		http.Error(w, "bad connector", http.StatusInternalServerError)
		return
	}
	if !c.Approved {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// The audience check is the load-bearing half: Bot Framework signs for every
	// bot in the world, and what makes a token OURS is that it names our app id.
	if _, err := BotFrameworkVerifier().Verify(r.Context(), token, spec.AppID); err != nil {
		Warn("[bridges] bot %q rejected an activity: %v", name, err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Master panic switch: recorded-only when transport is off.
	if !T.config().Enabled {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	body, _ := io.ReadAll(io.LimitReader(r.Body, maxActivityBody))
	var a botActivity
	if err := json.Unmarshal(body, &a); err != nil {
		Warn("[bridges] bot %q got an unparseable activity: %v", name, err)
		w.WriteHeader(http.StatusAccepted) // ack; a retry would not parse either
		return
	}

	req, err := activityToInbound(a, spec)
	if err != nil {
		Warn("[bridges] bot %q dropped an activity: %v", name, err)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if req == nil {
		w.WriteHeader(http.StatusOK) // nothing to route, and nothing wrong
		return
	}

	// Remember how to answer BEFORE routing: the agent's reply may be enqueued
	// while this request is still in flight, and without the record the outbound
	// loop has nowhere to send it.
	if err := T.rememberBotConv(c.Name, spec, a); err != nil {
		Warn("[bridges] bot %q will not be able to reply in %s: %v", name, a.Conversation.ID, err)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	key := BridgeKey{Owner: c.Owner, Service: spec.Service, Enabled: true, Name: c.Name}
	T.ingestInbound(key, *req)
	w.WriteHeader(http.StatusOK)
}

// rememberBotConv records where and how to answer this conversation, refusing a
// serviceUrl outside the connector's approved host.
//
// The refusal is defence in depth rather than the only guard — the credential's
// own allow-list would block the send too — but it fails here, once, with a log
// naming the host, instead of once per queued reply inside a dispatch error.
func (T *Bridges) rememberBotConv(connector string, spec BotFrameworkSpec, a botActivity) error {
	svcURL := strings.TrimRight(strings.TrimSpace(a.ServiceURL), "/")
	if svcURL == "" {
		return fmt.Errorf("activity carries no serviceUrl")
	}
	host := strings.TrimRight(strings.TrimSpace(spec.ServiceHost), "/")
	if host != "" && !strings.HasPrefix(svcURL+"/", host+"/") {
		return fmt.Errorf("serviceUrl %q is outside the connector's service_host %q", svcURL, host)
	}
	T.setBotConv(a.Conversation.ID, botConv{
		ServiceURL:   svcURL,
		TenantID:     a.tenant(),
		ConvType:     strings.TrimSpace(a.Conversation.ConversationType),
		LastActivity: strings.TrimSpace(a.ID),
		UserAadID:    strings.TrimSpace(a.From.AadObjectID),
		Connector:    connector,
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

// botBridgeCancel tracks the running outbound loops so a re-materialize (admin
// re-approve, startup reload) or a teardown can stop the prior one.
var (
	botBridgeMu     sync.Mutex
	botBridgeCancel = map[string]context.CancelFunc{}
)

// startBotBridge launches (or restarts) a connector's outbound loop. Idempotent.
func (T *Bridges) startBotBridge(c Connector) error {
	var spec BotFrameworkSpec
	if err := json.Unmarshal(c.Spec, &spec); err != nil {
		return fmt.Errorf("bad bot_framework spec for %q: %w", c.Name, err)
	}
	if strings.TrimSpace(spec.Service) == "" {
		spec.Service = "teams"
	}

	botBridgeMu.Lock()
	if cancel, ok := botBridgeCancel[c.Name]; ok {
		cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	botBridgeCancel[c.Name] = cancel
	botBridgeMu.Unlock()

	go T.botOutboundLoop(ctx, c, spec)
	Log("[bridges] bot %q live at /bridges/api/bot/%s (svc=%s, app=%s)", c.Name, c.Name, spec.Service, spec.AppID)
	return nil
}

func (T *Bridges) stopBotBridge(name string) {
	botBridgeMu.Lock()
	if cancel, ok := botBridgeCancel[name]; ok {
		cancel()
		delete(botBridgeCancel, name)
	}
	botBridgeMu.Unlock()
}

// botOutboundLoop drains this service's outbox on a short timer. Inbound is
// push, so this is the only clock in the bridge.
func (T *Bridges) botOutboundLoop(ctx context.Context, c Connector, spec BotFrameworkSpec) {
	Log("[bridges] bot %q outbound loop started (svc=%s)", c.Name, spec.Service)
	tick := time.NewTicker(botOutboundTick)
	defer tick.Stop()
	for {
		T.deliverBotOutbound(ctx, spec)
		select {
		case <-ctx.Done():
			Log("[bridges] bot %q outbound loop stopped", c.Name)
			return
		case <-tick.C:
		}
	}
}

// deliverBotOutbound posts each queued reply back into its conversation.
//
// A send failure re-queues the failed item and everything behind it, so nothing
// is lost and order is kept — the same discipline as deliverOutbound. An item
// whose conversation is UNKNOWN is different: there is no address for it and no
// tick will ever produce one, so it is dropped loudly rather than re-queued
// forever into a warning every three seconds.
func (T *Bridges) deliverBotOutbound(ctx context.Context, spec BotFrameworkSpec) {
	items := T.drainOutbox(spec.Service)
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
		if !ok || conv.ServiceURL == "" {
			Warn("[bridges] bot reply for %s dropped: no conversation on record, so there is no serviceUrl to send to (the bot must receive a message in a conversation before it can post there)", it.ChatID)
			continue
		}

		body, err := json.Marshal(botReplyActivity(it.Text, spec, conv))
		if err != nil {
			Warn("[bridges] bot reply for %s could not be encoded: %v", it.ChatID, err)
			continue
		}
		sendURL := conv.ServiceURL + "/v3/conversations/" + url.PathEscape(it.ChatID) + "/activities"
		_, status, err := authedRequest(spec.Credential, "POST", sendURL, string(body))
		if err != nil || status >= 300 {
			Warn("[bridges] bot send failed (svc=%s chat=%s): err=%v status=%d — re-queued %d item(s)",
				spec.Service, it.ChatID, err, status, len(items)-i)
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
// says does not render it, and teams is registered that way because its other
// transport posts HTML. Saying "markdown" here would only promise formatting
// that was stripped upstream.
func botReplyActivity(text string, spec BotFrameworkSpec, conv botConv) map[string]any {
	act := map[string]any{
		"type":       "message",
		"text":       text,
		"textFormat": "plain",
	}
	if spec.ReplyInThread && conv.LastActivity != "" {
		act["replyToId"] = conv.LastActivity
	}
	return act
}
