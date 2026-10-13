package core

import (
	"slices"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// The fleet-wide ask marks are listed when asked for with no agent: they were
// skipped on both passes, so a surface listing them showed none.
func TestAskInChatToolsListsTheFleetWideMarks(t *testing.T) {
	prev := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prev })
	SetUserToolAsksInChat(RootDB, "u", "", "fetch_url", true)
	SetUserToolAsksInChat(RootDB, "u", "a1", "web_search", true)
	if got := AskInChatTools(RootDB, "u", ""); !slices.Contains(got, "fetch_url") || slices.Contains(got, "web_search") {
		t.Errorf("fleet-wide list = %v, want fetch_url and not a1's web_search", got)
	}
	if got := AskInChatTools(RootDB, "u", "a1"); !slices.Contains(got, "fetch_url") || !slices.Contains(got, "web_search") {
		t.Errorf("a1's list = %v, want its own mark and the fleet-wide one", got)
	}
}
