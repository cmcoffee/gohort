package ui

// The badge that says a decision is waiting.
//
// An approval queue belongs to the USER, not to whichever agent is on screen —
// that is what all_agents means on a nav item. Its badge went stale while you
// sat on an agent that does not poll, because the 30-second tick was written
// for the session list and returned early for anything that has none. The
// button that lights up when a decision arrives did not, until you navigated.

import (
	"strings"
	"testing"
)

func pollBody(t *testing.T) string {
	t.Helper()
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	i := strings.Index(src, "if (bulkState && bulkState.mode) return;")
	if i < 0 {
		t.Fatal("the background tick is gone")
	}
	end := strings.Index(src[i:], "}, 30000);")
	if end < 0 {
		t.Fatal("could not bound the tick")
	}
	return src[i : i+end]
}

// The tick must not abandon the user's queues just because this agent has no
// session list to reload.
func TestTheTickStillRefreshesAUserQueueOnAnAgentWithNoList(t *testing.T) {
	body := pollBody(t)
	i := strings.Index(body, "isAltNavAgent")
	if i < 0 {
		t.Fatal("the channel-agent guard is gone; this test is reading the wrong place")
	}
	// A bare early return is the defect: it drops the badges with the sessions.
	guard := body[i:]
	if j := strings.Index(guard, "\n"); j > 0 && strings.Contains(guard[:j], "return;") {
		t.Error("the tick still returns outright for a non-channel agent, so a user queue's badge never updates there")
	}
	if !strings.Contains(body, "refreshChannelBadges(true, true)") {
		t.Error("the non-channel branch does not refresh the queue badges")
	}
	// And it must still skip the session reload, which is what it was for.
	after := body[i:]
	if strings.Contains(after[:strings.Index(after, "return;")+7], "loadSessions()") {
		t.Error("a non-channel agent now reloads a session list it does not have")
	}
}

// A badge with no badge_field counts every row: that is a SIZE, not a queue. It
// changes whenever anything is added and never means "you are needed", so
// refreshing those on a timer buys nothing and costs a full re-read of every
// source behind them.
func TestOnlyQueuesArePolled(t *testing.T) {
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	i := strings.Index(src, "function refreshChannelBadges(")
	if i < 0 {
		t.Fatal("refreshChannelBadges is gone")
	}
	fn := src[i : i+1200]
	if !strings.Contains(fn, "onlyQueues") {
		t.Fatal("refreshChannelBadges cannot narrow to queues")
	}
	if !strings.Contains(fn, "if (onlyQueues && !item.badge_field) return;") {
		t.Error("the queue filter does not key on badge_field")
	}
	// The existing narrowing is untouched: a non-alt-nav agent still only
	// fetches the always-on entries when it refreshes everything.
	if !strings.Contains(fn, "if (onlyAllAgents && !item.all_agents) return;") {
		t.Error("the all_agents narrowing was lost")
	}
}

// The navigation-time refresh is deliberately broader than the tick: arriving
// on a page is when every visible count should be right, and it happens once.
func TestNavigationStillRefreshesEveryVisibleBadge(t *testing.T) {
	src := readRuntimeFile(t, "30_agent_loop_panel.js")
	if !strings.Contains(src, "if (!isOrch && hasAllAgentNav) refreshChannelBadges(true);") {
		t.Error("the navigation refresh changed; it should stay broad, unlike the tick")
	}
}
