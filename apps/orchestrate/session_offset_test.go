// A message's index has to name it in STORAGE, not in whatever slice the client
// happened to receive.
//
// The scrub affordance sends {delete_at: i} where i is the message's position
// in the array it was given. Serve a tail without an offset and every one of
// those indices is off by the number dropped — so the ✕ on one message deletes
// a different one, permanently. This is the piece that has to exist BEFORE
// anything trims, which is why it ships while it is still always zero.
package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestServedSessionCarriesAnOffset(t *testing.T) {
	raw, err := json.Marshal(servedSession{
		ChatSession:   ChatSession{ID: "s1", Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
		MessageOffset: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	// Present even at zero: a client that reads it conditionally would silently
	// fall back to relative indices the day a tail arrives.
	if _, ok := got["message_offset"]; !ok {
		t.Errorf("message_offset must always be present, got %s", raw)
	}
	if n, _ := got["message_offset"].(float64); n != 0 {
		t.Errorf("a full load has no offset, got %v", n)
	}
}

// The wrapper must not change the shape the client already reads.
func TestServedSessionKeepsTheSessionShape(t *testing.T) {
	raw, _ := json.Marshal(servedSession{ChatSession: ChatSession{
		ID: "s1", AgentID: "wren",
		Messages: []ChatMessage{{Role: "user", Content: "hello"}},
	}})
	// Field names as the client actually reads them — ID and Messages are
	// untagged, which is exactly why the wrapper must embed rather than
	// re-declare them.
	for _, want := range []string{`"ID":"s1"`, `"Messages"`, `"hello"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("payload should still carry %s, got %s", want, raw)
		}
	}
}

// The offset is a fact about ONE RESPONSE, so it must not be stored: persisted,
// it would be a number that is wrong the moment anything else writes.
func TestOffsetIsNotPartOfTheStoredSession(t *testing.T) {
	raw, err := json.Marshal(ChatSession{ID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "message_offset") {
		t.Errorf("the stored session must not carry an offset, got %s", raw)
	}
}
