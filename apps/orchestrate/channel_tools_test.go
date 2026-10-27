package orchestrate

import (
	"testing"
	"time"
)

func TestRelativeAge(t *testing.T) {
	now := time.Date(2026, 7, 15, 7, 37, 0, 0, time.UTC)
	cases := []struct {
		then time.Time
		want string
	}{
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-20 * time.Minute), "20m ago"},
		{now.Add(-6 * time.Hour), "6h ago"},
		{now.Add(-3 * 24 * time.Hour), "3d ago"},
		{now.Add(2 * time.Hour), "just now"}, // future/skew -> not negative
	}
	for _, c := range cases {
		if got := relativeAge(now, c.then); got != c.want {
			t.Errorf("relativeAge(now, %s) = %q, want %q", c.then, got, c.want)
		}
	}
}

// localChatTime turns a UTC bridge timestamp into local time + relative age,
// and leaves unparseable input alone.
func TestLocalChatTime(t *testing.T) {
	out := localChatTime("2026-07-15T02:00:30Z")
	if out == "2026-07-15T02:00:30Z" || out == "" {
		t.Fatalf("UTC timestamp was not localized: %q", out)
	}
	if !containsAgo(out) {
		t.Errorf("expected a relative-age suffix, got %q", out)
	}
	if got := localChatTime("not a timestamp"); got != "not a timestamp" {
		t.Errorf("unparseable input should pass through, got %q", got)
	}
	if got := localChatTime(""); got != "" {
		t.Errorf("empty input should stay empty, got %q", got)
	}
}

func containsAgo(s string) bool {
	for _, sub := range []string{"ago", "just now"} {
		if len(s) >= len(sub) {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
