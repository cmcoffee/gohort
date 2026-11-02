package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// world is a store of its own, fresh limits, and the wakes the watches got.
func world(t *testing.T) chan string {
	t.Helper()
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return RootDB }
	prevPosts, prevFails, prevFires := posts, failures, EventMonitorFires
	posts, failures = NewRateLimiter(perMinute, time.Minute), NewRateLimiter(failMax, time.Minute)
	EventMonitorFires = NewRateLimiter(EventMonitorFiresPerMinute, time.Minute)
	recent.Lock()
	recent.ids = map[string][]string{}
	recent.Unlock()
	woke := make(chan string, 16)
	RegisterEventWaker(func(_ context.Context, owner, monitor, summary string) (bool, string) {
		woke <- owner + "/" + monitor + ": " + summary
		return true, ""
	})
	t.Cleanup(func() {
		RootDB, AuthDB = prevRoot, prevAuth
		posts, failures, EventMonitorFires = prevPosts, prevFails, prevFires
		RegisterEventWaker(nil)
	})
	return woke
}

// watch is owner's watch woken by a post.
func watch(owner, name string) {
	SaveEventMonitor(RootDB, EventMonitor{Name: name, Owner: owner, Kind: EventKindWebhook, Token: NewEventToken()})
}

// post sends body to webhook name with headers, and gives the status.
func post(t *testing.T, name, body string, headers map[string]string) int {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, Prefix+name, strings.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	Handle(w, r)
	return w.Code
}

func woken(t *testing.T, woke chan string) string {
	t.Helper()
	select {
	case s := <-woke:
		return s
	case <-time.After(2 * time.Second):
		return ""
	}
}

func quiet(t *testing.T, woke chan string) {
	t.Helper()
	select {
	case s := <-woke:
		t.Errorf("woke when it should not have: %s", s)
	case <-time.After(100 * time.Millisecond):
	}
}

// A GitHub-style webhook: an HMAC SHA-256 signature after "sha256=", its
// kind and id in headers. A signed push wakes the watch with the commit
// message; the same delivery again is passed over; a kind it does not pass
// on is accepted and passed over; a wrong signature, or none, is refused
// and said on its row.
func TestASignedEventWakesItsWatch(t *testing.T) {
	woke := world(t)
	watch("alice", "repo-events")
	h, err := Create(Webhook{Label: "GitHub pushes", Owner: "alice", Verify: VerifyHMAC, Header: "X-Hub-Signature-256", Prefix: "sha256=",
		Map:   Map{Text: "head_commit.message", Kind: "header:X-GitHub-Event", ID: "header:X-GitHub-Delivery", From: "sender.login"},
		Kinds: []string{"push"}, Feeds: "watch:repo-events"}, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if h.Name != "github-pushes" || h.Reach != ReachDirect || h.Algorithm != "sha256" {
		t.Fatalf("made: %+v", h)
	}
	body := `{"head_commit":{"message":"fix the build"},"sender":{"login":"octo"}}`
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(body))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	headers := map[string]string{"X-Hub-Signature-256": sig, "X-GitHub-Event": "push", "X-GitHub-Delivery": "d-1"}

	if code := post(t, h.Name, body, headers); code != http.StatusNoContent {
		t.Fatalf("signed push: %d", code)
	}
	if got := woken(t, woke); got != "alice/repo-events: GitHub pushes: push, from octo\n\nfix the build" {
		t.Errorf("woke with %q", got)
	}
	if code := post(t, h.Name, body, headers); code != http.StatusOK {
		t.Errorf("the same delivery again: %d", code)
	}
	quiet(t, woke)
	star := map[string]string{"X-Hub-Signature-256": sig, "X-GitHub-Event": "star", "X-GitHub-Delivery": "d-2"}
	if code := post(t, h.Name, body, star); code != http.StatusAccepted {
		t.Errorf("a kind it does not pass on: %d", code)
	}
	quiet(t, woke)
	for _, bad := range []map[string]string{
		{"X-Hub-Signature-256": "sha256=" + strings.Repeat("0", 64), "X-GitHub-Event": "push", "X-GitHub-Delivery": "d-3"},
		{"X-Hub-Signature-256": strings.TrimPrefix(sig, "sha256="), "X-GitHub-Event": "push", "X-GitHub-Delivery": "d-4"},
		{"X-GitHub-Event": "push", "X-GitHub-Delivery": "d-5"},
	} {
		if code := post(t, h.Name, body, bad); code != http.StatusUnauthorized {
			t.Errorf("%v: %d", bad, code)
		}
	}
	quiet(t, woke)
	if code := post(t, h.Name, body+" ", headers); code != http.StatusUnauthorized {
		t.Errorf("a body changed after signing: %d", code)
	}
	calls := Calls(h.Name)
	if len(calls) != 7 || !strings.HasPrefix(calls[0].Outcome, "refused: its signature does not match") ||
		!strings.Contains(calls[1].Outcome, "came with no signature") || calls[6].Outcome != "woke repo-events" ||
		calls[5].Outcome != "passed over: an event it already passed on" || calls[4].Outcome != "passed over: a kind it does not pass on" {
		for _, c := range calls {
			t.Log(c.Outcome)
		}
		t.Error("its calls do not say what happened")
	}
}

// A signature in base64 (Shopify), or SHA-1 in hex in any case, reads too.
func TestSignaturesInTheirOtherShapes(t *testing.T) {
	woke := world(t)
	watch("alice", "orders")
	shop, _ := Create(Webhook{Label: "Shop", Owner: "alice", Verify: VerifyHMAC, Header: "X-Shopify-Hmac-Sha256", Feeds: "watch:orders"}, "k1")
	old, _ := Create(Webhook{Label: "Old", Owner: "alice", Verify: VerifyHMAC, Header: "X-Signature", Algorithm: "sha1", Feeds: "watch:orders"}, "k2")
	body := `{"id":1}`
	m256 := hmac.New(sha256.New, []byte("k1"))
	m256.Write([]byte(body))
	m1 := hmac.New(sha1.New, []byte("k2"))
	m1.Write([]byte(body))
	if code := post(t, shop.Name, body, map[string]string{"X-Shopify-Hmac-Sha256": base64.StdEncoding.EncodeToString(m256.Sum(nil))}); code != http.StatusNoContent {
		t.Errorf("base64: %d", code)
	}
	woken(t, woke)
	if code := post(t, old.Name, body, map[string]string{"X-Signature": strings.ToUpper(hex.EncodeToString(m1.Sum(nil)))}); code != http.StatusNoContent {
		t.Errorf("sha1 in upper case: %d", code)
	}
	if got := woken(t, woke); !strings.HasSuffix(got, "\n\n"+body) {
		t.Errorf("with no text path, the body: %q", got)
	}
}

// A token in a header, or as a bearer in Authorization; a wrong one is
// refused. One that checks nothing has a long random address.
func TestTokensAndTheOpenAddress(t *testing.T) {
	woke := world(t)
	watch("alice", "alerts")
	tok, _ := Create(Webhook{Label: "Alerts", Owner: "alice", Verify: VerifyToken, Header: "X-Token", Map: Map{Text: "msg"}, Feeds: "watch:alerts"}, "tkn")
	bearer, _ := Create(Webhook{Label: "Alerts", Owner: "alice", Verify: VerifyToken, Header: "Authorization", Map: Map{Text: "msg"}, Feeds: "watch:alerts"}, "tkn2")
	if bearer.Name != "alerts-2" {
		t.Errorf("a second of the same name: %s", bearer.Name)
	}
	if post(t, tok.Name, `{"msg":"disk full"}`, map[string]string{"X-Token": "tkn"}) != http.StatusNoContent || woken(t, woke) == "" {
		t.Error("the token")
	}
	if post(t, bearer.Name, `{"msg":"x"}`, map[string]string{"Authorization": "Bearer tkn2"}) != http.StatusNoContent || woken(t, woke) == "" {
		t.Error("a bearer token")
	}
	for _, h := range []map[string]string{{"X-Token": "tkn2"}, {"X-Token": ""}, {"Authorization": "Bearer tkn"}} {
		name := tok.Name
		if _, ok := h["Authorization"]; ok {
			name = bearer.Name
		}
		if code := post(t, name, `{"msg":"x"}`, h); code != http.StatusUnauthorized {
			t.Errorf("%v: %d", h, code)
		}
	}
	quiet(t, woke)
	open, err := Create(Webhook{Label: "Lan tool", Owner: "alice", Verify: VerifyNone, Feeds: "watch:alerts"}, "")
	if err != nil || !strings.HasPrefix(open.Name, "lan-tool-") || len(open.Name) < len("lan-tool-")+32 {
		t.Errorf("open: %q %v", open.Name, err)
	}
	if post(t, open.Name, `ping`, nil) != http.StatusNoContent || woken(t, woke) == "" {
		t.Error("an open webhook")
	}
}

// What it passes on goes only to its watch while the watch takes it: one
// gone or paused is not woken, and the watch's own budget, shared with its
// token URL, bounds both doors.
func TestTheWatchDecides(t *testing.T) {
	woke := world(t)
	watch("alice", "w")
	h, _ := Create(Webhook{Label: "W", Owner: "alice", Verify: VerifyNone, Feeds: "watch:w"}, "")
	m, _ := GetEventMonitor(RootDB, "alice", "w")
	m.Paused = true
	SaveEventMonitor(RootDB, m)
	if post(t, h.Name, "a", nil) != http.StatusAccepted {
		t.Error("paused")
	}
	quiet(t, woke)
	m.Paused = false
	SaveEventMonitor(RootDB, m)
	for i := 0; i < EventMonitorFiresPerMinute; i++ {
		EventMonitorFires.Allow("alice:w")
	}
	if post(t, h.Name, "b", nil) != http.StatusTooManyRequests {
		t.Error("the watch's budget, spent by its other door, bounds this one too")
	}
	quiet(t, woke)
	RootDB.Unset("event_monitors", "alice:w")
	if post(t, h.Name, "c", nil) != http.StatusAccepted || !strings.Contains(Calls(h.Name)[0].Outcome, "is gone") {
		t.Errorf("gone: %+v", Calls(h.Name))
	}
}

// A retry refused for the watch's budget is passed on when it comes again.
func TestARetryRefusedForTheBudgetIsNotLost(t *testing.T) {
	woke := world(t)
	watch("alice", "w")
	h, _ := Create(Webhook{Label: "W", Owner: "alice", Verify: VerifyNone, Map: Map{ID: "id"}, Feeds: "watch:w"}, "")
	for i := 0; i < EventMonitorFiresPerMinute; i++ {
		EventMonitorFires.Allow("alice:w")
	}
	if post(t, h.Name, `{"id":"e1"}`, nil) != http.StatusTooManyRequests {
		t.Fatal("over budget")
	}
	EventMonitorFires = NewRateLimiter(EventMonitorFiresPerMinute, time.Minute)
	if post(t, h.Name, `{"id":"e1"}`, nil) != http.StatusNoContent || woken(t, woke) == "" {
		t.Error("the retry was passed over")
	}
}

// Refusals cost the caller: an unknown name or a failed check, past the
// limit, is turned away before anything is looked up. Only a post is
// answered; a body too large is refused.
func TestRefusalsAreBounded(t *testing.T) {
	world(t)
	watch("alice", "w")
	h, _ := Create(Webhook{Label: "W", Owner: "alice", Verify: VerifyToken, Header: "X-Token", Feeds: "watch:w"}, "t")
	r := httptest.NewRequest(http.MethodGet, Prefix+h.Name, nil)
	w := httptest.NewRecorder()
	Handle(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("get: %d", w.Code)
	}
	if post(t, h.Name, strings.Repeat("x", maxBody+1), map[string]string{"X-Token": "t"}) != http.StatusRequestEntityTooLarge {
		t.Error("too large")
	}
	for i := 0; i < failMax; i++ {
		post(t, "nope", "x", nil)
	}
	if post(t, h.Name, "x", map[string]string{"X-Token": "t"}) != http.StatusTooManyRequests {
		t.Error("an address that spent its refusals is still answered")
	}
}

// Setting one up: it needs a name, a header for a check that reads one, a
// secret, and one of its owner's own watches woken by a post; a relay that
// is a peer relay; kinds only with where the kind is read. Its owner alone
// edits, renews or deletes it, and how it checks stays as it was made.
func TestSettingOneUp(t *testing.T) {
	world(t)
	watch("alice", "mine")
	watch("bob", "theirs")
	SaveEventMonitor(RootDB, EventMonitor{Name: "polled", Owner: "alice", Kind: EventKindPoll})
	ok := Webhook{Label: "X", Owner: "alice", Verify: VerifyToken, Header: "X-T", Feeds: "watch:mine"}
	for _, tc := range []struct {
		change func(*Webhook)
		secret string
		want   string
	}{
		{func(h *Webhook) { h.Label = " " }, "s", "give it a name"},
		{func(h *Webhook) { h.Header = "" }, "s", "which header"},
		{func(h *Webhook) {}, "", "secret"},
		{func(h *Webhook) { h.Verify = "magic" }, "s", "checks a token"},
		{func(h *Webhook) { h.Verify, h.Algorithm = VerifyHMAC, "md5" }, "s", "sha256 or sha1"},
		{func(h *Webhook) { h.Feeds = "watch:theirs" }, "s", "not one of your watches"},
		{func(h *Webhook) { h.Feeds = "watch:polled" }, "s", "not one of your watches"},
		{func(h *Webhook) { h.Feeds = "" }, "s", "choose the watch"},
		{func(h *Webhook) { h.Reach = "peer:nothing" }, "s", "not a peer relay"},
		{func(h *Webhook) { h.Reach = "elsewhere" }, "s", "directly, or through"},
		{func(h *Webhook) { h.Kinds = []string{"push"} }, "s", "where its kind is read"},
		{func(h *Webhook) { h.Map.ID = "header: " }, "s", "header's name"},
	} {
		h := ok
		tc.change(&h)
		if _, err := Create(h, tc.secret); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("want %q, got %v", tc.want, err)
		}
	}
	h, err := Create(ok, "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(List("alice")) != 1 || len(List("bob")) != 0 {
		t.Error("listed for the wrong owner")
	}
	h.Verify, h.Label = VerifyNone, "Renamed"
	if _, err := Update("bob", h); err == nil {
		t.Error("another person edited it")
	}
	if got, err := Update("alice", h); err != nil || got.Verify != VerifyToken || got.Label != "Renamed" || got.Name != "x" {
		t.Errorf("edit: %+v %v", got, err)
	}
	if NewSecret("bob", h.Name, "z") == nil || NewSecret("alice", h.Name, "") == nil || NewSecret("alice", h.Name, "z") != nil || secret(h.Name) != "z" {
		t.Error("renewing its secret")
	}
	if Remove("bob", h.Name) == nil {
		t.Error("another person deleted it")
	}
	if Remove("alice", h.Name) != nil || secret(h.Name) != "" {
		t.Error("deleting it, with its secret")
	}
	if _, ok := Get("../etc"); ok {
		t.Error("a name outside the shape")
	}
}

// Its address: this site's own, or a peer relay's while it is connected.
func TestItsAddress(t *testing.T) {
	world(t)
	h := Webhook{Name: "x", Reach: ReachDirect}
	if got, ok := h.Address("https://gohort.example.test/"); !ok || got != "https://gohort.example.test/hook/x" {
		t.Errorf("direct: %s", got)
	}
	h.Reach = ReachPeer + "relay"
	if _, ok := h.Address("https://gohort.example.test"); ok {
		t.Error("a relay not connected has no address yet")
	}
}

// Posts to one webhook are bounded whatever they are; a record that checks
// in a way gohort does not know refuses every post; a watch no longer
// woken by a post is not woken; an open webhook has no secret to renew; a
// name outside the shape is never looked up.
func TestWhatIsNeverPassedOn(t *testing.T) {
	woke := world(t)
	watch("alice", "w")
	h, _ := Create(Webhook{Label: "W", Owner: "alice", Verify: VerifyNone, Feeds: "watch:w"}, "")
	posts = NewRateLimiter(1, time.Minute)
	post(t, h.Name, "a", nil)
	woken(t, woke)
	if post(t, h.Name, "b", nil) != http.StatusTooManyRequests {
		t.Error("posts past its ceiling")
	}
	posts = NewRateLimiter(perMinute, time.Minute)

	odd := h
	odd.Name, odd.Verify = "odd", "magic"
	RootDB.Set(table, odd.Name, odd)
	if post(t, odd.Name, "x", nil) != http.StatusUnauthorized {
		t.Error("a check gohort does not know let a post through")
	}
	odd.Name, odd.Verify = "blank", ""
	RootDB.Set(table, odd.Name, odd)
	if post(t, odd.Name, "x", nil) != http.StatusUnauthorized {
		t.Error("a record with no check let a post through")
	}
	if NewSecret("alice", h.Name, "z") == nil {
		t.Error("an open webhook was given a secret")
	}
	m, _ := GetEventMonitor(RootDB, "alice", "w")
	m.Kind = EventKindPoll
	SaveEventMonitor(RootDB, m)
	if post(t, h.Name, "c", nil) != http.StatusAccepted {
		t.Error("a watch that is not woken by a post")
	}
	quiet(t, woke)
	bad := h
	bad.Name = "Bad/Name"
	RootDB.Set(table, bad.Name, bad)
	if _, ok := Get(bad.Name); ok {
		t.Error("a name outside the shape was looked up")
	}
}

// slackPost signs body as Slack does, at when.
func slackPost(t *testing.T, name, body, key string, when time.Time) int {
	t.Helper()
	ts := strconv.FormatInt(when.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("v0:" + ts + ":" + body))
	return post(t, name, body, map[string]string{"X-Slack-Request-Timestamp": ts, "X-Slack-Signature": "v0=" + hex.EncodeToString(mac.Sum(nil))})
}

// channelSeams stands in for the linked services: alice has slack, and
// what is passed to it is kept.
func channelSeams(t *testing.T) *[]ChannelMessage {
	t.Helper()
	var got []ChannelMessage
	linked := []string{"slack"}
	LinkedServices = func(owner string) []string {
		if owner == "alice" {
			return linked
		}
		return nil
	}
	ToChannel = func(owner, svc string, m ChannelMessage) { got = append(got, m) }
	t.Cleanup(func() { LinkedServices, ToChannel = nil, nil })
	return &got
}

// Slack's Events API into a linked service's channel: its URL check is
// answered, a signed message is passed on as a message in its chat, once;
// a bot's post (no user) is passed over; a request signed too long ago, or
// signed wrong, or not at all, is refused. Unlinked, nothing is passed on.
func TestASlackEventReachesAChannel(t *testing.T) {
	world(t)
	got := channelSeams(t)
	h, err := Create(Webhook{Label: "Slack", Owner: "alice", Verify: VerifySlack, Header: "ignored",
		Map:   Map{Text: "event.text", Chat: "event.channel", From: "event.user", ID: "event.ts", Kind: "event.type"},
		Kinds: []string{"message"}, Feeds: "channel:slack"}, "signing-secret")
	if err != nil {
		t.Fatal(err)
	}
	if h.Header != "" {
		t.Errorf("Slack's check takes no header of the owner's: %q", h.Header)
	}
	now := time.Now()
	r := httptest.NewRequest(http.MethodPost, Prefix+h.Name, strings.NewReader(`{"type":"url_verification","challenge":"abc123"}`))
	ts := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, []byte("signing-secret"))
	mac.Write([]byte("v0:" + ts + `:{"type":"url_verification","challenge":"abc123"}`))
	r.Header.Set("X-Slack-Request-Timestamp", ts)
	r.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	Handle(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"challenge":"abc123"`) || len(*got) != 0 {
		t.Errorf("the URL check: %d %s", w.Code, w.Body.String())
	}
	msg := `{"type":"event_callback","event":{"type":"message","channel":"C1","user":"U1","text":"hello","ts":"1700.1"}}`
	if code := slackPost(t, h.Name, msg, "signing-secret", now); code != http.StatusOK {
		t.Fatalf("a signed message: %d", code)
	}
	if len(*got) != 1 || (*got)[0] != (ChannelMessage{ID: "1700.1", Chat: "C1", From: "U1", Text: "hello"}) {
		t.Errorf("passed on: %+v", *got)
	}
	slackPost(t, h.Name, msg, "signing-secret", now)
	bot := `{"type":"event_callback","event":{"type":"message","channel":"C1","bot_id":"B1","text":"my own reply","ts":"1700.2"}}`
	slackPost(t, h.Name, bot, "signing-secret", now)
	if len(*got) != 1 {
		t.Errorf("a retry or a bot's post was passed on: %+v", *got)
	}
	for _, c := range []struct {
		why  string
		code int
	}{
		{"old", slackPost(t, h.Name, msg, "signing-secret", now.Add(-10*time.Minute))},
		{"future", slackPost(t, h.Name, msg, "signing-secret", now.Add(10*time.Minute))},
		{"wrong key", slackPost(t, h.Name, msg, "other", now)},
		{"unsigned", post(t, h.Name, msg, nil)},
	} {
		if c.code != http.StatusUnauthorized {
			t.Errorf("%s: %d", c.why, c.code)
		}
	}
	if got := Calls(h.Name)[0].Outcome; got != "refused: it came with no Slack signature" {
		t.Errorf("the unsigned post's row: %q", got)
	}
	LinkedServices = func(string) []string { return nil }
	msg2 := strings.Replace(msg, "1700.1", "1700.3", 1)
	if code := slackPost(t, h.Name, msg2, "signing-secret", now); code != http.StatusAccepted || len(*got) != 1 ||
		!strings.Contains(Calls(h.Name)[0].Outcome, "no longer one of its owner's linked services") {
		t.Errorf("unlinked: %d %+v", code, Calls(h.Name)[0])
	}
}

// Feeding a channel takes one of the owner's linked services and where a
// message's chat is read.
func TestFeedingAChannelNeedsALinkAndAChat(t *testing.T) {
	world(t)
	channelSeams(t)
	ok := Webhook{Label: "S", Owner: "alice", Verify: VerifyNone, Map: Map{Chat: "c"}, Feeds: "channel:slack"}
	if _, err := Create(ok, ""); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		change func(*Webhook)
		want   string
	}{
		{func(h *Webhook) { h.Map.Chat = "" }, "chat is read"},
		{func(h *Webhook) { h.Feeds = "channel:discord" }, "not one of your linked services"},
		{func(h *Webhook) { h.Owner = "bob" }, "not one of your linked services"},
		{func(h *Webhook) { h.Map.Chat = "header:" }, "header's name"},
	} {
		h := ok
		c.change(&h)
		if _, err := Create(h, ""); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("want %q, got %v", c.want, err)
		}
	}
	LinkedServices = nil
	if _, err := Create(ok, ""); err == nil {
		t.Error("with no linked services kept here, a channel was fed")
	}
	if got := FeedsChannel("alice", "slack"); len(got) != 1 || got[0].Watch() != "" || got[0].Channel() != "slack" {
		t.Errorf("feeding slack: %+v", got)
	}
	if got := FeedsChannel("alice", "discord"); len(got) != 0 {
		t.Errorf("feeding discord: %+v", got)
	}
}
