package core

import "testing"

// A session is running from its registration until its last event marks it
// done: what a page opening it reads to join it rather than show it ended.
func TestALiveSessionIsRunningUntilItsLastEvent(t *testing.T) {
	m := NewLiveSessionMap[string](4)
	if m.Running("s1") {
		t.Fatal("a session never registered is running")
	}
	m.Register("s1", "probe", nil)
	if !m.Running("s1") {
		t.Fatal("a registered session is not running")
	}
	m.AppendEvent("s1", "the end", true)
	if m.Running("s1") {
		t.Error("a session whose last event came is still running")
	}
}
