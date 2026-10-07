package orchestrate

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// What a message sent reaches the model's copy of the history, so a later turn
// does not believe a delivered picture is still owed.
func TestHistoryKnowsWhatAMessageDelivered(t *testing.T) {
	m := ChatMessage{Role: "assistant", Content: "Cover's done.",
		Attachments: []string{"img-1"},
		Files:       []deliveredFile{{ID: "f1", Name: "rory_song.mp3", Kind: "audio"}},
		ToolCalls: []PersistedToolCall{
			{Name: "send_message", Args: map[string]any{"to": "Group Chat", "attachments": []any{"workspace/songs/gospel.mp4"}}, Result: "Sent to Group Chat (replying in-thread)."},
			// Queued for approval is not sent; an errored call is not sent.
			{Name: "send_message", Args: map[string]any{"attachment": "other.png"}, Result: "Queued for your approval: message to Bob."},
			{Name: "send_message", Args: map[string]any{"attachments": []any{"broken.png"}}, Err: "no such file"},
		},
	}
	got := llmHistoryContent(m)
	if !strings.HasPrefix(got, "Cover's done.\n") {
		t.Fatalf("the message's own words come first: %q", got)
	}
	for _, want := range []string{"1 image", "rory_song.mp3", "gospel.mp4 (via send_message)", "Already sent"} {
		if !strings.Contains(got, want) {
			t.Errorf("history note missing %q:\n%s", want, got)
		}
	}
	for _, not := range []string{"other.png", "broken.png", "workspace/songs"} {
		if strings.Contains(got, not) {
			t.Errorf("history note claims %q, which was not sent:\n%s", not, got)
		}
	}

	// A background result's card keeps its report marker AND says what it sent.
	card := llmHistoryContent(ChatMessage{Role: "assistant", Content: "Cover's done.", ReportFrom: "WiWee", Attachments: []string{"img-1"}})
	if !strings.Contains(card, "automated report") || !strings.Contains(card, "1 image") {
		t.Errorf("a delivered report card lost its marker or its delivery: %q", card)
	}

	// Nothing sent, nothing added: history stays byte-identical, prompt cache
	// and all.
	for _, plain := range []ChatMessage{
		{Role: "assistant", Content: "just a reply"},
		{Role: "user", Content: "here's a photo", Attachments: []string{"img-9"}},
	} {
		if got := llmHistoryContent(plain); got != plain.Content {
			t.Errorf("a message that delivered nothing changed: %q", got)
		}
	}
}

// A background result delivered while a turn runs is seen by that turn, once,
// and only if it landed after the turn began.
func TestATurnLearnsOfABackgroundDelivery(t *testing.T) {
	sid := "chan:test;" + t.Name()
	recordBackgroundDelivery(sid, 1, "earlier, before this turn")
	time.Sleep(2 * time.Millisecond)

	w := watchBackgroundDeliveries(sid)
	if w.files() != 0 || w.notices() != nil {
		t.Fatal("a delivery from before the turn began is not this turn's business")
	}

	recordBackgroundDelivery(sid, 2, "Cover's done for the Rory album.")
	if w.files() != 2 {
		t.Errorf("files = %d, want the 2 delivered during the turn", w.files())
	}
	n := w.notices()
	if len(n) != 1 || !strings.Contains(n[0].Content, "already sent") || !strings.Contains(n[0].Content, "Cover's done") {
		t.Fatalf("notice = %+v, want one saying it is already sent, with what it said", n)
	}
	if w.notices() != nil {
		t.Error("the same delivery was announced twice")
	}

	// Other sessions, and deliveries with no files, are not this turn's.
	recordBackgroundDelivery("chan:other;"+t.Name(), 1, "x")
	recordBackgroundDelivery(sid, 0, "text only")
	if w.files() != 2 || w.notices() != nil {
		t.Error("another session's delivery, or one with no files, reached this turn")
	}
}

// The loop calls an injection drain again before finalizing and stops on nil,
// so the composed drain must return nil whenever both halves are empty.
func TestDeliveryDrainEndsOnNil(t *testing.T) {
	sid := "chan:test;" + t.Name()
	w := watchBackgroundDeliveries(sid)
	drain := drainWithDeliveries(nil, w)
	if drain() != nil {
		t.Fatal("nothing queued, nothing delivered: the drain must return nil")
	}
	recordBackgroundDelivery(sid, 1, "done")
	if got := drain(); len(got) != 1 {
		t.Fatalf("got %d messages, want the delivery notice", len(got))
	}
	if drain() != nil {
		t.Error("after reporting, the drain must return nil again")
	}
	user := drainWithDeliveries(func() []Message { return []Message{{Role: "user", Content: "note"}} }, w)
	if got := user(); len(got) != 1 || got[0].Content != "note" {
		t.Errorf("the turn's own notes still come through: %+v", got)
	}
}

// Both turn paths read the ledger in all three places, and the wake records a
// delivery only after every route has had its chance to send it.
func TestBothTurnPathsReadTheLedger(t *testing.T) {
	for file, needles := range map[string][]string{
		"agent_dispatch.go": {`bgSent.files\(\)\s*\n\s*}`, `if bgSent.files\(\) > 0 \{\s*return nil`, `InjectionDrain: drainWithDeliveries\(onRoundStart, bgSent\)`},
		"runner.go":         {`pr.sess.Files\) \+ pr.bgSent.files\(\)`, `if pr.bgSent.files\(\) > 0 \{\s*return nil`, `InjectionDrain: drainWithDeliveries\(`},
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range needles {
			if !regexp.MustCompile(n).Match(src) {
				t.Errorf("%s: missing %s", file, n)
			}
		}
	}
	src, _ := os.ReadFile("scheduled_updates.go")
	s := string(src)
	send, rec, warn := strings.Index(s, "deliverWakeToChannel(p, subSess"), strings.Index(s, "recordBackgroundDelivery(p.SessionID"), strings.Index(s, "but nothing sent them")
	if send < 0 || rec < send || warn < send {
		t.Error("the wake must record (or warn about) its delivery after the channel send, not before it")
	}
}
