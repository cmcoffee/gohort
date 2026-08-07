// Taking people's names back out of a render prompt.
//
// The schema asks the model not to put them there, and asking is where this
// started. Asking is not enough on its own: a name is the most natural way to
// refer to somebody, the model has just been shown a manifest full of names,
// and a prompt reading "Rory on a beach" looks correct to everything except the
// renderer — which has no idea who that is and draws whoever it thinks the name
// looks like, standing next to nothing you supplied.
//
// So a name whose picture is ATTACHED gets rewritten to where that picture is:
// "the person in the first image". True by construction, since the substitution
// only happens when the reference is in this very call, and it says exactly what
// the guidance asks the model to say.
//
// TWO DELIBERATE LIMITS.
//
// Only people. A pet's or a place's name is a useful noun to a renderer and
// carries no identity prior to fight with; the failure being fixed is faces.
//
// Only capitalized whole-word matches. Names collide with ordinary words — Mark,
// Rose, May, Art, Bill — and rewriting "put a mark on it" would be far worse
// than missing a lowercase "rory". False negatives cost a nudge; false positives
// corrupt the request.
//
// What this CANNOT do is catch a description. "a man with a short beard" is
// prose, indistinguishable from any other prose, and competing with the
// reference just as hard. The schema still carries that half.
package imagefetch

import (
	"fmt"
	"strings"
	"unicode"

	. "github.com/cmcoffee/gohort/core"
)

// minScrubName is the shortest name worth matching. Two-letter names exist, but
// two-letter words are everywhere and the collision rate is not survivable.
const minScrubName = 3

// positionWord names an image by where it sits in the call. Words for the first
// few because that is how a person refers to them and how the schema's examples
// are written; a number past that, since "the seventh image" reads worse than
// what it replaces.
func positionWord(i int) string {
	switch i {
	case 0:
		return "first"
	case 1:
		return "second"
	case 2:
		return "third"
	case 3:
		return "fourth"
	}
	return fmt.Sprintf("#%d", i+1)
}

// scrubSubjectNames rewrites the names of attached people into positions.
//
// subjects is index-aligned with the call's images list. Returns the rewritten
// prompt and the names that were replaced, in the order they were found, for
// the note that tells the model what happened — a silent rewrite would leave it
// believing the prompt it wrote is the prompt that ran.
func scrubSubjectNames(prompt string, subjects []ImageSubject) (string, []string) {
	out, replaced := prompt, []string(nil)
	for i, s := range subjects {
		if !s.Person {
			continue
		}
		name := strings.TrimSpace(s.Name)
		if len([]rune(name)) < minScrubName {
			continue
		}
		var hit bool
		// Possessive first. Replacing the bare name would leave a dangling
		// "'s" attached to a phrase that already ends in a noun, turning "the
		// person in the first image's face" into something that reads as the
		// image's face rather than the person's.
		for _, poss := range []string{name + "'s", name + "’s"} {
			if next, ok := replaceWord(out, poss, "the "+positionWord(i)+" image's"); ok {
				out, hit = next, true
			}
		}
		if next, ok := replaceWord(out, name, "the person in the "+positionWord(i)+" image"); ok {
			out, hit = next, true
		}
		if hit {
			replaced = append(replaced, name)
		}
	}
	return out, replaced
}

// replaceWord swaps every capitalized whole-word occurrence of word.
//
// Whole-word so "Roryish" and "Marketing" survive; capitalized so an ordinary
// lowercase noun that happens to be somebody's name survives too. Reports
// whether anything changed, because the caller has to know to say so.
func replaceWord(s, word, with string) (string, bool) {
	if word == "" || !startsUpper(word) {
		return s, false
	}
	var b strings.Builder
	changed, i := false, 0
	for i < len(s) {
		idx := strings.Index(s[i:], word)
		if idx < 0 {
			break
		}
		at := i + idx
		if wordBoundary(s, at, len(word)) {
			b.WriteString(s[i:at])
			b.WriteString(with)
			changed = true
			i = at + len(word)
			continue
		}
		// Not a whole word — copy through the match and keep looking, so an
		// embedded occurrence cannot stall the scan.
		b.WriteString(s[i : at+len(word)])
		i = at + len(word)
	}
	if !changed {
		return s, false
	}
	b.WriteString(s[i:])
	return b.String(), true
}

// wordBoundary reports whether the match at `at` stands alone. Letters and
// digits on either side mean it is part of a longer word; punctuation and space
// mean it is not.
func wordBoundary(s string, at, n int) bool {
	if at > 0 {
		r := rune(s[at-1])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	if end := at + n; end < len(s) {
		r := rune(s[end])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func startsUpper(s string) bool {
	for _, r := range s {
		return unicode.IsUpper(r)
	}
	return false
}

// scrubNote tells the model what was rewritten and why.
//
// Reported rather than done quietly: the next prompt it writes should not need
// this, and a model that never learns its prompt was changed will write the
// same one every time. Phrased as a correction that already succeeded, so it
// does not read as a failure worth retrying.
func scrubNote(replaced []string) string {
	if len(replaced) == 0 {
		return ""
	}
	who := strings.Join(replaced, ", ")
	return fmt.Sprintf(" NOTE: your prompt named %s. Their picture was attached, so the name was replaced with which image they are in before the render — "+
		"a name means nothing to the renderer, and at worst pulls in whoever it thinks that name looks like, which is how an attached reference gets ignored. "+
		"Say where someone is, not who they are: \"the person in the first image\".", who)
}

// checkPromptSubjects runs both halves BEFORE anything is dispatched, and is
// the reason this is a render-time check rather than a render-time complaint.
//
// The first version noted the un-passed case after the fact. That is too late
// in the way that matters: the render has already happened, it cost a minute
// and a backend call, it came back with a stranger's face, and a note under it
// is something the model can read and ship the picture anyway. Caught here,
// nothing is spent and the retry is right.
//
// Returns the prompt to actually use, a note about what was rewritten, and an
// error when the call should not proceed at all.
func checkPromptSubjects(sess *ToolSession, prompt string, refs []string) (string, string, error) {
	attached := SubjectsForRefs(sess, refs)
	out, replaced := scrubSubjectNames(prompt, attached)
	if err := refuseUnpassedPeople(sess, out, attached); err != nil {
		return prompt, "", err
	}
	return out, scrubNote(replaced), nil
}

// refuseUnpassedPeople stops a render whose prompt names somebody this agent
// HAS a picture of and did not attach.
//
// A refusal rather than a note because there is no way to fix it in flight —
// no image to point the name at — and because the render would otherwise
// produce a confident, deliverable picture of the wrong person. One extra round
// costs a few seconds; a wrong face gets sent to someone.
//
// The escape is stated in the message. A capitalized word can be somebody's
// name and an ordinary noun at once ("Rose garden"), and a check that can only
// be satisfied one way turns a false positive into a loop.
func refuseUnpassedPeople(sess *ToolSession, prompt string, attached []ImageSubject) error {
	people := PeopleWithPictures(sess)
	if len(people) == 0 || strings.TrimSpace(prompt) == "" {
		return nil
	}
	var missed []string
	for _, k := range people {
		if subjectAttached(k.Subject, attached) {
			continue
		}
		name := strings.TrimSpace(k.Subject.Name)
		if len([]rune(name)) < minScrubName || !startsUpper(name) {
			continue
		}
		if _, found := replaceWord(prompt, name, name); found {
			missed = append(missed, fmt.Sprintf("%s → pass %s", name, k.Ref))
		}
	}
	if len(missed) == 0 {
		return nil
	}
	return fmt.Errorf("nothing was rendered. Your prompt names %s, and you HAVE a picture of them — rendering now would invent a face instead of using theirs, which is the whole failure the library exists to prevent. "+
		"Put the id in images and take the name out of the prompt; refer to them by position instead (\"the person in the first image\"). "+
		"If that word did not mean a person here, reword it so it is not capitalized and call again",
		strings.Join(missed, ", "))
}

func subjectAttached(s ImageSubject, attached []ImageSubject) bool {
	for _, a := range attached {
		if SameSubject(a, s) {
			return true
		}
	}
	return false
}
