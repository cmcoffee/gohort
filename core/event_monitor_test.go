package core

import (
	"context"
	"testing"
)

func TestEventMonitorStoreRoundTrip(t *testing.T) {
	db := memDB(t)
	m := EventMonitor{
		Name: "ts-join", Owner: "craig", Kind: EventKindWebhook,
		WakeBrief: "someone joined", Token: NewEventToken(),
	}
	SaveEventMonitor(db, m)

	got, ok := GetEventMonitor(db, "craig", "ts-join")
	if !ok || got.Token != m.Token || got.Kind != EventKindWebhook {
		t.Fatalf("GetEventMonitor round-trip failed: %+v ok=%v", got, ok)
	}
	// Token lookup (the public-endpoint path) finds it.
	byTok, ok := FindEventMonitorByToken(db, m.Token)
	if !ok || byTok.Name != "ts-join" {
		t.Fatalf("FindEventMonitorByToken failed: %+v ok=%v", byTok, ok)
	}
	if _, ok := FindEventMonitorByToken(db, "nope"); ok {
		t.Fatalf("FindEventMonitorByToken matched a bogus token")
	}
	// Owner scoping: another owner doesn't see it.
	if _, ok := GetEventMonitor(db, "other", "ts-join"); ok {
		t.Fatalf("monitor leaked across owners")
	}
	DeleteEventMonitor(db, "craig", "ts-join")
	if _, ok := GetEventMonitor(db, "craig", "ts-join"); ok {
		t.Fatalf("DeleteEventMonitor left the record")
	}
}

func TestEventMatch(t *testing.T) {
	cases := []struct {
		answer, match string
		want          bool
	}{
		{"YES, two new CVEs", "", true},         // default match = YES
		{"no change", "", false},                // default match = YES, absent
		{"status: ALERT raised", "alert", true}, // case-insensitive custom
		{"all quiet", "ALERT", false},           // custom absent
		{"", "", false},                         // empty answer never fires
	}
	for _, c := range cases {
		if got := eventMatch(c.answer, c.match); got != c.want {
			t.Errorf("eventMatch(%q, %q) = %v, want %v", c.answer, c.match, got, c.want)
		}
	}
}

func TestExtractJSONPath(t *testing.T) {
	body := []byte(`{"quoteResponse":{"result":[{"regularMarketPrice":142.37,"symbol":"NVDA"}]}}`)
	got, err := extractJSONPath(body, "quoteResponse.result.0.regularMarketPrice")
	if err != nil || got != "142.37" {
		t.Fatalf("extractJSONPath = %q, %v; want \"142.37\"", got, err)
	}
	if _, err := extractJSONPath(body, "quoteResponse.result.5.x"); err == nil {
		t.Fatalf("expected out-of-range index error")
	}
	if _, err := extractJSONPath([]byte("not json"), "a.b"); err == nil {
		t.Fatalf("expected non-JSON error")
	}
}

func TestExtractRegex(t *testing.T) {
	got, err := extractRegex([]byte(`price: $142.37 USD`), `\$([0-9.]+)`)
	if err != nil || got != "142.37" {
		t.Fatalf("extractRegex = %q, %v; want \"142.37\"", got, err)
	}
	if _, err := extractRegex([]byte("nope"), `\$([0-9.]+)`); err == nil {
		t.Fatalf("expected no-match error")
	}
}

func TestCompareValues(t *testing.T) {
	cases := []struct {
		ev, op, tv string
		want       bool
		wantErr    bool
	}{
		{"142.37", "<", "150", true, false},
		{"152", "<", "150", false, false},
		{"152", ">", "150", true, false},
		{"150", ">=", "150", true, false},
		{"ok", "==", "ok", true, false},
		{"up", "!=", "down", true, false},
		{"server is DOWN", "contains", "DOWN", true, false},
		{"abc", "<", "150", false, true}, // non-numeric
		{"1", "??", "2", false, true},    // bad op
	}
	for _, c := range cases {
		got, err := compareValues(c.ev, c.op, c.tv)
		if c.wantErr {
			if err == nil {
				t.Errorf("compareValues(%q,%q,%q) expected error", c.ev, c.op, c.tv)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("compareValues(%q,%q,%q) = %v,%v; want %v", c.ev, c.op, c.tv, got, err, c.want)
		}
	}
}

func TestExecuteEventPollFiresAndDebounces(t *testing.T) {
	db := memDB(t)
	m := EventMonitor{
		Name: "cve-watch", Owner: "craig", Kind: EventKindPoll,
		CheckAgent: "Security", Check: "any new CVEs?", IntervalSeconds: 60,
	}
	SaveEventMonitor(db, m)

	pollAnswer := "YES — CVE-2026-1"
	RegisterEventPoller(func(ctx context.Context, owner, agentID, check string) (string, error) {
		return pollAnswer, nil
	})
	defer RegisterEventPoller(nil)

	var wakes []string
	RegisterEventWaker(func(ctx context.Context, owner, name, summary string) {
		wakes = append(wakes, summary)
	})
	defer RegisterEventWaker(nil)

	// First match → wakes.
	executeEventPoll(context.Background(), db, m)
	if len(wakes) != 1 {
		t.Fatalf("expected 1 wake on first match, got %d", len(wakes))
	}
	// Same answer again → debounced (no new wake). Re-read so LastResult is set.
	cur, _ := GetEventMonitor(db, "craig", "cve-watch")
	executeEventPoll(context.Background(), db, cur)
	if len(wakes) != 1 {
		t.Fatalf("expected debounce on identical answer, got %d wakes", len(wakes))
	}
	// Changed but still-matching answer → edge-triggered, so NO new wake. It
	// re-fires only after the condition clears and re-arms.
	pollAnswer = "YES — CVE-2026-1, CVE-2026-2"
	cur, _ = GetEventMonitor(db, "craig", "cve-watch")
	executeEventPoll(context.Background(), db, cur)
	if len(wakes) != 1 {
		t.Fatalf("expected no re-wake while still matching (edge-triggered), got %d", len(wakes))
	}
	// Non-matching answer → re-arms (no wake).
	pollAnswer = "no new CVEs"
	cur, _ = GetEventMonitor(db, "craig", "cve-watch")
	executeEventPoll(context.Background(), db, cur)
	if len(wakes) != 1 {
		t.Fatalf("expected no wake on non-match, got %d", len(wakes))
	}
	// Matching again after re-arm → a fresh onset wakes.
	pollAnswer = "YES — CVE-2026-3"
	cur, _ = GetEventMonitor(db, "craig", "cve-watch")
	executeEventPoll(context.Background(), db, cur)
	if len(wakes) != 2 {
		t.Fatalf("expected a wake on the next onset after re-arm, got %d", len(wakes))
	}
}
