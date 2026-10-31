package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
)

// recordsTable keeps each service connector's record: how its calls have
// gone, its last health check, and today's spend.
const recordsTable = "service_records"

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
	// Hours are call counts for this clock hour and the one before.
	Hours [2]HourCount `json:"hours"`
	// Spend and Unpriced are today's: what priced calls cost, and how many
	// calls had no price declared.
	Spend    float64 `json:"spend,omitempty"`
	Unpriced int     `json:"unpriced,omitempty"`
}

// HourCount is one clock hour's calls.
type HourCount struct {
	Hour   int64 `json:"hour"`
	Calls  int   `json:"calls"`
	Failed int   `json:"failed"`
}

// HealthCheck is the last free check.
type HealthCheck struct {
	At      time.Time `json:"at"`
	OK      bool      `json:"ok"`
	Message string    `json:"message,omitempty"`
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
		RootDB.Get(recordsTable, name, &r)
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
		RootDB.Set(recordsTable, name, r)
	}
}

// noteCall records one real call of a capability, and its cost into the
// ledger monitoring totals.
func noteCall(ctx context.Context, name, capability string, out outcome, err error) {
	now := time.Now()
	var after CapabilityRecord
	defer func() {
		if err != nil {
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
			Body: fmt.Sprintf("%s failed %d of its last %d calls. The last: %s\n\nOpen Admin > Extensions > Connectors and use Fix on its row: it reads the failure, tries a corrected spec against the service, and fills the form for you to save.",
				capability, failed, calls, c.LastError),
		})
	}
}
