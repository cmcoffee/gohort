package agenthost

// People outside gohort: one person on a platform that calls an agent through
// an outside-audience key (docs/design/outside-audience.md).
//
// Each such person runs in a store of their own, the way a shared agent's
// recipient and a servitor appliance already do: the turn's runtime user is a
// synthetic name, so their notes, findings, history and threads live apart
// from the owner's and from every other person's, and the owner's agent memory
// sits underneath read-only. The name holds ":", which no account name may
// (core/accountname), so it can never be a real account's store.

import (
	"errors"
	"fmt"
	"strings"
)

// outsidePrefix opens every outside person's runtime user name.
const outsidePrefix = "outside:"

// MaxOutsidePersonLen bounds the id a platform names its user by.
const MaxOutsidePersonLen = 128

// ErrNotForOutsideAudience refuses an outside person's turn on an agent whose
// owner has not set it to serve people outside.
var ErrNotForOutsideAudience = errors.New("this agent is not set to serve people outside gohort: its owner turns on \"Serves people outside gohort\" on the agent's Access page")

// OutsideScope is the runtime user an outside person's turns run as: one
// store per source (the API key's id) and person. Empty when either is.
func OutsideScope(source, person string) string {
	source, person = strings.TrimSpace(source), strings.TrimSpace(person)
	if source == "" || person == "" {
		return ""
	}
	return outsidePrefix + source + ":" + person
}

// IsOutsideScope reports whether a runtime user is an outside person.
func IsOutsideScope(user string) bool {
	return strings.HasPrefix(strings.TrimSpace(user), outsidePrefix)
}

// CheckOutsidePerson refuses an id that cannot name a store safely: empty,
// longer than MaxOutsidePersonLen, or holding anything but ASCII letters,
// digits and @ . _ + - |. Case is kept, since a platform's ids may differ by
// it. Refused rather than cleaned up, so two different people can never be
// folded into one store by the cleaning.
func CheckOutsidePerson(id string) error {
	if strings.TrimSpace(id) != id || id == "" {
		return errors.New("the end user's id is empty or has spaces around it")
	}
	if len(id) > MaxOutsidePersonLen {
		return fmt.Errorf("the end user's id is longer than %d characters", MaxOutsidePersonLen)
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("@._+-|", r):
		default:
			return fmt.Errorf("the end user's id may hold only letters, digits and @ . _ + - | (not %q)", r)
		}
	}
	return nil
}
