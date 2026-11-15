package account

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The Timezone select reads "based on my browser" while the account follows
// it, saying the zone it has now, and the chosen zone otherwise. Choosing the
// browser again keeps the zone it has until the browser next reports one, so
// nothing the person owns moves in between.
func TestTheTimezoneChoiceIsTheBrowserOrAZone(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
	AuthSetUser(db, "pat", "pw-pat-123", false)
	if accountZoneChoice(db, "pat") != "" || browserZoneLabel("pat") != "Based on my browser" {
		t.Errorf("unset: %q %q", accountZoneChoice(db, "pat"), browserZoneLabel("pat"))
	}
	u, _ := AuthGetUser(db, "pat")
	u.Timezone, u.TimezoneFromBrowser = "Europe/London", true
	db.Set(AuthTable, "user:pat", u)
	if accountZoneChoice(db, "pat") != "" || browserZoneLabel("pat") != "Based on my browser (now Europe/London)" {
		t.Errorf("following: %q %q", accountZoneChoice(db, "pat"), browserZoneLabel("pat"))
	}
	AuthSetUserTimezone(db, "pat", "Asia/Tokyo")
	if accountZoneChoice(db, "pat") != "Asia/Tokyo" || browserZoneLabel("pat") != "Based on my browser" {
		t.Errorf("chosen: %q %q", accountZoneChoice(db, "pat"), browserZoneLabel("pat"))
	}
	followBrowserZone(db, "pat")
	if u, _ := AuthGetUser(db, "pat"); u.Timezone != "Asia/Tokyo" || !u.TimezoneFromBrowser {
		t.Errorf("back to the browser: %q %v", u.Timezone, u.TimezoneFromBrowser)
	}
}
