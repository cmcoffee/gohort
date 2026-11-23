package orchestrate

import (
	"sync"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
)

// phoneLink is a messaging bridge that records what it is asked to send.
type phoneLink struct {
	MessagingLink
	mu   sync.Mutex
	sent []string
}

func (l *phoneLink) OwnerHandle(owner string) (string, bool) { return "self", true }
func (l *phoneLink) SendToChat(owner, chatID, text string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sent = append(l.sent, text)
	return nil
}
func (l *phoneLink) count() int { l.mu.Lock(); defer l.mu.Unlock(); return len(l.sent) }

// Everything waiting on its owner goes to their phone when they chose that,
// once, whatever wrote it: a queued change approval wrote its notice straight
// to the store and reached nobody's phone. A notice that waits on nobody is
// not forwarded by the store, and notify sends a waiting one only once.
func TestEverythingWaitingOnTheOwnerIsForwarded(t *testing.T) {
	pinRootDB(t)
	app, _, _ := newTestOrchestrate(t) // its own AuthDB, with alice in it
	AuthSetNotifyForward(AuthDB(), "alice", "phone")
	link := &phoneLink{}
	prevLink, _ := ActiveMessagingLink()
	RegisterMessagingLink(link)
	t.Cleanup(func() { RegisterMessagingLink(prevLink) })
	prevHook := notices.OnWaiting
	t.Cleanup(func() { notices.OnWaiting = prevHook })
	registerNoticeTransports(app)

	wait := func(want int) {
		t.Helper()
		for i := 0; i < 100 && link.count() < want; i++ {
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond) // and nothing more after it
		if got := link.count(); got != want {
			t.Fatalf("sent %d to the phone, want %d: %v", got, want, link.sent)
		}
	}
	notices.Record(RootDB, notices.Notice{Owner: "alice", Kind: notices.KindBlocked, Title: "Scout is waiting for you to approve a change"})
	wait(1)
	notices.Record(RootDB, notices.Notice{Owner: "alice", Kind: notices.KindBlocked, Title: "Scout is waiting for you to approve a change"})
	notices.Record(RootDB, notices.Notice{Owner: "alice", Kind: notices.KindReport, Title: "Scout said something"})
	wait(1)
	app.notify("alice", notices.Notice{Kind: notices.KindBlocked, Title: "fetch_url needs your approval to run unattended"})
	wait(2)
	app.notify("alice", notices.Notice{Kind: notices.KindStopped, Title: "fetch_url was not run"})
	wait(3)
}
