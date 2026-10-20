package main

// Setup names an app's sessions for it, so the model calls they make say
// whose they are (core.sessionCallerID): every setup path, web and command
// line, comes through set_agent_db.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

type namedTestApp struct{ AppCore }

func (namedTestApp) Name() string         { return "autoblog" }
func (namedTestApp) Desc() string         { return "" }
func (namedTestApp) SystemPrompt() string { return "" }
func (namedTestApp) Init() error          { return nil }
func (namedTestApp) Main() error          { return nil }

func TestSetupNamesAnAppsSessions(t *testing.T) {
	prev := global.cache
	global.cache = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { global.cache = prev })
	app := &namedTestApp{}
	set_agent_db(app, &DBase{Store: kvlite.MemStore()})
	if c := app.Get().CreateSession(WORKER).CallerID; !strings.HasPrefix(c, "autoblog.") {
		t.Errorf("a session of the app is called %q", c)
	}
}
