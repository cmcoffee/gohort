package sections

import (
	"net/http"
	"sort"
	"sync"

	"github.com/cmcoffee/gohort/core/ui"
)

// IntegrationKind is one kind of thing a person connects for themselves (an
// API, an MCP server, a webhook, a Teams bot, a linked service), listed with
// all the others under Extensions > Integrations and added from its one Add.
//
// A kind is its app's own endpoints, named here so the Extensions page can
// show and add every kind without importing any of them: it reads the kind's
// list through List, adds through Add, and puts the kind's own row actions
// on its rows. The records stay where they are; nothing is stored here.
type IntegrationKind struct {
	// Name tells the kind from the others. It is stable, a lowercase word,
	// since its form fields and row fields are told apart by it.
	Name string
	// Label is the kind as the list and the picker name it ("Teams bot").
	Label string
	// Blurb says in one line what one is, under the picker.
	Blurb string
	// Help is how to set one up, shown while one is being added.
	Help string
	// Order sorts the kinds, low to high.
	Order int

	// Allowed says whether user sees the kind at all: whether they may use
	// the app it belongs to. Nil is everybody.
	Allowed func(r *http.Request, user string) bool
	// CanAdd says whether user may add one, and anything to say about it
	// ("A new one waits for an administrator's approval."). Nil is yes.
	CanAdd func(user string) (ok bool, note string)

	// Fields is the add form, as the kind's own Add dialog had it. Nil is
	// a kind that is not added here (one lent to the person, say).
	Fields func(user string) []ui.FormField
	// Defaults answers a GET with the add form's starting values. Optional.
	Defaults http.Handler
	// Add takes the add form's values as a JSON POST and answers as the
	// kind's own endpoint does.
	Add http.Handler
	// List answers a GET with the person's ones, a JSON array of objects.
	List http.Handler
	// Row says how a listed object reads in the shared list.
	Row func(rec map[string]any) IntegrationRow
	// Actions are the kind's row actions. Their show and hide conditions
	// read the kind's own fields, which the page keeps to the kind's rows.
	Actions []ui.RowAction
}

// IntegrationRow is what the shared list shows of one item.
type IntegrationRow struct {
	// Key tells it from the kind's others. Blank leaves it out of the list:
	// one another kind's row stands for, such as a Teams bot's own webhook.
	Key    string
	Title  string
	Detail string
	Status string // one of the Status values
}

// The statuses the list shows; blank is working.
const (
	StatusWaiting  = "waiting"   // waiting for an administrator's approval
	StatusNeedsKey = "needs_key" // has no key yet
	StatusOff      = "off"       // switched off, by its owner or an administrator
	StatusTrouble  = "trouble"   // its last check or call went wrong
)

var integrationKinds = struct {
	sync.Mutex
	by map[string]IntegrationKind
}{by: map[string]IntegrationKind{}}

// RegisterIntegrationKind adds kind k, or replaces the one of its name. Call
// it from the app's routes, where its handlers are made.
func RegisterIntegrationKind(k IntegrationKind) {
	integrationKinds.Lock()
	integrationKinds.by[k.Name] = k
	integrationKinds.Unlock()
}

// IntegrationKinds is every registered kind, by Order then Name.
func IntegrationKinds() []IntegrationKind {
	integrationKinds.Lock()
	out := make([]IntegrationKind, 0, len(integrationKinds.by))
	for _, k := range integrationKinds.by {
		out = append(out, k)
	}
	integrationKinds.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Name < out[j].Name
	})
	return out
}
