package orchestrate

// Dates on what an agent keeps to read later. A note, a board post or a line
// in its cortex saying "closes Monday" is read days on, when "Monday" means
// a different day; an agent repeated a free weekend a week after it ended
// that way. So at the moment of writing, every relative day word with one
// reading gets its date beside it, in the person's own time zone, and one
// that could mean two days (a weekday that is today, "next Friday") goes back
// to the agent to write the date itself (textutil.ResolveRelativeDates).

import (
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/textutil"
)

// datedText is text with its relative day words dated, or an error naming the
// ones with two readings, for the agent to write as dates and save again.
func (t *chatTurn) datedText(text string) (string, error) {
	return datedFor(t.user)(text)
}

// datedFor is datedText for a writer with no turn in hand, in user's zone.
func datedFor(user string) func(string) (string, error) {
	return func(text string) (string, error) {
		now := time.Now().In(UserLocation(user))
		out, amb := textutil.ResolveRelativeDates(text, now)
		if len(amb) == 0 {
			return out, nil
		}
		var parts []string
		for _, a := range amb {
			parts = append(parts, fmt.Sprintf("%q could be %s", a.Phrase, a.Why))
		}
		return "", fmt.Errorf("NOT saved: %s. Today is %s. Write the date itself (for example \"Monday, Oct 12\") in place of the word, and save again",
			strings.Join(parts, "; "), now.Format("Monday, Jan 2, 2006"))
	}
}
