package core

// The signed-in person's own account, as they set it from a page: the name
// they go by (core/displayname), the timezone their browser reports, and
// whether they have put the getting-started card away (gettingstarted.go).

import (
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/displayname"
	"github.com/cmcoffee/gohort/core/gettingstarted"
	"github.com/cmcoffee/gohort/core/notices"
)

const (
	// meNamePath reads and sets the person's own name, for their account page.
	meNamePath = "/api/me/name"
	// meZonePath is where a page reports the zone the browser is in.
	meZonePath = "/api/me/browser-zone"
)

// authDisplayName is the account's name, "" when it has none.
func authDisplayName(db Database, username string) string {
	u, _ := AuthGetUser(db, username)
	return strings.TrimSpace(u.DisplayName)
}

// authSetDisplayName stores name as the account's, cleaned. A blank clears
// it, since a name is optional; a name the rule refuses is the error, and
// nothing is stored.
func authSetDisplayName(db Database, username, name string) error {
	clean := ""
	if strings.TrimSpace(name) != "" {
		var err error
		if clean, err = displayname.Clean(name); err != nil {
			return err
		}
	}
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) {
		return errors.New("no such account")
	}
	user.DisplayName = clean
	db.Set(AuthTable, "user:"+username, user)
	return nil
}

// meNameHandler is the signed-in person's own name: GET {display_name,
// username}, and POST or PUT {display_name} to change it.
func meNameHandler(db Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := AuthCurrentUser(r)
		if user == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
		case http.MethodPost, http.MethodPut:
			var req struct {
				DisplayName string `json:"display_name"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			if err := authSetDisplayName(db, user, req.DisplayName); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"display_name": authDisplayName(db, user), "username": user})
	}
}

// authFollowsBrowserZone reports an account whose timezone is its browser's:
// none chosen yet, or one a browser set. A zone chosen on the account page
// (AuthSetUserTimezone) is the person's, and no browser moves it.
func authFollowsBrowserZone(u AuthUser) bool {
	return strings.TrimSpace(u.Timezone) == "" || u.TimezoneFromBrowser
}

// authFollowBrowserZone takes zone, as a browser reported it, as the
// account's timezone when it follows its browser. Whenever that moves the
// zone the account runs on, the person is told: the times they see and the
// schedules they own move with it.
func authFollowBrowserZone(db Database, username, zone string) {
	var user AuthUser
	if !db.Get(AuthTable, "user:"+username, &user) || !authFollowsBrowserZone(user) {
		return
	}
	loc, iana, err := ResolveZone(zone)
	if err != nil {
		return
	}
	if user.Timezone == iana && user.TimezoneFromBrowser {
		return
	}
	// What it ran on until now: its own zone, or the server's. The server's
	// is named "Local" when it comes from the system, so zones are compared
	// by their clocks, not their names.
	before, beforeLoc := strings.TrimSpace(user.Timezone), time.Local
	if before == "" {
		before = "the server's zone"
		if name := time.Local.String(); name != "Local" {
			before += " (" + name + ")"
		}
	} else if l, _, err := ResolveZone(before); err == nil {
		beforeLoc = l
	}
	user.Timezone, user.TimezoneFromBrowser = iana, true
	db.Set(AuthTable, "user:"+username, user)
	if sameClock(beforeLoc, loc) {
		return
	}
	Log("[auth] %q follows their browser to the timezone %s (was %s)", username, iana, before)
	notices.Record(RootDB, notices.Notice{
		Owner: username,
		Kind:  notices.KindReport,
		Title: "Your timezone is now " + iana,
		Body: "It follows your browser. The times you see, and the schedules you own, use " + iana +
			" now, in place of " + before + ". To keep one zone wherever you are, choose it on your Account page.",
	})
}

// sameClock reports two zones that keep the same time as each other for the
// coming year, summer time included: whatever either is called, nothing that
// runs on one moves if it runs on the other.
func sameClock(a, b *time.Location) bool {
	now := time.Now()
	for m := 0; m < 12; m++ {
		at := now.AddDate(0, m, 0)
		_, oa := at.In(a).Zone()
		_, ob := at.In(b).Zone()
		if oa != ob {
			return false
		}
	}
	return true
}

// meZoneHandler takes the browser's zone from a page: POST {zone}. It changes
// nothing for an account with a zone of its own.
func meZoneHandler(db Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := AuthCurrentUser(r)
		if user == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Zone string `json:"zone"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		authFollowBrowserZone(db, user, req.Zone)
		w.WriteHeader(http.StatusNoContent)
	}
}

// browserZoneScript reports the browser's zone from a page, for an account
// that follows it. One request per page load, and nothing when the browser
// does not say.
const browserZoneScript = `<script>(function(){try{var z=Intl.DateTimeFormat().resolvedOptions().timeZone;if(!z)return;fetch('` + meZonePath + `',{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify({zone:z})});}catch(e){}})();</script>`

// meHideStartPath puts the getting-started card away for good.
const meHideStartPath = "/api/me/getting-started/hide"

// The account's own step: a name, for everyone, since it is how the people
// they share with, and who share with them, tell who they are.
func init() {
	gettingstarted.Register(gettingstarted.Step{
		ID: "name", Order: 10,
		Title: "Tell us your name",
		Help:  "So the people you share with know who you are. Optional.",
		Link:  "/account/",
		Done: func(user string) bool {
			if AuthDB == nil {
				return true
			}
			db := AuthDB()
			return db == nil || authDisplayName(db, user) != ""
		},
	})
}

// gettingStartedCard is the dashboard's getting-started card for whoever is
// looking: the steps that apply to them, done ones ticked, and a way to put
// it away. Nothing once they have, or once nothing is left to do.
func gettingStartedCard(r *http.Request) string {
	user := AuthCurrentUser(r)
	if user == "" || AuthDB == nil {
		return ""
	}
	db := AuthDB()
	if db == nil {
		return ""
	}
	if u, ok := AuthGetUser(db, user); !ok || u.GettingStartedHidden {
		return ""
	}
	items := gettingstarted.For(r, user)
	if gettingstarted.Remaining(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class="notice gs-card"><span class="gs-title">Getting started</span><ul class="gs-steps">`)
	for _, it := range items {
		if it.Finished {
			b.WriteString(`<li class="gs-done"><span class="gs-mark">&#10003;</span><span class="gs-step">` + html.EscapeString(it.Title) + `</span></li>`)
			continue
		}
		b.WriteString(`<li><span class="gs-mark">&#9675;</span><span class="gs-step"><a href="` + html.EscapeString(it.Link) + `">` +
			html.EscapeString(it.Title) + `</a>`)
		if it.Help != "" {
			b.WriteString(`<span class="gs-help">` + html.EscapeString(it.Help) + `</span>`)
		}
		b.WriteString(`</span></li>`)
	}
	b.WriteString(`</ul><div class="notice-acts"><button type="button" class="notice-dismiss" data-dismiss="` + meHideStartPath + `">Hide</button></div></div>`)
	return b.String()
}

// meHideStartHandler puts the card away: POST.
func meHideStartHandler(db Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := AuthCurrentUser(r)
		if user == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var u AuthUser
		if db.Get(AuthTable, "user:"+user, &u) {
			u.GettingStartedHidden = true
			db.Set(AuthTable, "user:"+user, u)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
