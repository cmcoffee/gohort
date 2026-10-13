package toolcat

import (
	"testing"

	"github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestTheOwnersLabelWinsAndARejectionSticks(t *testing.T) {
	prev := core.RootDB
	core.RootDB = &core.DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { core.RootDB = prev })

	tool := core.TempTool{Name: "gen", Description: "make a song"}
	if c, _ := Of("u", tool); c != "" {
		t.Fatalf("an unfiled tool has a category: %q", c)
	}
	SetAuto("u", tool, "Music")
	if c, auto := Of("u", tool); c != "Music" || !auto {
		t.Errorf("filed: %q auto=%v", c, auto)
	}
	if c, _ := Of("v", tool); c != "" {
		t.Error("one owner's filing reached another's tool")
	}
	own := tool
	own.Category = "Audio"
	if c, auto := Of("u", own); c != "Audio" || auto {
		t.Errorf("the owner's label did not win: %q auto=%v", c, auto)
	}
	changed := tool
	changed.Description = "make a podcast"
	if _, ok := Auto("u", changed); ok {
		t.Error("a tool that changed what it does kept its old filing")
	}
	Reject("u", tool, "music")
	if c, _ := Of("u", tool); c != "" {
		t.Errorf("a rejected filing is still shown: %q", c)
	}
	if r := Rejected("u", tool); len(r) != 1 || r[0] != "music" {
		t.Errorf("rejected = %v", r)
	}
	if got := InUse("u", []core.TempTool{own, {Name: "x", Category: "audio"}}); len(got) != 1 {
		t.Errorf("one category, two spellings: %v", got)
	}
}
