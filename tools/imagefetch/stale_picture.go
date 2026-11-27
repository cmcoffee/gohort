package imagefetch

// An edit of a person's picture that came in a while ago stops once to check
// it is the one meant.
//
// "Can you take this picture and replace the lady with Craig" reached WiWee
// without its picture (the picture was judged on its own and held back), and
// the edit changed the newest picture WiWee had: a different photo, sent long
// before, with no lady in it. Nothing in the call was wrong by itself. What
// stood out was that "this picture" was a person's picture from well before
// the message, so the first edit of one now comes back with when it came in,
// who sent it and what it shows, and the agent either makes the same call
// again or asks for the picture.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// stalePictureAge is how long ago a person's picture may have come in before
// an edit of it checks first. A picture sent with the message, or just before
// it, is younger than this.
const stalePictureAge = 15 * time.Minute

// checkedPictures are the pictures an edit has already checked, by
// conversation, so the second call goes ahead. In memory: a restart checks
// again, once.
var checkedPictures = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

// stalePictureCheck is the check before an edit whose picture is a person's
// and came in more than stalePictureAge before now, the first time it is
// edited in this conversation. nil for any other edit.
func stalePictureCheck(sess *ToolSession, args map[string]any, now time.Time) error {
	if sess == nil || !editsPicture(args) {
		return nil
	}
	refs := imageSources(args)
	if len(refs) == 0 {
		return nil
	}
	ring := RecentImages(sess)
	idx := -1
	for i, r := range ring {
		if strings.EqualFold(refs[0], r.Ref) || (r.ID != "" && strings.EqualFold(refs[0], r.ID)) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil // a kept picture, a file, a URL: chosen by name, not by being newest
	}
	pic := ring[idx]
	if pic.Origin != ImageFromUser || pic.When.IsZero() || now.Sub(pic.When) < stalePictureAge {
		return nil
	}
	key := sess.Username + "\x00" + firstNonEmpty(sess.ChatSessionID, sess.DeliverySessionID) + "\x00" + firstNonEmpty(pic.ID, pic.Ref)
	checkedPictures.Lock()
	for k, at := range checkedPictures.m {
		if now.Sub(at) > 24*time.Hour {
			delete(checkedPictures.m, k)
		}
	}
	_, done := checkedPictures.m[key]
	if !done {
		checkedPictures.m[key] = now
	}
	checkedPictures.Unlock()
	if done {
		return nil
	}
	name := firstNonEmpty(pic.ID, pic.Ref)
	var b strings.Builder
	fmt.Fprintf(&b, "not edited yet: check this is the picture meant. %s came in %s ago", name, ageWords(now.Sub(pic.When)))
	if note := strings.TrimSpace(pic.Note); note != "" {
		fmt.Fprintf(&b, " (%s)", note)
	}
	if c := strings.TrimSpace(pic.Caption); c != "" {
		fmt.Fprintf(&b, ", and shows: %s", c)
	}
	b.WriteString(". ")
	for _, r := range ring[:idx] {
		if r.Origin == ImageFromUser {
			fmt.Fprintf(&b, "A newer picture from a person is %s", firstNonEmpty(r.ID, r.Ref))
			if c := strings.TrimSpace(r.Caption); c != "" {
				fmt.Fprintf(&b, " (%s)", c)
			}
			b.WriteString(". ")
			break
		}
	}
	b.WriteString("If the person said \"this picture\" and their message carried none, the one they meant has not arrived: ask them to send it again rather than editing an older one. If this is the one they mean, make the same call again and it goes ahead.")
	return fmt.Errorf("%s", b.String())
}

// ageWords is a picture's age as a person says it.
func ageWords(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "an hour"
		}
		return fmt.Sprintf("%d hours", h)
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
