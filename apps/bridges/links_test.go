package bridges

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/snugforge/kvlite"
)

// chatAPI is a messaging service as a connector sees it: what it holds to
// fetch, what was sent, and whether sending fails.
type chatAPI struct {
	mu       sync.Mutex
	messages []map[string]string
	cursor   string
	fetches  []string
	sent     []string
	failSend bool
}

func (a *chatAPI) add(m map[string]string) {
	a.mu.Lock()
	a.messages = append(a.messages, m)
	a.mu.Unlock()
}

func (a *chatAPI) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			a.fetches = append(a.fetches, r.URL.RawQuery)
			json.NewEncoder(w).Encode(map[string]any{"messages": a.messages, "cursor": a.cursor})
			return
		}
		if a.failSend {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		b, _ := io.ReadAll(r.Body)
		a.sent = append(a.sent, string(b))
		w.Write([]byte(`{"id":"s1"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// chatConnector keeps a service connector at srv giving fetch and send,
// and, with fetchOnly, one giving fetch alone.
func chatConnector(t *testing.T, name, base string, fetchOnly bool) {
	t.Helper()
	provides := []any{map[string]any{"capability": "message.fetch", "steps": []any{map[string]any{"call": "fetch"}},
		"output": map[string]any{
			"messages": map[string]any{"path": "messages", "fields": map[string]any{"id": "id", "chat": "chat", "from": "from", "text": "text", "at": "at"}},
			"cursor":   map[string]any{"path": "cursor"},
		}}}
	if !fetchOnly {
		provides = append(provides, map[string]any{"capability": "message.send", "steps": []any{map[string]any{"call": "send"}},
			"output": map[string]any{"id": map[string]any{"path": "id"}}})
	}
	spec, _ := json.Marshal(map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": base, "auth": map[string]any{"none": true},
			"operations": map[string]any{
				"fetch": map[string]any{"path": "/fetch?chat={chat}&since={since}"},
				"send":  map[string]any{"method": "POST", "path": "/send", "body": map[string]any{"to": "{to}", "text": "{text}"}},
			}},
		"provides": provides,
	})
	if err := SaveConnector(RootDB, Connector{Name: name, Kind: service.Kind, Spec: spec}); err != nil {
		t.Fatal(err)
	}
}

func linkWorld(t *testing.T) *Bridges {
	t.Helper()
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	auth := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	T := &Bridges{AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	T.setConfig(bridgesConfig{Enabled: true})
	return T
}

func lastOutcome(t *testing.T, l serviceLink) string {
	t.Helper()
	st, _ := lastLink(l.key())
	return st.Outcome
}

// A linked service talks through its connector: its first poll passes over
// what is there, the next passes on what is new (asking from the cursor it
// was given) to the channel bound to the service, and the agent's answer
// goes out through the connector's send, to the chat it came from.
func TestALinkedServiceTalksThroughItsConnector(t *testing.T) {
	T := linkWorld(t)
	api := &chatAPI{cursor: "c1"}
	chatConnector(t, "acme-chat", api.serve(t).URL, false)
	api.add(map[string]string{"id": "m1", "chat": "room-1", "from": "u1", "text": "old news", "at": "1700000000.1"})
	SaveChannel(RootDB, Channel{ID: "ch-acme", Owner: "alice", Name: "Acme", Service: "acme", AgentID: "agent-1", AutoReply: true})
	var asked []string
	var mu sync.Mutex
	RegisterChannelAgentRunner(func(_ context.Context, in ChannelInbound) (ChannelReply, error) {
		mu.Lock()
		asked = append(asked, in.Text)
		mu.Unlock()
		return ChannelReply{Text: "pong"}, nil
	})
	t.Cleanup(func() { RegisterChannelAgentRunner(nil) })

	l, err := T.storeLink(serviceLink{Owner: "alice", Service: "acme", Connector: "acme-chat"})
	if err != nil {
		t.Fatal(err)
	}
	T.pollLink(context.Background(), l)
	if got := lastOutcome(t, l); !strings.HasPrefix(got, "connected: 1 earlier") {
		t.Errorf("the first poll: %q", got)
	}
	api.add(map[string]string{"id": "m2", "chat": "room-1", "from": "u2", "text": "ping", "at": time.Now().UTC().Format(time.RFC3339)})
	api.cursor = "c2"
	T.pollLink(context.Background(), l)
	if api.fetches[1] != "chat=&since=c1" {
		t.Errorf("the second poll did not ask from the cursor: %q", api.fetches)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := len(asked)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	if strings.Join(asked, "|") != "ping" {
		t.Errorf("the agent was asked %q", asked)
	}
	mu.Unlock()
	for time.Now().Before(deadline) && T.pendingOutboxCount("acme", "alice") == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	T.pollLink(context.Background(), l)
	if len(api.sent) != 1 || !strings.Contains(api.sent[0], `"to":"room-1"`) || !strings.Contains(api.sent[0], "pong") {
		t.Errorf("sent: %q", api.sent)
	}
	if !T.serviceHasOutput("alice", "acme") {
		t.Error("a linked service has a way out")
	}
	if got := lastOutcome(t, l); got != "0 new, 1 sent" {
		t.Errorf("the last poll: %q", got)
	}
}

// A reply that cannot be sent is kept for the next poll, and the row says so.
func TestAReplyNotSentIsKept(t *testing.T) {
	T := linkWorld(t)
	api := &chatAPI{failSend: true}
	chatConnector(t, "acme-chat", api.serve(t).URL, false)
	l, _ := T.storeLink(serviceLink{Owner: "alice", Service: "acme", Connector: "acme-chat"})
	T.enqueueOutbox(OutboxItem{Service: "acme", Owner: "alice", ChatID: "room-1", Text: "hello"})
	T.pollLink(context.Background(), l)
	if T.pendingOutboxCount("acme", "alice") != 1 || !strings.Contains(lastOutcome(t, l), "could not send a reply") {
		t.Errorf("kept: %d, %q", T.pendingOutboxCount("acme", "alice"), lastOutcome(t, l))
	}
	api.failSend = false
	T.pollLink(context.Background(), l)
	if T.pendingOutboxCount("acme", "alice") != 0 || len(api.sent) != 1 {
		t.Errorf("sent on the next poll: %q", api.sent)
	}
}

// Messages that do not say their chat, from a poll naming none, are not
// guessed at: the row says to name the chats.
func TestMessagesWithNoChatAreNotGuessed(t *testing.T) {
	T := linkWorld(t)
	api := &chatAPI{}
	chatConnector(t, "acme-chat", api.serve(t).URL, false)
	api.add(map[string]string{"id": "m1", "text": "where am I"})
	l, _ := T.storeLink(serviceLink{Owner: "alice", Service: "acme", Connector: "acme-chat"})
	T.pollLink(context.Background(), l)
	if got := lastOutcome(t, l); !strings.Contains(got, "name the chats") {
		t.Errorf("%q", got)
	}
	l.Chats = []string{"room-9"}
	T.pollLink(context.Background(), l)
	if got := lastOutcome(t, l); !strings.HasPrefix(got, "connected") || api.fetches[1] != "chat=room-9&since=" {
		t.Errorf("with a chat named: %q %q", got, api.fetches)
	}
}

// What a link may be: a service name in its shape, not iMessage, not one
// another transport carries for the person; a connector that both fetches
// and sends, which the person may reach; a poll no more often than the
// floor, over no more than the chats it takes. Unlinking takes its cursors.
func TestWhatALinkMayBe(t *testing.T) {
	T := linkWorld(t)
	api := &chatAPI{}
	base := api.serve(t).URL
	chatConnector(t, "acme-chat", base, false)
	chatConnector(t, "acme-read", base, true)
	chatConnector(t, "bobs-chat", base, false)
	bobs, _ := GetConnector(RootDB, "bobs-chat")
	bobs.SetAccess(RootDB, []string{"bob"})
	T.DB.Set(bridgeKeysTable, "k1", BridgeKey{ID: "k1", Name: "relay", Owner: "alice", Service: "sms", Enabled: true})
	many := make([]string, linkMaxChats+1)
	for i := range many {
		many[i] = "c" + strings.Repeat("x", i+1)
	}
	for _, c := range []struct {
		l    serviceLink
		want string
	}{
		{serviceLink{Service: "Acme Chat", Connector: "acme-chat"}, "lowercase"},
		{serviceLink{Service: "imessage", Connector: "acme-chat"}, "desktop relay"},
		{serviceLink{Service: "sms", Connector: "acme-chat"}, "already carries sms"},
		{serviceLink{Service: "acme", Connector: "acme-read"}, "both fetch and send"},
		{serviceLink{Service: "acme", Connector: "nope"}, "no connector"},
		{serviceLink{Service: "acme", Connector: "bobs-chat"}, "limited to named users"},
		{serviceLink{Service: "acme", Connector: "acme-chat", IntervalSecs: 5}, "every 15 seconds"},
		{serviceLink{Service: "acme", Connector: "acme-chat", Chats: many}, "at most"},
	} {
		c.l.Owner = "alice"
		if _, err := T.storeLink(c.l); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("want %q, got %v", c.want, err)
		}
	}
	l, err := T.storeLink(serviceLink{Owner: "alice", Service: " Acme ", Connector: "acme-chat", Chats: []string{"a", " a ", "b"}})
	if err != nil || l.Service != "acme" || strings.Join(l.Chats, ",") != "a,b" {
		t.Fatalf("%+v %v", l, err)
	}
	T.DB.Set(linkCursorTable, l.cursorKey("a"), "c9")
	if len(T.linksOf("alice")) != 1 || len(T.linksOf("bob")) != 0 {
		t.Error("listed for the wrong person")
	}
	if T.removeLink("bob", "acme") == nil {
		t.Error("someone else unlinked it")
	}
	if err := T.removeLink("alice", "acme"); err != nil || len(T.DB.Keys(linkCursorTable)) != 0 {
		t.Errorf("unlinked: %v, cursors %v", err, T.DB.Keys(linkCursorTable))
	}
}

// A time as a link reads it: seconds since 1970 or RFC3339; anything else
// is left out.
func TestSentAt(t *testing.T) {
	for in, want := range map[string]string{
		"1700000000.000100":         "2023-11-14T22:13:20Z",
		"2026-11-01T10:00:00+02:00": "2026-11-01T08:00:00Z",
		"yesterday":                 "",
		"42":                        "",
	} {
		if got := sentAt(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// A link is polled as its owner: a connector later limited to others is
// not reached through it, and the row says why. A service a link carries
// has a way out even when another connector for it, added later, sends
// nothing.
func TestALinkIsPolledAsItsOwner(t *testing.T) {
	T := linkWorld(t)
	api := &chatAPI{}
	chatConnector(t, "acme-chat", api.serve(t).URL, false)
	l, err := T.storeLink(serviceLink{Owner: "alice", Service: "acme", Connector: "acme-chat"})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := GetConnector(RootDB, "acme-chat")
	c.SetAccess(RootDB, []string{"bob"})
	T.pollLink(context.Background(), l)
	if got := lastOutcome(t, l); !strings.Contains(got, "limited to named users") || len(api.fetches) != 0 {
		t.Errorf("%q, fetched %d", got, len(api.fetches))
	}
	RootDB.Set("connectors", "acme-push", Connector{Name: "acme-push", Kind: RestMessagingConnectorKind, Owner: "alice", Approved: true,
		Spec: json.RawMessage(`{"service":"acme"}`)})
	if !T.serviceHasOutput("alice", "acme") {
		t.Error("the link's way out was lost to a connector that sends nothing")
	}
}
