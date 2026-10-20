package promotion

// Everyone in a share list is a request.
//
// Naming people, or a team, is the owner's own call: they know who gets it.
// Everyone is not. It reaches every account in the deployment, including ones
// that do not exist yet, so widening to it is filed for an administrator, the
// same queue a published tool or app goes through. Narrowing is always the
// owner's: taking Everyone out, or lowering what it may do, applies at once
// and withdraws anything still waiting.
//
// A kind with levels (a guide's view and edit) passes them in rising order,
// and the request names the level: "<id>:edit". A kind with none files under
// the bare id and its only level is EveryoneOn.

import "strings"

// EveryoneOn is the one level of a kind that has no levels.
const EveryoneOn = "on"

// EveryoneAsk is one save of a share list, as far as Everyone goes.
type EveryoneAsk struct {
	Kind   string   // the promotion kind: "guide", "system", "pipeline"
	Owner  string   // the record's owner, whoever is saving
	ID     string   // what the request is filed under
	Note   string   // shown to the administrator: what it is, by name
	Levels []string // rising; nil means just EveryoneOn
	Want   string   // what the list asks for now; "" is none
	Has    string   // what is granted now; "" is none
	Admin  bool     // the saver is an administrator: applied, never asked
}

func (a EveryoneAsk) levels() []string {
	if len(a.Levels) == 0 {
		return []string{EveryoneOn}
	}
	return a.Levels
}

// rank is a level's place, 0 for none, -1 for one the kind does not have.
func (a EveryoneAsk) rank(level string) int {
	if level == "" {
		return 0
	}
	for i, l := range a.levels() {
		if l == level {
			return i + 1
		}
	}
	return -1
}

// requestName is what a request for level is filed under.
func (a EveryoneAsk) requestName(level string) string {
	if len(a.Levels) == 0 {
		return a.ID
	}
	return a.ID + ":" + level
}

// EveryonePending is the level a request is waiting on, or "".
func EveryonePending(db Store, a EveryoneAsk) string {
	if db == nil {
		return ""
	}
	for _, l := range a.levels() {
		if PendingPromotion(db, a.Owner, a.Kind, a.requestName(l)) {
			return l
		}
	}
	return ""
}

// withdrawExcept drops every waiting request but keep's.
func (a EveryoneAsk) withdrawExcept(db Store, keep string) {
	for _, l := range a.levels() {
		if l != keep {
			WithdrawPromotionRequest(db, a.Owner, a.Kind, a.requestName(l))
		}
	}
}

// AskEveryone settles one save and returns the level to store now: Want when
// it narrows or an administrator is saving, else Has, with Want filed for an
// administrator to approve. A level the kind does not have reads as none.
func AskEveryone(db Store, a EveryoneAsk) (string, error) {
	a.Want, a.Has = strings.TrimSpace(a.Want), strings.TrimSpace(a.Has)
	if a.rank(a.Want) < 0 {
		a.Want = ""
	}
	if a.rank(a.Has) < 0 {
		a.Has = ""
	}
	if db == nil {
		if a.rank(a.Want) <= a.rank(a.Has) || a.Admin {
			return a.Want, nil
		}
		return a.Has, nil
	}
	if a.rank(a.Want) <= a.rank(a.Has) || a.Admin {
		a.withdrawExcept(db, "")
		return a.Want, nil
	}
	if EveryonePending(db, a) == a.Want {
		return a.Has, nil // asked already; a re-save does not re-date it
	}
	a.withdrawExcept(db, a.Want)
	if err := CreatePromotionRequest(db, a.Owner, a.Kind, a.requestName(a.Want), a.Note); err != nil {
		return a.Has, err
	}
	return a.Has, nil
}

// ParseEveryoneRequest splits a filed name back into the id and the level.
// A kind with levels names them; a bare id is EveryoneOn.
func ParseEveryoneRequest(name string) (id, level string) {
	if i := strings.LastIndexByte(name, ':'); i >= 0 {
		return name[:i], name[i+1:]
	}
	return name, EveryoneOn
}
