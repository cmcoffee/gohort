package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/toolcat"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Every tool without a label of its own gets filed, API tools included; a
// tool with the owner's label is never asked about, and a category the owner
// took a tool out of is never chosen for it again.
func TestEveryUnlabelledToolIsFiled(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	for _, tt := range []TempTool{
		{Name: "mine", Description: "the owner's own", Category: "Music", CommandTemplate: "echo"},
		{Name: "weather_now", Description: "current weather", Mode: TempToolModeAPI, CommandTemplate: "https://wx.test/now"},
		{Name: "song_gen", Description: "make a song", CommandTemplate: "echo"},
	} {
		if err := AdminPersistTempTool(RootDB, "u", tt); err != nil {
			t.Fatal(err)
		}
	}
	song := TempTool{Name: "song_gen", Description: "make a song", CommandTemplate: "echo"}
	for _, p := range LoadPersistentTempTools(RootDB, "u") {
		if p.Tool.Name == "song_gen" {
			song = p.Tool
		}
	}
	toolcat.Reject("u", song, "Music")
	asked := map[string]string{}
	prev := toolCategoryAsk
	toolCategoryAsk = func(ctx context.Context, app *OrchestrateApp, msgs []Message) (string, error) {
		u := msgs[1].Content
		name := strings.Fields(strings.SplitN(u, "Tool: ", 2)[1])[0]
		asked[name] = u
		if name == "song_gen" {
			return `{"category": "Audio"}`, nil
		}
		return `{"category": "Weather"}`, nil
	}
	t.Cleanup(func() { toolCategoryAsk = prev })

	(&OrchestrateApp{}).fileUserTools("u")
	if _, ok := asked["mine"]; ok {
		t.Error("a tool with the owner's own label was filed")
	}
	if !strings.Contains(asked["song_gen"], "Do not choose any of those") || strings.Contains(asked["song_gen"], "in use: Music") {
		t.Errorf("a rejected category was offered: %q", asked["song_gen"])
	}
	cats := map[string]string{}
	for _, p := range LoadPersistentTempTools(RootDB, "u") {
		c, _ := toolcat.Of("u", p.Tool)
		cats[p.Tool.Name] = c
	}
	if cats["weather_now"] != "Weather" || cats["song_gen"] != "Audio" || cats["mine"] != "Music" {
		t.Errorf("categories = %v", cats)
	}
}
