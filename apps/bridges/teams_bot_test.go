package bridges

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/clientcert"
	"github.com/cmcoffee/gohort/core/teamsbots"
	"github.com/cmcoffee/gohort/core/webhooks"
	"github.com/cmcoffee/snugforge/jwcrypt"
)

const botApp = "11111111-2222-3333-4444-555555555555"

// signing is Microsoft's one signing key and key set for the whole package.
// One, because the verifier is shared and keeps the first key-set address it
// fetches: a second test's own key server was never asked, and its tokens
// failed against the first test's cached key.
var signing struct {
	once sync.Once
	key  *rsa.PrivateKey
	url  string
}

// microsoftStandIn signs activity tokens as Bot Framework does, mints reply
// tokens, and takes replies as the Teams service would.
type microsoftStandIn struct {
	key     *rsa.PrivateKey
	srv     *httptest.Server
	mu      sync.Mutex
	replies []string
}

func newMicrosoftStandIn(t *testing.T) *microsoftStandIn {
	t.Helper()
	signing.once.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
		// The key set is fetched by the verifier's own client, over plain HTTP
		// here, and outlives every test: see signing.
		keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"keys":[{"kty":"RSA","use":"sig","alg":"RS256","kid":"k1","n":%q,"e":%q,"endorsements":["msteams"]}]}`,
				b64(k.N.Bytes()), b64(big.NewInt(int64(k.E)).Bytes()))
		}))
		signing.key, signing.url = k, keys.URL
	})
	m := &microsoftStandIn{key: signing.key}
	m.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token"):
			w.Write([]byte(`{"access_token":"reply-token","expires_in":3600}`))
		default:
			b, _ := io.ReadAll(r.Body)
			m.mu.Lock()
			m.replies = append(m.replies, r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(b))
			m.mu.Unlock()
			w.Write([]byte(`{"id":"r1"}`))
		}
	}))
	v := teamsbots.Verifier()
	prevKeys, prevURL, prevClient := v.JWKSURL, teamsbots.TokenURL, teamsbots.HTTPClient
	v.JWKSURL = signing.url
	teamsbots.TokenURL = func(tenant string) string { return m.srv.URL + "/" + tenant + "/oauth2/v2.0/token" }
	teamsbots.HTTPClient = m.srv.Client()
	t.Cleanup(func() {
		m.srv.Close()
		v.JWKSURL, teamsbots.TokenURL, teamsbots.HTTPClient = prevKeys, prevURL, prevClient
	})
	return m
}

// token is an activity token for audience, naming serviceURL.
func (m *microsoftStandIn) token(t *testing.T, audience, serviceURL string) string {
	t.Helper()
	now := time.Now().Unix()
	tok, err := jwcrypt.SignRS256(m.key, map[string]interface{}{"aud": audience, "iss": "https://api.botframework.com",
		"serviceurl": serviceURL, "exp": now + 600, "nbf": now - 60, "iat": now}, map[string]string{"kid": "k1"})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// activity is a one-to-one message from Ada in tenant, replies to serviceURL.
func activity(serviceURL, tenant, id, text string) string {
	b, _ := json.Marshal(map[string]any{"type": "message", "id": id, "serviceUrl": serviceURL, "channelId": "msteams", "text": text,
		"from":         map[string]string{"id": "29:ada", "name": "Ada Lovelace", "aadObjectId": "aad-ada"},
		"conversation": map[string]string{"id": "a:conv-1", "conversationType": "personal", "tenantId": tenant}})
	return string(b)
}

// A Teams bot end to end: Microsoft's signed activity at the bot's webhook
// reaches the agent bound to teams, and the agent's answer goes back into
// the conversation signed with the bot's own reply token. A token for
// another bot is refused; a bot waiting for approval, or one an activity
// from another tenant reaches, passes nothing on.
func TestATeamsBotAnswersInTeams(t *testing.T) {
	T := linkWorld(t)
	m := newMicrosoftStandIn(t)
	const tenant = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	bot, err := teamsbots.Save(teamsbots.Bot{Owner: "alice", Name: "Wren", AppID: botApp, TenantID: tenant, ServiceHost: m.srv.URL}, "sekret", true)
	if err != nil {
		t.Fatal(err)
	}
	T.wireWebhooks()
	t.Cleanup(func() { webhooks.LinkedServices, webhooks.ToChannel, webhooks.ServiceCheck = nil, nil, nil })
	hook, err := webhooks.Create(webhooks.Webhook{Label: "Wren (Teams bot)", Owner: "alice", Verify: teamsbots.VerifyName, Feeds: "channel:teams"}, "")
	if err != nil {
		t.Fatal(err)
	}
	teamsbots.SetWebhook("alice", "teams", hook.Name)
	SaveChannel(RootDB, Channel{ID: "ch-teams", Owner: "alice", Name: "Teams", Service: "teams", AgentID: "agent-1", AutoReply: true})
	asked := make(chan string, 4)
	RegisterChannelAgentRunner(func(_ context.Context, in ChannelInbound) (ChannelReply, error) {
		asked <- in.Text
		return ChannelReply{Text: "deploy is green"}, nil
	})
	t.Cleanup(func() { RegisterChannelAgentRunner(nil) })

	svcURL := m.srv.URL + "/amer/"
	post := func(token, body string) int {
		r := httptest.NewRequest(http.MethodPost, webhooks.Prefix+hook.Name, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		webhooks.Handle(w, r)
		return w.Code
	}
	if code := post(m.token(t, botApp, svcURL), activity(svcURL, tenant, "m1", "status?")); code != http.StatusAccepted ||
		!strings.Contains(webhooks.Calls(hook.Name)[0].Outcome, "waits for an administrator's approval") {
		t.Errorf("held: %d %+v", code, webhooks.Calls(hook.Name)[0])
	}
	teamsbots.AdminSetDisabled("alice", "teams", false)
	bot, _ = teamsbots.Load("alice", "teams")

	for _, c := range []struct {
		why, token string
	}{
		{"another bot's token", m.token(t, "99999999-2222-3333-4444-555555555555", svcURL)},
		{"a token for another reply address", m.token(t, botApp, "https://elsewhere.example/amer/")},
		{"no token", ""},
	} {
		if code := post(c.token, activity(svcURL, tenant, "m2", "status?")); code != http.StatusUnauthorized {
			t.Errorf("%s: %d", c.why, code)
		}
	}
	if code := post(m.token(t, botApp, svcURL), activity(svcURL, "ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee", "m3", "status?")); code != http.StatusAccepted {
		t.Errorf("another tenant: %d", code)
	}
	select {
	case got := <-asked:
		t.Fatalf("passed on what it should not have: %q", got)
	case <-time.After(100 * time.Millisecond):
	}

	if code := post(m.token(t, botApp, svcURL), activity(svcURL, tenant, "m4", "<at>Wren</at> is the deploy done?")); code != http.StatusOK {
		t.Fatalf("a signed message: %d %+v", code, webhooks.Calls(hook.Name)[0])
	}
	select {
	case got := <-asked:
		if got != "is the deploy done?" {
			t.Errorf("the agent was asked %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the message never reached the agent")
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && T.pendingOutboxCount("teams", "alice") == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	// The shared reply loop may be running too (another test registered the
	// routes): whichever sends it, it is sent once.
	T.deliverBotOutbound(context.Background(), bot)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		n := len(m.replies)
		m.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.replies) != 1 || !strings.HasPrefix(m.replies[0], "/amer/v3/conversations/a:conv-1/activities Bearer reply-token ") ||
		!strings.Contains(m.replies[0], `"text":"deploy is green"`) {
		t.Errorf("replies: %q", m.replies)
	}
}

// A reply for a conversation another bot heard, or none has, is not sent
// with this bot's token.
func TestAReplyGoesOnlyWhereTheBotHeard(t *testing.T) {
	T := linkWorld(t)
	m := newMicrosoftStandIn(t)
	bot, _ := teamsbots.Save(teamsbots.Bot{Owner: "alice", Name: "Wren", AppID: botApp, ServiceHost: m.srv.URL}, "s", false)
	T.setBotConv("a:theirs", botConv{ServiceURL: m.srv.URL + "/amer", Connector: "bob\x00teams"})
	T.enqueueOutbox(OutboxItem{Service: "teams", Owner: "alice", ChatID: "a:theirs", Text: "x"})
	T.enqueueOutbox(OutboxItem{Service: "teams", Owner: "alice", ChatID: "a:nobody", Text: "y"})
	T.deliverBotOutbound(context.Background(), bot)
	if len(m.replies) != 0 {
		t.Errorf("sent: %q", m.replies)
	}
}

// The Bridges page's Teams bot: added, it waits for approval under the
// default policy (an administrator's does not), and its webhook is made,
// passing to the channel teams; one bot a service, and none where a linked
// service carries it; saved again it keeps its secret; its app package
// comes for the address its webhook has; deleted, its webhook goes too.
func TestTheTeamsBotOnTheBridgesPage(t *testing.T) {
	f := newOwnershipFixture(t)
	f.adb.Set(WebTable, "external_url", "https://gohort.example.test")
	T := f.T
	T.wireWebhooks()
	t.Cleanup(func() { webhooks.LinkedServices, webhooks.ToChannel, webhooks.ServiceCheck = nil, nil, nil })
	do := func(user, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(f.adb, user)})
		w := httptest.NewRecorder()
		f.routes()(w, r)
		return w.Code, w.Body.String()
	}
	form := `{"name":"Wren","app_id":"` + botApp + `","tenant_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","secret":"sekret","accept":["personal"]}`
	code, body := do("alice", "POST", "/api/teams-bots", form)
	if code >= 300 || !strings.Contains(body, "https://gohort.example.test/hook/wren-teams-bot") || !strings.Contains(body, "approval") {
		t.Fatalf("add: %d %s", code, body)
	}
	b, _ := teamsbots.Load("alice", "teams")
	h, ok := webhooks.Owned("alice", b.Webhook)
	if !b.AwaitingApproval || !ok || h.Verify != teamsbots.VerifyName || h.Feeds != "channel:teams" {
		t.Errorf("bot %+v, webhook %+v", b, h)
	}
	if code, body := do("alice", "POST", "/api/teams-bots", form); code != 400 || !strings.Contains(body, "edit it") {
		t.Errorf("a second bot for teams: %d %s", code, body)
	}
	// iMessage comes through the desktop relay, never a bot.
	if code, body := do("root", "POST", "/api/teams-bots", strings.Replace(form, `"accept"`, `"service":"imessage","accept"`, 1)); code != 400 || !strings.Contains(body, "desktop relay") {
		t.Errorf("a Teams bot named imessage: %d %s", code, body)
	}
	// One Azure Bot is added once: another person naming the same app id is
	// refused, so their bot cannot hear this one's traffic.
	if code, body := do("root", "POST", "/api/teams-bots", strings.Replace(form, `"accept"`, `"service":"teams-twin","accept"`, 1)); code != 400 || !strings.Contains(body, "added once") {
		t.Errorf("a second bot with the same app id: %d %s", code, body)
	}
	opsForm := strings.Replace(form, botApp, "22222222-2222-3333-4444-555555555555", 1)
	if code, _ := do("root", "POST", "/api/teams-bots", strings.Replace(opsForm, `"accept"`, `"service":"teams-ops","accept"`, 1)); code >= 300 {
		t.Errorf("an administrator's: %d", code)
	}
	if rb, _ := teamsbots.Load("root", "teams-ops"); rb.AwaitingApproval {
		t.Error("an administrator's bot was held")
	}
	// A new bot with nothing ticked starts with the design's defaults; one
	// edited with nothing ticked keeps "everywhere", as before.
	if code, body := do("root", "POST", "/api/teams-bots", `{"name":"Plain","app_id":"33333333-2222-3333-4444-555555555555","secret":"s","service":"teams-plain"}`); code >= 300 {
		t.Fatalf("a bot with nothing ticked: %d %s", code, body)
	}
	wantAccept, wantMention := teamsbots.NewBotDefaults()
	if pb, _ := teamsbots.Load("root", "teams-plain"); strings.Join(pb.Accept, ",") != strings.Join(wantAccept, ",") || strings.Join(pb.Mention, ",") != strings.Join(wantMention, ",") {
		t.Errorf("a new bot with nothing ticked: accept %v, mention %v", pb.Accept, pb.Mention)
	}
	if code, body := do("alice", "POST", "/api/teams-bots?service=teams", `{"name":"Wren Two","app_id":"`+botApp+`","secret":""}`); code >= 300 {
		t.Errorf("edit: %d %s", code, body)
	}
	if teamsbots.Secret("alice", "teams") != "sekret" {
		t.Error("an edit with no secret lost the one it had")
	}
	if eb, _ := teamsbots.Load("alice", "teams"); len(eb.Accept) != 0 || len(eb.Mention) != 0 {
		t.Errorf("an edit with nothing ticked took a new bot's defaults: accept %v, mention %v", eb.Accept, eb.Mention)
	}
	_, list := do("alice", "GET", "/api/teams-bots", "")
	if strings.Contains(list, "sekret") || !strings.Contains(list, "Wren Two") || !strings.Contains(list, "waiting for an administrator") {
		t.Errorf("the list: %s", list)
	}
	code, zipBody := do("alice", "GET", "/api/teams-bots/package?service=teams", "")
	if code != 200 || !strings.HasPrefix(zipBody, "PK") {
		t.Errorf("the package: %d", code)
	}
	if code, _ := do("bob", "GET", "/api/teams-bots/package?service=teams", ""); code != 404 {
		t.Errorf("someone else's package: %d", code)
	}
	if code, _ := do("alice", "DELETE", "/api/teams-bots?service=teams", ""); code >= 300 {
		t.Errorf("delete: %d", code)
	}
	if _, ok := webhooks.Get(b.Webhook); ok {
		t.Error("the bot's webhook outlived it")
	}
}

// The reply loop sends for a bot an administrator lets answer, and holds
// what a bot waiting for approval would send.
func TestOnlyAnApprovedBotReplies(t *testing.T) {
	T := linkWorld(t)
	m := newMicrosoftStandIn(t)
	bot, _ := teamsbots.Save(teamsbots.Bot{Owner: "alice", Name: "Wren", AppID: botApp, ServiceHost: m.srv.URL}, "s", true)
	T.setBotConv("a:c1", botConv{ServiceURL: m.srv.URL + "/amer", Connector: bot.Key()})
	T.enqueueOutbox(OutboxItem{Service: "teams", Owner: "alice", ChatID: "a:c1", Text: "x"})
	T.deliverTeams(context.Background())
	if len(m.replies) != 0 || T.pendingOutboxCount("teams", "alice") != 1 {
		t.Errorf("a held bot replied: %q", m.replies)
	}
	teamsbots.AdminSetDisabled("alice", "teams", false)
	T.deliverTeams(context.Background())
	if len(m.replies) != 1 {
		t.Errorf("an approved bot did not: %q", m.replies)
	}
}

// A bot added to a group chat records the conversation at once; in the group
// it answers only when named, and each message carries where its sender runs
// if the agent serves people outside: the group's own store there, the
// person's in a one-to-one chat.
func TestATeamsBotIsAddedAndAnswersWhenNamed(t *testing.T) {
	T := linkWorld(t)
	m := newMicrosoftStandIn(t)
	const tenant = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	accept, mention := teamsbots.NewBotDefaults()
	bot, err := teamsbots.Save(teamsbots.Bot{Owner: "alice", Name: "Wren", AppID: botApp, TenantID: tenant, ServiceHost: m.srv.URL, Accept: accept, Mention: mention}, "sekret", false)
	if err != nil {
		t.Fatal(err)
	}
	T.wireWebhooks()
	t.Cleanup(func() { webhooks.LinkedServices, webhooks.ToChannel, webhooks.ServiceCheck = nil, nil, nil })
	hook, err := webhooks.Create(webhooks.Webhook{Label: "Wren (Teams bot)", Owner: "alice", Verify: teamsbots.VerifyName, Feeds: "channel:teams"}, "")
	if err != nil {
		t.Fatal(err)
	}
	teamsbots.SetWebhook("alice", "teams", hook.Name)
	SaveChannel(RootDB, Channel{ID: "ch-teams", Owner: "alice", Name: "Teams", Service: "teams", AgentID: "agent-1", AutoReply: true})
	asked := make(chan ChannelInbound, 4)
	RegisterChannelAgentRunner(func(_ context.Context, in ChannelInbound) (ChannelReply, error) {
		asked <- in
		return ChannelReply{Text: "ok"}, nil
	})
	t.Cleanup(func() { RegisterChannelAgentRunner(nil) })

	svcURL := m.srv.URL + "/amer/"
	botID := "28:" + botApp
	send := func(v map[string]any) int {
		v["serviceUrl"], v["channelId"] = svcURL, "msteams"
		v["recipient"] = map[string]string{"id": botID, "name": "Wren"}
		b, _ := json.Marshal(v)
		r := httptest.NewRequest(http.MethodPost, webhooks.Prefix+hook.Name, strings.NewReader(string(b)))
		r.Header.Set("Authorization", "Bearer "+m.token(t, botApp, svcURL))
		w := httptest.NewRecorder()
		webhooks.Handle(w, r)
		return w.Code
	}
	group := map[string]string{"id": "19:group-1@thread.v2", "conversationType": "groupChat", "tenantId": tenant, "name": "Lunch crew"}
	ada := map[string]string{"id": "29:ada", "name": "Ada Lovelace", "aadObjectId": "aad-ada"}

	if code := send(map[string]any{"type": "conversationUpdate", "id": "u1", "conversation": group,
		"membersAdded": []map[string]string{{"id": botID}}}); code != http.StatusOK {
		t.Fatalf("added to a group: %d %+v", code, webhooks.Calls(hook.Name)[0])
	}
	if bc, ok := T.getBotConv(bot.Key(), "19:group-1@thread.v2"); !ok || bc.ConvType != "groupChat" || bc.Title != "Lunch crew" {
		t.Errorf("the group was not recorded when the bot was added: %+v %v", bc, ok)
	}

	send(map[string]any{"type": "message", "id": "g1", "text": "lunch anyone?", "from": ada, "conversation": group})
	send(map[string]any{"type": "message", "id": "g2", "text": "<at>Grace</at> lunch?", "from": ada, "conversation": group,
		"entities": []map[string]any{{"type": "mention", "mentioned": map[string]string{"id": "29:grace"}}}})
	select {
	case in := <-asked:
		t.Fatalf("answered in a group without being named: %q", in.Text)
	case <-time.After(150 * time.Millisecond):
	}

	send(map[string]any{"type": "message", "id": "g3", "text": "<at>Wren</at> what's the status?", "from": ada, "conversation": group,
		"entities": []map[string]any{{"type": "mention", "mentioned": map[string]string{"id": botID}}}})
	var groupScope string
	select {
	case in := <-asked:
		groupScope = in.OutsideScope
		if in.Text != "what's the status?" || !agenthost.IsOutsideScope(groupScope) || strings.HasSuffix(groupScope, ":aad-ada") {
			t.Errorf("named in a group: text %q, store %q; want the group's store", in.Text, groupScope)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("named in a group, it never answered")
	}

	send(map[string]any{"type": "message", "id": "p1", "text": "hi", "from": ada,
		"conversation": map[string]string{"id": "a:conv-1", "conversationType": "personal", "tenantId": tenant}})
	select {
	case in := <-asked:
		if want := agenthost.OutsideScope(bot.OutsideSource(), "aad-ada"); in.OutsideScope != want || in.OutsideScope == groupScope {
			t.Errorf("a one-to-one chat runs in %q, want the person's own %q", in.OutsideScope, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a one-to-one message never reached the agent")
	}

	// The owner's word on the group: every message, then not at all.
	T.DB.Set(botConvAnswerTable, answerKey(bot, "19:group-1@thread.v2"), answerAll)
	send(map[string]any{"type": "message", "id": "g4", "text": "anyone around?", "from": ada, "conversation": group})
	select {
	case in := <-asked:
		if in.Text != "anyone around?" {
			t.Errorf("asked %q", in.Text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a group set to every message did not answer an unnamed one")
	}
	T.DB.Set(botConvAnswerTable, answerKey(bot, "19:group-1@thread.v2"), answerOff)
	send(map[string]any{"type": "message", "id": "g5", "text": "<at>Wren</at> status?", "from": ada, "conversation": group,
		"entities": []map[string]any{{"type": "mention", "mentioned": map[string]string{"id": botID}}}})
	select {
	case in := <-asked:
		t.Errorf("answered in a group its owner switched it off in: %q", in.Text)
	case <-time.After(200 * time.Millisecond):
	}
}

// Two owners' bots added to the same chat each keep their own way back into
// it: the second to hear a message does not take the first one's record, and
// a record kept under the conversation alone (as before) still answers.
func TestTwoBotsInOneChatEachKeepTheirWayBack(t *testing.T) {
	T := linkWorld(t)
	m := newMicrosoftStandIn(t)
	alice, err := teamsbots.Save(teamsbots.Bot{Owner: "alice", Name: "Wren", AppID: botApp, ServiceHost: m.srv.URL}, "s", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := teamsbots.Save(teamsbots.Bot{Owner: "bob", Name: "Finch", AppID: "66666666-7777-8888-9999-000000000000", ServiceHost: m.srv.URL}, "s", false)
	if err != nil {
		t.Fatal(err)
	}
	T.setBotConv("19:shared@thread.v2", botConv{ServiceURL: m.srv.URL + "/amer", Connector: alice.Key()})
	T.setBotConv("19:shared@thread.v2", botConv{ServiceURL: m.srv.URL + "/emea", Connector: bob.Key()})
	if bc, ok := T.getBotConv(alice.Key(), "19:shared@thread.v2"); !ok || bc.ServiceURL != m.srv.URL+"/amer" {
		t.Fatalf("the first bot lost its record when the second heard the chat: %+v %v", bc, ok)
	}
	T.enqueueOutbox(OutboxItem{Service: "teams", Owner: "alice", ChatID: "19:shared@thread.v2", Text: "from wren"})
	T.deliverBotOutbound(context.Background(), alice)
	if len(m.replies) != 1 {
		t.Fatalf("the first bot's reply was not sent: %q", m.replies)
	}

	// Kept the old way, under the conversation alone: read, then moved.
	T.DB.Set(botConvTable, "a:legacy", botConv{ServiceURL: m.srv.URL + "/amer", Connector: alice.Key()})
	if _, ok := T.getBotConv(alice.Key(), "a:legacy"); !ok {
		t.Fatal("a record kept under the conversation alone is no longer read")
	}
	if _, ok := T.getBotConv(bob.Key(), "a:legacy"); ok {
		t.Fatal("another bot read a record it did not write")
	}
	T.setBotConv("a:legacy", botConv{ServiceURL: m.srv.URL + "/amer", Connector: alice.Key()})
	var left botConv
	if T.DB.Get(botConvTable, "a:legacy", &left) {
		t.Error("the old record stayed beside the new one")
	}
}

// A Teams bot's channel is fed only by its own Bot Framework webhook: one
// checked by a token or nothing is refused when made, a post through one
// made before is not passed on, and the webhook form does not offer it.
func TestOnlyTheBotsOwnWebhookFeedsItsChannel(t *testing.T) {
	T := linkWorld(t)
	m := newMicrosoftStandIn(t)
	if _, err := teamsbots.Save(teamsbots.Bot{Owner: "alice", Name: "Wren", AppID: botApp, ServiceHost: m.srv.URL}, "s", true); err != nil {
		t.Fatal(err)
	}
	T.wireWebhooks()
	t.Cleanup(func() { webhooks.LinkedServices, webhooks.ToChannel, webhooks.ServiceCheck = nil, nil, nil })
	_, err := webhooks.Create(webhooks.Webhook{Label: "spoof", Owner: "alice", Verify: webhooks.VerifyNone,
		Map: webhooks.Map{Text: "text", Chat: "chat", From: "from", ID: "id"}, Feeds: "channel:teams"}, "")
	if err == nil || !strings.Contains(err.Error(), "its own webhook") {
		t.Fatalf("a webhook with no Bot Framework check was made to feed the bot: %v", err)
	}
	if _, err := webhooks.Create(webhooks.Webhook{Label: "Wren (Teams bot)", Owner: "alice", Verify: teamsbots.VerifyName, Feeds: "channel:teams"}, ""); err != nil {
		t.Fatalf("the bot's own webhook was refused: %v", err)
	}
	if got := webhooks.ServiceCheck("alice", "teams"); got != teamsbots.VerifyName {
		t.Errorf("the bot's service names check %q", got)
	}
	if got := webhooks.ServiceCheck("alice", "acme"); got != "" {
		t.Errorf("a service with no bot names check %q", got)
	}
}

// Bridges switched off send nothing; the reply waits and goes once they are
// on again. One held past botReplyMaxAge is dropped, not sent late.
func TestATeamsReplyWaitsWhileBridgesAreOffAndNotForever(t *testing.T) {
	T := linkWorld(t)
	m := newMicrosoftStandIn(t)
	bot, err := teamsbots.Save(teamsbots.Bot{Owner: "alice", Name: "Wren", AppID: botApp, ServiceHost: m.srv.URL}, "s", false)
	if err != nil {
		t.Fatal(err)
	}
	T.setBotConv("a:c1", botConv{ServiceURL: m.srv.URL + "/amer", Connector: bot.Key()})
	T.setConfig(bridgesConfig{Enabled: false})
	T.enqueueOutbox(OutboxItem{Service: "teams", Owner: "alice", ChatID: "a:c1", Text: "held"})
	T.deliverTeams(context.Background())
	if len(m.replies) != 0 {
		t.Fatalf("sent with bridges off: %q", m.replies)
	}
	T.setConfig(bridgesConfig{Enabled: true})
	T.deliverTeams(context.Background())
	if len(m.replies) != 1 {
		t.Fatalf("the held reply did not go once bridges were on: %q", m.replies)
	}
	T.enqueueOutbox(OutboxItem{Service: "teams", Owner: "alice", ChatID: "a:c1", Text: "stale",
		Created: time.Now().Add(-2 * botReplyMaxAge).UTC().Format(time.RFC3339)})
	T.deliverTeams(context.Background())
	if len(m.replies) != 1 {
		t.Errorf("a reply held past its age was sent: %q", m.replies)
	}
}

// A bot can sign in with a certificate instead of a secret. Added that way
// with nothing pasted, it gets one made here and is told to upload it; Edit
// shows the certificate and offers its public part, which holds no key; a
// pasted certificate that cannot sign in is refused before anything is
// saved, and one that can replaces what it had.
func TestATeamsBotSignsInWithACertificate(t *testing.T) {
	f := newOwnershipFixture(t)
	f.adb.Set(WebTable, "external_url", "https://gohort.example.test")
	T := f.T
	T.wireWebhooks()
	t.Cleanup(func() { webhooks.LinkedServices, webhooks.ToChannel, webhooks.ServiceCheck = nil, nil, nil })
	do := func(user, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(f.adb, user)})
		w := httptest.NewRecorder()
		f.routes()(w, r)
		return w.Code, w.Body.String()
	}
	form := `{"name":"Helper","app_id":"` + botApp + `","tenant_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","sign_in":"certificate","accept":["personal"]}`
	code, body := do("alice", "POST", "/api/teams-bots", form)
	if code >= 300 || !strings.Contains(body, "A certificate was made") {
		t.Fatalf("add with a certificate: %d %s", code, body)
	}
	made, ok := teamsbots.Certificate("alice", "teams")
	if b, _ := teamsbots.Load("alice", "teams"); !ok || !b.UsesCertificate() {
		t.Fatalf("bot %+v, certificate %v", b, ok)
	}
	_, edit := do("alice", "GET", "/api/teams-bots?service=teams", "")
	if !strings.Contains(edit, made.ThumbprintSHA1()) || !strings.Contains(edit, "/bridges/api/teams-bots/certificate?service=teams") ||
		!strings.Contains(edit, `"sign_in":"certificate"`) {
		t.Errorf("the edit form does not show the certificate: %s", edit)
	}
	code, pub := do("alice", "GET", "/api/teams-bots/certificate?service=teams", "")
	if code != 200 || !strings.Contains(pub, "BEGIN CERTIFICATE") || strings.Contains(pub, "PRIVATE KEY") {
		t.Errorf("the public certificate: %d %s", code, pub)
	}
	if code, _ := do("bob", "GET", "/api/teams-bots/certificate?service=teams", ""); code != 404 {
		t.Errorf("someone else's bot's certificate: %d", code)
	}
	// Saved again with nothing pasted, it keeps the certificate it has.
	if code, body := do("alice", "POST", "/api/teams-bots?service=teams", form); code >= 300 || strings.Contains(body, "A certificate was made") {
		t.Errorf("saved again: %d %s", code, body)
	}
	if again, _ := teamsbots.Certificate("alice", "teams"); again.Thumbprint() != made.Thumbprint() {
		t.Error("saving again replaced the certificate")
	}
	// A pasted certificate with no key is refused, and the bot keeps its own.
	ours, _ := clientcert.Generate("ours", 0)
	bad, _ := json.Marshal(ours.CertificatePEM())
	if code, body := do("alice", "POST", "/api/teams-bots?service=teams", strings.Replace(form, `"accept"`, `"certificate":`+string(bad)+`,"accept"`, 1)); code != 400 || !strings.Contains(body, "no private key") {
		t.Errorf("a certificate without its key: %d %s", code, body)
	}
	good, _ := json.Marshal(ours.PEM())
	if code, body := do("alice", "POST", "/api/teams-bots?service=teams", strings.Replace(form, `"accept"`, `"certificate":`+string(good)+`,"accept"`, 1)); code >= 300 {
		t.Fatalf("a pasted certificate: %d %s", code, body)
	}
	if now, _ := teamsbots.Certificate("alice", "teams"); now.Thumbprint() != ours.Thumbprint() {
		t.Error("the pasted certificate is not the one it signs in with")
	}
	// Asked for a new one, it makes one.
	if code, body := do("alice", "POST", "/api/teams-bots?service=teams", strings.Replace(form, `"accept"`, `"new_certificate":true,"accept"`, 1)); code >= 300 || !strings.Contains(body, "A certificate was made") {
		t.Errorf("a new certificate: %d %s", code, body)
	}
	if now, _ := teamsbots.Certificate("alice", "teams"); now.Thumbprint() == ours.Thumbprint() {
		t.Error("asking for a new certificate kept the old one")
	}
}
