package scribe

import (
	"os"
	"strings"
	"testing"
)

// A chat panel with no InjectURL has nowhere to put a message typed while a
// turn is running, so it falls back to its ordinary send — which tears the
// running stream down. What the author sees is
//
//	Could not complete this turn — cancelled
//
// after asking for one more thing, and everything the turn had done is thrown
// away. A guide is written over a long turn, with sections landing as they are
// written, so this is the panel most likely to be typed into while it is busy.
//
// Both halves are checked because either alone is broken: a URL with no route
// 404s, and a route nothing points at is dead code.
func TestGuidesChatAcceptsMidFlightMessages(t *testing.T) {
	page, err := os.ReadFile("page.go")
	if err != nil {
		t.Fatal(err)
	}
	web, err := os.ReadFile("web.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), `InjectURL:    "chat/inject"`) {
		t.Error("the Guide Author panel declares no InjectURL — a second thought will cancel the turn in progress")
	}
	// chat/inject is one of the endpoints orchestrate's AppChat answers
	// (PublicHandleInject, the running turn's note queue); Scribe has to hand
	// its chat/ paths there.
	if !strings.Contains(string(web), "strings.HasPrefix(path, scribeChat.Prefix)") || !strings.Contains(string(web), "orch.ServeAppChat(") {
		t.Error("nothing serves chat/inject, so the panel's mid-flight message 404s")
	}
}
