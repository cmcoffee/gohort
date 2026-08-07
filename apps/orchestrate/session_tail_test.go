package orchestrate

// What a tail must never do is renumber the messages it does send. The client
// scrubs and truncates by index, so a trim that forgets its offset does not
// make the page slow — it deletes the wrong message.

import "testing"

func msgsN(n int) []ChatMessage {
	out := make([]ChatMessage, n)
	for i := range out {
		out[i] = ChatMessage{Content: string(rune('a' + i%26))}
	}
	return out
}

func TestTailKeepsTheEndAndSaysWhereItStarted(t *testing.T) {
	all := msgsN(10)
	got, off := tailMessages(all, 3)
	if len(got) != 3 {
		t.Fatalf("tail of 3 returned %d", len(got))
	}
	if off != 7 {
		t.Errorf("offset = %d, want 7", off)
	}
	// The offset plus a delivered message's position must land on the same
	// message in storage. This is the assertion the scrub depends on.
	for i := range got {
		if got[i].Content != all[i+off].Content {
			t.Errorf("delivered[%d] is storage[%d]? got %q want %q", i, i+off, got[i].Content, all[i+off].Content)
		}
	}
}

func TestShortThreadIsServedWhole(t *testing.T) {
	all := msgsN(4)
	got, off := tailMessages(all, 80)
	if len(got) != 4 || off != 0 {
		t.Errorf("a thread shorter than the limit is untrimmed, got %d messages at offset %d", len(got), off)
	}
	// Exactly at the limit is also whole — an off-by-one here would drop the
	// oldest message of every thread that just reached the boundary.
	if got, off := tailMessages(msgsN(80), 80); len(got) != 80 || off != 0 {
		t.Errorf("a thread exactly at the limit is untrimmed, got %d at %d", len(got), off)
	}
}

func TestZeroLimitLoadsEverything(t *testing.T) {
	got, off := tailMessages(msgsN(500), 0)
	if len(got) != 500 || off != 0 {
		t.Errorf("limit 0 means all, got %d messages at offset %d", len(got), off)
	}
}

func TestRequestedLimitBeatsTheDefault(t *testing.T) {
	// The load-earlier button doubles its ask; a server that clamped to the
	// default would leave the button visibly doing nothing.
	if n := resolveTailLimit("500"); n != 500 {
		t.Errorf("an explicit 500 = %d", n)
	}
	// And an explicit 0 has to survive a nonzero default, or the button can
	// never reach the top of a long thread.
	if n := resolveTailLimit("0"); n != 0 {
		t.Errorf("an explicit 0 means all, got %d", n)
	}
	// Nothing asked for, or nonsense asked for, falls to the configured
	// default rather than to zero — which here means the opposite.
	def := sessionTailLimit()
	for _, bad := range []string{"", "  ", "-5", "all", "12x", "1e6"} {
		if n := resolveTailLimit(bad); n != def {
			t.Errorf("resolveTailLimit(%q) = %d, want the default %d", bad, n, def)
		}
	}
}
