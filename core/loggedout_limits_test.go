package core

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// One fire per webhook monitor at a time. Posts arriving during a fire are
// delivered together as the next one, so a burst costs one more turn and loses
// nothing; a monitor that has used its fires runs no more.
func TestWebhookPostsCoalesceAndStopAtTheirLimit(t *testing.T) {
	db := memDB(t)
	SaveEventMonitor(db, EventMonitor{Owner: "craig", Name: "ci", Kind: EventKindWebhook, MaxFires: 5})
	release := make(chan struct{})
	var mu sync.Mutex
	var got []string
	RegisterEventWaker(func(ctx context.Context, owner, name, summary string) (bool, string) {
		mu.Lock()
		got = append(got, summary)
		first := len(got) == 1
		mu.Unlock()
		if first {
			<-release
		}
		return true, ""
	})
	defer RegisterEventWaker(nil)

	m, _ := GetEventMonitor(db, "craig", "ci")
	done := make(chan struct{})
	go func() { FireEventMonitor(context.Background(), db, m, "one"); close(done) }()
	time.Sleep(50 * time.Millisecond)
	FireEventMonitor(context.Background(), db, m, "two")
	FireEventMonitor(context.Background(), db, m, "three")
	close(release)
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || !strings.Contains(got[1], "two") || !strings.Contains(got[1], "three") {
		t.Fatalf("posts during a fire: %q", got)
	}

	cur, _ := GetEventMonitor(db, "craig", "ci")
	cur.MaxFires, cur.FireCount = 2, 2
	SaveEventMonitor(db, cur)
	FireEventMonitor(context.Background(), db, cur, "four")
	if len(got) != 2 {
		t.Errorf("a monitor past its fires fired again: %q", got)
	}
}

// Deleting an account clears its webhook tokens and pauses its monitors, and
// a token whose owner is gone finds nothing.
func TestAWebhookEndsWithItsAccount(t *testing.T) {
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })

	SaveEventMonitor(RootDB, EventMonitor{Owner: "alice", Name: "ci", Kind: EventKindWebhook, Token: "tok-a"})
	if _, ok := FindEventMonitorByToken(RootDB, "tok-a"); !ok {
		t.Fatal("a live account's token does not resolve")
	}
	adb.Unset(AuthTable, "user:alice")
	if _, ok := FindEventMonitorByToken(RootDB, "tok-a"); ok {
		t.Error("a removed account's webhook still resolves")
	}
	if got := RevokeUserCredentials(adb, "alice"); got["event monitors"] != 1 {
		t.Errorf("deletion did not reach the monitor: %v", got)
	}
	if m, _ := GetEventMonitor(RootDB, "alice", "ci"); m.Token != "" || !m.Paused {
		t.Errorf("monitor after deletion: %+v", m)
	}
}

// A capability token in the path is not written to the access log.
func TestTheAccessLogDropsPathTokens(t *testing.T) {
	tok := strings.Repeat("ab12", 12)
	r := httptest.NewRequest(http.MethodPost, "/orchestrate/api/operator/event/"+tok, nil)
	if got := accessLogPath(r); strings.Contains(got, tok) {
		t.Errorf("token logged: %s", got)
	}
	r = httptest.NewRequest(http.MethodGet, "/orchestrate/api/sessions/123e4567-e89b-12d3-a456-426614174000", nil)
	if got := accessLogPath(r); strings.Contains(got, "REDACTED") {
		t.Errorf("an ordinary id was redacted: %s", got)
	}
}

// Spent answers without spending.
func TestASpentCheckRecordsNothing(t *testing.T) {
	l := NewRateLimiter(2, time.Minute)
	for i := 0; i < 5; i++ {
		if l.Spent("x") {
			t.Fatal("a check spent the allowance")
		}
	}
	l.Allow("x")
	l.Allow("x")
	if !l.Spent("x") {
		t.Error("a spent key reads as available")
	}
}

// A failed login counts against the address (an IPv6 one by its /64) and the
// account, the account at a higher threshold.
func TestLoginLimitsCountTheBlockAndTheAccount(t *testing.T) {
	a := loginLockKeys(net.ParseIP("2001:db8:1:2::5"), "Alice@Example.com")
	b := loginLockKeys(net.ParseIP("2001:db8:1:2::9"), "alice@example.com")
	if a[0] != b[0] || a[1] != b[1] || a[1] != "acct:alice@example.com" {
		t.Errorf("keys: %v %v", a, b)
	}
	if attemptsFor("acct:x") <= attemptsFor("203.0.113.1") {
		t.Error("the account limit is not looser than the address limit")
	}
}

// Reset requests are bounded per account, so no one can mail an account
// without limit.
func TestResetRequestsAreBounded(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	sent := 0
	for i := 0; i < 10; i++ {
		r := httptest.NewRequest(http.MethodPost, "/forgot", strings.NewReader("email=nobody@example.com"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.RemoteAddr = fmt.Sprintf("198.51.100.%d:5000", i+1)
		ForgotHandler(db)(httptest.NewRecorder(), r)
		if forgotPerAccount.Spent("nobody@example.com") {
			sent = i + 1
			break
		}
	}
	if sent == 0 || sent > 3 {
		t.Errorf("an account's reset requests were not bounded (spent after %d)", sent)
	}
}
