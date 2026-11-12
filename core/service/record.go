package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
)

// recordsTable keeps each service connector's record: how its calls have
// gone, its last health check, and today's spend. A record is the
// connection's, by its id: one made again under the same name starts with
// none of the one before's.
const recordsTable = "service_records"

// recordKey is where connector name's record is kept: under its id, or,
// for one saved before connections had ids (or a draft), its name.
func recordKey(name string) string {
	if c, ok := GetConnector(RootDB, name); ok && c.ID != "" {
		return "id:" + c.ID
	}
	return name
}

// forgetRecord drops connector c's record, as it is deleted.
func forgetRecord(c Connector) {
	recordMu.Lock()
	defer recordMu.Unlock()
	if RootDB == nil {
		return
	}
	RootDB.Unset(recordsTable, c.Name)
	if c.ID != "" {
		RootDB.Unset(recordsTable, "id:"+c.ID)
	}
}

// Record is one connector's record.
type Record struct {
	Capabilities map[string]*CapabilityRecord `json:"capabilities,omitempty"`
	Health       *HealthCheck                 `json:"health,omitempty"`
	// Day is the date the counts below are for; a new day starts them over.
	Day       string  `json:"day,omitempty"`
	Tests     int     `json:"tests,omitempty"`
	TestSpend float64 `json:"test_spend,omitempty"`
}

// CapabilityRecord is how one capability's real calls have gone.
type CapabilityRecord struct {
	LastOK    time.Time `json:"last_ok,omitempty"`
	LastFail  time.Time `json:"last_fail,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	// LastRefused and LastRefusal are the last call the provider declined
	// for its CONTENT (a safety or usage policy): kept apart from failures,
	// since the service worked and said no to what was asked.
	LastRefused time.Time `json:"last_refused,omitempty"`
	LastRefusal string    `json:"last_refusal,omitempty"`
	// Hours are call counts for this clock hour and the one before.
	Hours [2]HourCount `json:"hours"`
	// Spend and Unpriced are today's: what priced calls cost, and how many
	// calls had no price declared.
	Spend    float64 `json:"spend,omitempty"`
	Unpriced int     `json:"unpriced,omitempty"`
}

// HourCount is one clock hour's calls.
type HourCount struct {
	Hour    int64 `json:"hour"`
	Calls   int   `json:"calls"`
	Failed  int   `json:"failed"`
	Refused int   `json:"refused,omitempty"`
}

// HealthCheck is the last free check.
type HealthCheck struct {
	At      time.Time `json:"at"`
	OK      bool      `json:"ok"`
	Message string    `json:"message,omitempty"`
}

// failingWithin is how recent a connector's latest failure must be to count
// it as failing now: a failure from last week, with nothing since, is not
// something to offer help with today.
const failingWithin = 24 * time.Hour

// Failing reports whether the connector is failing now: its most recent
// outcome, a real call's or the free health check's, within the last day,
// was a failure. A call the provider refused for its content is not one: the
// service worked and said no to what was asked.
func (r Record) Failing(now time.Time) bool {
	var lastOK, lastFail time.Time
	for _, c := range r.Capabilities {
		if c == nil {
			continue
		}
		if c.LastOK.After(lastOK) {
			lastOK = c.LastOK
		}
		if c.LastFail.After(lastFail) {
			lastFail = c.LastFail
		}
	}
	if h := r.Health; h != nil {
		if h.OK && h.At.After(lastOK) {
			lastOK = h.At
		}
		if !h.OK && h.At.After(lastFail) {
			lastFail = h.At
		}
	}
	return !lastFail.IsZero() && lastFail.After(lastOK) && now.Sub(lastFail) < failingWithin
}

var recordMu sync.Mutex

// RecordOf returns a connector's record.
func RecordOf(name string) Record {
	recordMu.Lock()
	defer recordMu.Unlock()
	return loadRecord(name)
}

func loadRecord(name string) Record {
	var r Record
	if RootDB != nil {
		RootDB.Get(recordsTable, recordKey(name), &r)
	}
	if r.Capabilities == nil {
		r.Capabilities = map[string]*CapabilityRecord{}
	}
	if today := time.Now().Format("2006-01-02"); r.Day != today {
		r.Day, r.Tests, r.TestSpend = today, 0, 0
		for _, c := range r.Capabilities {
			c.Spend, c.Unpriced = 0, 0
		}
	}
	return r
}

func updateRecord(name string, fn func(*Record)) {
	recordMu.Lock()
	defer recordMu.Unlock()
	r := loadRecord(name)
	fn(&r)
	if RootDB != nil {
		RootDB.Set(recordsTable, recordKey(name), r)
	}
}

// noteCall records one real call of a capability, and its cost into the
// ledger monitoring totals.
func noteCall(ctx context.Context, name, capability string, out outcome, err error) {
	now := time.Now()
	var after CapabilityRecord
	refused := contentRefused(err)
	defer func() {
		if err != nil && !refused {
			failingNotice(name, capability, after)
		}
	}()
	updateRecord(name, func(r *Record) {
		defer func() { after = *r.Capabilities[capability] }()
		c := r.Capabilities[capability]
		if c == nil {
			c = &CapabilityRecord{}
			r.Capabilities[capability] = c
		}
		h := now.Unix() / 3600
		if c.Hours[0].Hour != h {
			if c.Hours[0].Hour == h-1 {
				c.Hours[1] = c.Hours[0]
			} else {
				c.Hours[1] = HourCount{Hour: h - 1}
			}
			c.Hours[0] = HourCount{Hour: h}
		}
		c.Hours[0].Calls++
		// A call is charged when the provider did the work, even if what came
		// back then failed the contract's check; one the provider refused or
		// failed was never priced, and costs nothing.
		if out.priced {
			c.Spend += out.cost
		} else if err == nil {
			c.Unpriced++
		}
		// A refusal of the content is the service answering, not failing:
		// counted as a refusal, it does not make the service "failing" or
		// send an admin to Fix a spec that is fine.
		if refused {
			c.Hours[0].Refused++
			c.LastRefused, c.LastRefusal = now, clip(err.Error(), 500)
			c.LastOK = now
			return
		}
		if err != nil {
			c.Hours[0].Failed++
			c.LastFail, c.LastError = now, clip(err.Error(), 500)
			return
		}
		c.LastOK = now
	})
	if out.priced {
		RecordExternalCostFor(ctx, "service:"+name+"/"+capability, "Service "+name+": "+capability, out.cost)
	}
}

// noteTest counts one contract test against today's budget, and its cost,
// apart from real use.
func noteTest(ctx context.Context, name, capability string, out outcome) {
	updateRecord(name, func(r *Record) {
		if out.priced {
			r.TestSpend += out.cost
		}
	})
	if out.priced {
		RecordExternalCostFor(ctx, "service:"+name+"/"+capability+":test", "Service "+name+": "+capability+" (tests)", out.cost)
	}
}

// Recent sums a capability's calls over this hour and the last.
func (c *CapabilityRecord) Recent() (calls, failed int) {
	if c == nil {
		return 0, 0
	}
	h := time.Now().Unix() / 3600
	for _, b := range c.Hours {
		if b.Hour >= h-1 {
			calls += b.Calls
			failed += b.Failed
		}
	}
	return calls, failed
}

// RecentRefused is how many of a capability's calls over this hour and the
// last the provider declined for their content.
func (c *CapabilityRecord) RecentRefused() int {
	if c == nil {
		return 0
	}
	h, n := time.Now().Unix()/3600, 0
	for _, b := range c.Hours {
		if b.Hour >= h-1 {
			n += b.Refused
		}
	}
	return n
}

// contentRefusalMark is the fixed phrase gohort's HTTP layer puts in an
// error when a provider refused what was asked (core's refusal note), "how a
// failure reader tells a refusal from a broken tool".
const contentRefusalMark = "REFUSED THE CONTENT"

// contentRefused reports whether err is a provider declining a call's
// content rather than the call failing.
func contentRefused(err error) bool {
	return err != nil && strings.Contains(err.Error(), contentRefusalMark)
}

// failNoticeAfter is how many failures, in two hours and more than half the
// calls, make a service's failing worth telling an admin about.
const failNoticeAfter = 3

// failingNotice tells each admin, in their notifications, that a service is
// failing and where to fix it. A repeat folds into the same notice with a
// count (core/notices), so a service failing all day is one row.
func failingNotice(name, capability string, c CapabilityRecord) {
	calls, failed := c.Recent()
	if failed < failNoticeAfter || failed*2 <= calls || RootDB == nil || AuthDB == nil {
		return
	}
	db := AuthDB()
	if db == nil {
		return
	}
	for _, u := range AuthListUsers(db) {
		if !u.Admin {
			continue
		}
		notices.Record(RootDB, notices.Notice{
			Owner: u.Username,
			Kind:  notices.KindStopped,
			Title: "Service " + name + " is failing",
			Body: fmt.Sprintf("%s failed %d of its last %d calls. The last: %s\n\nOpen Admin > Services > Connectors and use Fix on its row: it reads the failure, tries a corrected spec against the service, and fills the form for you to save.",
				capability, failed, calls, c.LastError),
		})
	}
}
