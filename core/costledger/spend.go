// What each person spends in a month, and the caps on it.
//
// The cost ledger (cost_ledger.go) knows what each source cost per day, and
// nothing of who. This is the other half: every priced cost done for a person
// (model use at the administrator's tier rates, a paid credential call, a
// service) is charged to them in a monthly rollup, split by the agent that ran
// it, the key that asked for it and where the cost came from. Work no person
// stands behind is not charged here, and never capped.
//
// Who pays rides the context (WithSpender), set where work starts for someone:
// a request (the signed-in person, or the owner of the key that asked), and the
// places background work starts (a run, a pipeline, a session). The first
// person set stays: work a person caused is theirs however deep it goes. The
// agent is the last one set, the one doing the work.
//
// Caps are monthly, a calendar month in the deployment's zone (time.Local):
// the administrator's default for everyone and an override per person, a lower
// one of the person's own, and one per key, set by its owner. Zero is no cap.
// Work is refused before it starts once a cap is reached, and a paid call that
// would cross one is not sent; a model turn already under way finishes on the
// local worker. Crossing 80% of a person's cap tells them once a month
// (OnWarn).
//
// A leaf of core, beside the per-source ledger, because core sits at its
// export ceiling; it needs nothing of core but somewhere to keep its rows.
package costledger

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Spender is who pays for work done under a context: the Person charged, the
// Agent doing it and the Key that asked for it, each empty when not known.
type Spender struct {
	Person, Agent, Key string
}

type spenderKey struct{}

// WithSpender is ctx with s merged into who pays: the person and the key
// already set stay (work a person or a key caused stays theirs), an agent set
// here replaces the one before (the agent doing the work is the innermost).
func WithSpender(ctx context.Context, s Spender) context.Context {
	if ctx == nil {
		return ctx
	}
	cur := SpenderFrom(ctx)
	next := cur
	if next.Person == "" {
		next.Person = strings.TrimSpace(s.Person)
	}
	if next.Key == "" {
		next.Key = strings.TrimSpace(s.Key)
	}
	if a := strings.TrimSpace(s.Agent); a != "" {
		next.Agent = a
	}
	if next == cur {
		return ctx
	}
	return context.WithValue(ctx, spenderKey{}, next)
}

// SpenderFrom is who pays for work under ctx.
func SpenderFrom(ctx context.Context) Spender {
	if ctx == nil {
		return Spender{}
	}
	s, _ := ctx.Value(spenderKey{}).(Spender)
	return s
}

// PersonSpend is what one person spent in one month: the total, and the same
// split by agent, by key and by where it came from ("model:lead",
// "cred:<name>", ...). Warned marks the month's 80% notice as sent.
type PersonSpend struct {
	Month   string             `json:"month"`
	Person  string             `json:"person"`
	Total   float64            `json:"total"`
	Agents  map[string]float64 `json:"agents,omitempty"`
	Keys    map[string]float64 `json:"keys,omitempty"`
	Sources map[string]float64 `json:"sources,omitempty"`
	Warned  bool               `json:"warned,omitempty"`
}

// Where spending and its caps are kept.
const (
	spendTable = "person_spend" // "<YYYY-MM>|<person>" -> PersonSpend
	capsTable  = "spend_caps"   // capDefault, "user:<p>", "own:<p>", "key:<id>" -> float64
	capDefault = "default"
)

var spendMu sync.Mutex

// OnWarn, when set, is told once a month that person's spending crossed 80%
// of their cap. Core sets it to put a notice on their bell.
var OnWarn func(person string, spent, cap float64)

// Month is the month t falls in, as the ledger keys it.
func Month(t time.Time) string { return t.In(time.Local).Format("2006-01") }

// ThisMonth is the current month.
func ThisMonth() string { return Month(time.Now()) }

// Charge adds usd to the person paying for work under ctx, under source, and
// returns their month's spend after it. Nothing is charged for work no person
// stands behind, for a zero cost, or before the ledger is wired.
func Charge(ctx context.Context, source string, usd float64) PersonSpend {
	s := SpenderFrom(ctx)
	if s.Person == "" || usd <= 0 {
		return PersonSpend{}
	}
	spendMu.Lock()
	db := ledgerDB()
	if db == nil {
		spendMu.Unlock()
		return PersonSpend{}
	}
	month := ThisMonth()
	row := spentIn(db, month, s.Person)
	before := row.Total
	row.Total += usd
	add := func(m *map[string]float64, k string) {
		if k == "" {
			return
		}
		if *m == nil {
			*m = map[string]float64{}
		}
		(*m)[k] += usd
	}
	add(&row.Agents, s.Agent)
	add(&row.Keys, s.Key)
	add(&row.Sources, source)
	cap := personCap(db, s.Person)
	warn := cap > 0 && !row.Warned && before < 0.8*cap && row.Total >= 0.8*cap
	if warn {
		row.Warned = true
	}
	db.Set(spendTable, month+"|"+s.Person, row)
	spendMu.Unlock()
	if warn && OnWarn != nil {
		go OnWarn(s.Person, row.Total, cap)
	}
	return row
}

// Spent is what person spent in month.
func Spent(month, person string) PersonSpend {
	db := ledgerDB()
	if db == nil {
		return PersonSpend{Month: month, Person: person}
	}
	return spentIn(db, month, person)
}

func spentIn(db Store, month, person string) PersonSpend {
	var row PersonSpend
	db.Get(spendTable, month+"|"+person, &row)
	row.Month, row.Person = month, person
	return row
}

// MonthSpends is everyone who spent anything in month, the most first.
func MonthSpends(month string) []PersonSpend {
	db := ledgerDB()
	if db == nil {
		return nil
	}
	var out []PersonSpend
	for _, k := range db.Keys(spendTable) {
		m, person, ok := strings.Cut(k, "|")
		if !ok || m != month {
			continue
		}
		out = append(out, spentIn(db, month, person))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].Person < out[j].Person
	})
	return out
}

func ledgerDB() Store {
	costLedgerMu.Lock()
	defer costLedgerMu.Unlock()
	return costLedgerDB
}

// --- caps ---------------------------------------------------------------

// getCap is the cap kept under key; setCap never keeps one below zero.
func getCap(db Store, key string) float64 {
	var v float64
	if db != nil {
		db.Get(capsTable, key, &v)
	}
	return v
}

func setCap(key string, usd float64) {
	if usd < 0 {
		usd = 0
	}
	if db := ledgerDB(); db != nil {
		db.Set(capsTable, key, usd)
	}
}

// DefaultCap is the administrator's monthly cap for everyone, zero for none.
func DefaultCap() float64 { return getCap(ledgerDB(), capDefault) }

// SetDefaultCap sets the administrator's monthly cap for everyone.
func SetDefaultCap(usd float64) { setCap(capDefault, usd) }

// PersonOverride is the administrator's cap for person in place of the
// default, zero for none.
func PersonOverride(person string) float64 { return getCap(ledgerDB(), "user:"+person) }

// SetPersonOverride sets the administrator's cap for person; zero goes back
// to the default.
func SetPersonOverride(person string, usd float64) { setCap("user:"+person, usd) }

// OwnCap is the lower cap person set themselves, zero for none.
func OwnCap(person string) float64 { return getCap(ledgerDB(), "own:"+person) }

// SetOwnCap sets the cap person sets themselves. It only ever lowers theirs:
// above the administrator's, the administrator's is what holds.
func SetOwnCap(person string, usd float64) { setCap("own:"+person, usd) }

// KeyCap is a key's monthly cap, set by its owner, zero for none.
func KeyCap(keyID string) float64 { return getCap(ledgerDB(), "key:"+keyID) }

// SetKeyCap sets a key's monthly cap.
func SetKeyCap(keyID string, usd float64) { setCap("key:"+keyID, usd) }

// AdminCap is the administrator's cap for person: their override, else the
// default.
func AdminCap(person string) float64 {
	db := ledgerDB()
	if c := getCap(db, "user:"+person); c > 0 {
		return c
	}
	return getCap(db, capDefault)
}

// PersonCap is the cap that holds for person: the lower of the
// administrator's and their own, zero when neither is set.
func PersonCap(person string) float64 { return personCap(ledgerDB(), person) }

func personCap(db Store, person string) float64 {
	admin := getCap(db, "user:"+person)
	if admin <= 0 {
		admin = getCap(db, capDefault)
	}
	own := getCap(db, "own:"+person)
	switch {
	case admin <= 0:
		return own
	case own <= 0 || own > admin:
		return admin
	}
	return own
}

// OverCap is why work under ctx may not spend extra more: the person paying
// has reached their monthly cap, or the key that asked has reached its own.
// "" when it may (extra 0 asks whether anything is left at all).
func OverCap(ctx context.Context, extra float64) string {
	s := SpenderFrom(ctx)
	if s.Person == "" {
		return ""
	}
	db := ledgerDB()
	if db == nil {
		return ""
	}
	row := spentIn(db, ThisMonth(), s.Person)
	over := func(spent, cap float64) bool {
		if cap <= 0 {
			return false
		}
		if extra > 0 {
			return spent+extra > cap
		}
		return spent >= cap
	}
	if cap := personCap(db, s.Person); over(row.Total, cap) {
		return fmt.Sprintf("the monthly spending limit of %s is reached ($%.2f of $%.2f this month)", s.Person, row.Total, cap)
	}
	if s.Key != "" {
		if cap := getCap(db, "key:"+s.Key); over(row.Keys[s.Key], cap) {
			return fmt.Sprintf("this key's monthly spending limit is reached ($%.2f of $%.2f this month)", row.Keys[s.Key], cap)
		}
	}
	return ""
}
