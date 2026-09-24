package bridges

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// ownershipFixture is a deployment with two ordinary users (alice, bob) and an
// admin (root), a Bridges app on its own store, and alice's conversation plus a
// legacy ownerless one recorded before ownership existed.
type ownershipFixture struct {
	T   *Bridges
	adb Database
}

func newOwnershipFixture(t *testing.T) *ownershipFixture {
	t.Helper()
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })

	T := &Bridges{AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	T.saveConvo(Convo{ChatID: "chat-alice", Service: "imessage", Owner: "alice", Added: true, DisplayName: "Alice's friend"})
	T.storeMessage(StoredMessage{ID: "m1", ChatID: "chat-alice", Role: "user", Text: "alice's secret"})
	T.saveConvo(Convo{ChatID: "chat-legacy", Service: "imessage", Added: true})
	T.storeMessage(StoredMessage{ID: "m2", ChatID: "chat-legacy", Role: "user", Text: "old thread"})
	return &ownershipFixture{T: T, adb: adb}
}

// call runs one handler as user and returns the recorder.
func (f *ownershipFixture) call(user, method, path, body string, h http.HandlerFunc) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(f.adb, user)})
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// listed reports whether the conversation list handed user contains chatID.
func (f *ownershipFixture) listed(t *testing.T, user, chatID string) bool {
	t.Helper()
	w := f.call(user, http.MethodGet, "/api/conversations", "", f.T.handleConversations)
	var rows []struct {
		ChatID string `json:"chat_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("list as %s: %v (%s)", user, err, w.Body.String())
	}
	for _, r := range rows {
		if r.ChatID == chatID {
			return true
		}
	}
	return false
}

// Any user with the app grant could read, edit, connect and delete any
// conversation by its chat id. Each of those paths now answers another user's
// chat exactly as it answers a missing one.
func TestBobCannotReachAlicesConversation(t *testing.T) {
	f := newOwnershipFixture(t)

	if w := f.call("alice", http.MethodGet, "/api/messages/chat-alice", "", f.T.handleMessages); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "alice's secret") {
		t.Fatalf("alice reading her own thread: %d %s", w.Code, w.Body.String())
	}
	if !f.listed(t, "alice", "chat-alice") {
		t.Fatal("alice's own conversation missing from her list")
	}

	denied := []struct {
		name   string
		method string
		path   string
		body   string
		h      http.HandlerFunc
	}{
		{"messages", http.MethodGet, "/api/messages/chat-alice", "", f.T.handleMessages},
		{"conv-info", http.MethodGet, "/api/conv-info/chat-alice", "", f.T.handleConvInfo},
		{"rename", http.MethodPatch, "/api/conversation/chat-alice", `{"display_name":"mine now"}`, f.T.handleConvUpdate},
		{"delete", http.MethodDelete, "/api/conversation/chat-alice", "", f.T.handleConvUpdate},
		{"add", http.MethodPost, "/api/add-convo?chat_id=chat-alice", "", f.T.handleAddConvo},
		{"add-by-handle", http.MethodPost, "/api/add-convo", `{"handle":"chat-alice"}`, f.T.handleAddConvo},
		{"connect", http.MethodPost, "/api/connect-channel?chat_id=chat-alice", "", f.T.handleConnectChannel},
	}
	for _, d := range denied {
		w := f.call("bob", d.method, d.path, d.body, d.h)
		if w.Code != http.StatusNotFound {
			t.Errorf("bob %s on alice's chat: got %d, want 404 (%s)", d.name, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "secret") {
			t.Errorf("bob %s leaked alice's thread: %s", d.name, w.Body.String())
		}
	}
	if f.listed(t, "bob", "chat-alice") {
		t.Error("alice's conversation shows in bob's list")
	}
	c, ok := f.T.getConvo("chat-alice")
	if !ok || c.DisplayName != "Alice's friend" || c.Owner != "alice" {
		t.Fatalf("bob's requests changed alice's conversation: ok=%v %+v", ok, c)
	}

	// The agent-facing seam is scoped the same way.
	ct := channelThreadsImpl{T: f.T}
	for _, th := range ct.Threads("bob") {
		if th.ChatID == "chat-alice" {
			t.Error("alice's chat in bob's agent thread list")
		}
	}
	if msgs := ct.Messages("bob", "chat-alice", 10); len(msgs) != 0 {
		t.Errorf("bob's agent read alice's thread: %+v", msgs)
	}
	if err := ct.Deliver("bob", "imessage", "chat-alice", "", "hi from bob", "", nil); err == nil {
		t.Error("bob's agent sent into alice's conversation")
	}
}

// A conversation recorded before ownership existed is the deployment admin's.
func TestLegacyOwnerlessConversationIsAdminOnly(t *testing.T) {
	f := newOwnershipFixture(t)

	if w := f.call("bob", http.MethodGet, "/api/messages/chat-legacy", "", f.T.handleMessages); w.Code != http.StatusNotFound {
		t.Errorf("non-admin read a legacy conversation: %d", w.Code)
	}
	if f.listed(t, "bob", "chat-legacy") {
		t.Error("legacy conversation listed for a non-admin")
	}
	if w := f.call("root", http.MethodGet, "/api/messages/chat-legacy", "", f.T.handleMessages); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "old thread") {
		t.Errorf("admin could not read a legacy conversation: %d %s", w.Code, w.Body.String())
	}
	if !f.listed(t, "root", "chat-legacy") {
		t.Error("legacy conversation missing from the admin's list")
	}
	// Admin standing reaches the legacy record, not another user's owned one.
	if w := f.call("root", http.MethodGet, "/api/messages/chat-alice", "", f.T.handleMessages); w.Code != http.StatusNotFound {
		t.Errorf("admin read alice's owned conversation: %d", w.Code)
	}
	// Viewing does not rewrite the legacy record's owner.
	if c, _ := f.T.getConvo("chat-legacy"); c.Owner != "" {
		t.Errorf("legacy conversation was stamped on read: %q", c.Owner)
	}
}

// A key's poll drained every item for its service, whoever queued it.
func TestPollDrainsOnlyTheKeyOwnersOutbox(t *testing.T) {
	f := newOwnershipFixture(t)
	f.T.saveBridgeKey(BridgeKey{ID: "ka", Key: "secret-alice", Owner: "alice", Service: "imessage", Enabled: true})
	f.T.saveBridgeKey(BridgeKey{ID: "kb", Key: "secret-bob", Owner: "bob", Service: "imessage", Enabled: true})
	f.T.saveBridgeKey(BridgeKey{ID: "kr", Key: "secret-root", Owner: "root", Service: "imessage", Enabled: true})

	f.T.enqueueOutbox(OutboxItem{ID: "o-alice", ChatID: "chat-alice", Service: "imessage", Text: "for alice's contact", Owner: "alice"})
	// Queued before outbound carried an owner.
	f.T.DB.Set(outboxTable, "o-legacy", OutboxItem{ID: "o-legacy", ChatID: "chat-legacy", Service: "imessage", Text: "old", Created: now()})

	poll := func(secret string) ([]OutboxItem, string) {
		r := httptest.NewRequest(http.MethodGet, "/api/poll", nil)
		r.Header.Set("X-API-Key", secret)
		w := httptest.NewRecorder()
		f.T.handlePoll(w, r)
		var items []OutboxItem
		if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
			t.Fatalf("poll: %v (%s)", err, w.Body.String())
		}
		return items, w.Body.String()
	}

	if items, _ := poll("secret-bob"); len(items) != 0 {
		t.Fatalf("bob's key drained other users' outbound: %+v", items)
	}
	items, raw := poll("secret-alice")
	if len(items) != 1 || items[0].ID != "o-alice" {
		t.Fatalf("alice's key: got %+v", items)
	}
	if strings.Contains(raw, `"alice"`) {
		t.Errorf("owner reached the connector: %s", raw)
	}
	items, _ = poll("secret-root")
	if len(items) != 1 || items[0].ID != "o-legacy" {
		t.Fatalf("admin's key should take the ownerless item and only it: %+v", items)
	}
}

// Inbound stamps the key's owner on first save, and another user's key cannot
// write into a conversation that is already someone's.
func TestInboundStampsOwnerAndRefusesAnotherUsersKey(t *testing.T) {
	f := newOwnershipFixture(t)
	alice := BridgeKey{Owner: "alice", Service: "imessage", Name: "alice-mac"}
	bob := BridgeKey{Owner: "bob", Service: "imessage", Name: "bob-mac"}

	f.T.ingestInbound(alice, hookRequest{ChatID: "chat-new", Handle: "+15550100", Text: "hello", MsgID: "n1"})
	c, ok := f.T.getConvo("chat-new")
	if !ok || c.Owner != "alice" {
		t.Fatalf("first inbound did not stamp the key's owner: ok=%v %+v", ok, c)
	}

	f.T.ingestInbound(bob, hookRequest{ChatID: "chat-new", Handle: "+15550199", Text: "injected", MsgID: "n2"})
	for _, m := range f.T.recentMessages("chat-new", 0) {
		if strings.Contains(m.Text, "injected") {
			t.Fatal("bob's key wrote into alice's conversation")
		}
	}
	if c, _ := f.T.getConvo("chat-new"); c.Owner != "alice" || len(c.Members) != 1 {
		t.Fatalf("bob's inbound changed alice's conversation: %+v", c)
	}

	// A generic api key, which any user may mint, does not claim a legacy
	// conversation by naming its chat id.
	f.T.ingestInbound(BridgeKey{Owner: "bob", Service: "api"}, hookRequest{ChatID: "chat-legacy", Text: "claim", MsgID: "n3"})
	if c, _ := f.T.getConvo("chat-legacy"); c.Owner != "" {
		t.Fatalf("an api key claimed a legacy conversation: %q", c.Owner)
	}
}
