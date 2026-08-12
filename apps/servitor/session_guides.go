// Which guides an investigation fed, and which investigation produced a guide.
//
// The two halves already existed and did not know about each other. An
// investigation could push a section into a guide, creating one if the name was
// new; a guide could be read. Nothing recorded that THIS session wrote THAT
// guide, so the obvious follow-ups had no answer: "open what I just wrote"
// needed the user to go and find it by name, and "where did this section come
// from" had nothing to point at.
//
// Recorded on the FIRST push and every push after, because a single
// investigation legitimately feeds several guides — an outage writes up the
// symptom in one and the fix in another — and forcing a choice at the first
// push would be choosing before the work is done.
//
// Persisted rather than in memory: the point is a link that survives the
// session ending, which is when somebody comes looking for it.
package servitor

import (
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// sessionGuidesTable maps a probe session to the guides it wrote.
const sessionGuidesTable = "servitor_session_guides"

// SessionGuide is one guide an investigation contributed to.
type SessionGuide struct {
	DocID string    `json:"doc_id"`
	Title string    `json:"title"`
	At    time.Time `json:"at"`
	// Created marks the guide this session brought into existence, as opposed
	// to one it added a section to. Worth keeping apart: deleting a guide the
	// investigation authored discards work nobody else has, and appending to
	// somebody's established runbook is a different kind of act from starting
	// one.
	Created bool `json:"created,omitempty"`
}

// linkSessionGuide records that a session wrote to a guide.
//
// Idempotent by doc id: a session that pushes four sections into one guide has
// one link, not four. The title is refreshed on each push so a guide renamed
// after the fact still reads correctly here — the id is the identity, the title
// is a label.
func linkSessionGuide(udb Database, sessionID, docID, title string, created bool) {
	sessionID, docID = strings.TrimSpace(sessionID), strings.TrimSpace(docID)
	if udb == nil || sessionID == "" || docID == "" {
		return
	}
	var list []SessionGuide
	udb.Get(sessionGuidesTable, sessionID, &list)
	for i := range list {
		if list[i].DocID == docID {
			if t := strings.TrimSpace(title); t != "" {
				list[i].Title = t
			}
			// Created is sticky: the first push is the one that made it, and a
			// later append must not rewrite that history.
			udb.Set(sessionGuidesTable, sessionID, list)
			return
		}
	}
	list = append(list, SessionGuide{
		DocID: docID, Title: strings.TrimSpace(title), At: time.Now(), Created: created,
	})
	udb.Set(sessionGuidesTable, sessionID, list)
	Log("[servitor] session %s wrote to guide %q (%s)", sessionID, title, docID)
}

// SessionGuides returns the guides one investigation contributed to, oldest
// first — the order they were written, which is the order they were thought of.
func SessionGuides(udb Database, sessionID string) []SessionGuide {
	if udb == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	var list []SessionGuide
	udb.Get(sessionGuidesTable, strings.TrimSpace(sessionID), &list)
	return list
}

// forgetSessionGuides drops a session's links.
//
// Called when the SESSION is discarded, never when a guide is: the guide is the
// durable artifact and outlives the investigation that produced it, so removing
// a link because a session was cleaned up is right and removing one because a
// guide was deleted would be backwards.
func forgetSessionGuides(udb Database, sessionID string) {
	if udb == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	udb.Unset(sessionGuidesTable, strings.TrimSpace(sessionID))
}
