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
	// Second, because refuseUnpassedPeople has the better advice when both
	// apply: "you have their picture, pass it" beats "you have no picture of
	// anyone" for a caller who does.
	if err := refuseInventedReference(sess, out, refs); err != nil {
		return prompt, "", err
	}
	return out, scrubNote(replaced), nil
}

// referenceClaims are the phrases a prompt asserts a source picture with: that
// it SHOWS someone, and that the render should carry that likeness across.
//
// The same two limits the name scrub documents, for the same reasons. This
// cannot catch a paraphrase — "make him look like he does in the picture" is
// the claim in words nothing here matches — and it is not trying to. It catches
// the phrasing an agent reaches for when it believes it is holding a photograph
// of somebody, which is the belief the check exists to interrupt. A miss costs
// a bad render; a false positive costs a round, so the list stays short and the
// escape is stated in the refusal.
var referenceClaims = []string{
	"reference photo",
	"reference image",
	"reference picture",
	"reference shot",
	"same person",
	"their face",
	"his face",
	"her face",
	"my face",
	"the face",
	"person's face",
	"recognizable",
	"likeness",
	"looks like them",
	"look like them",
}

// referenceClaim returns the phrase a prompt uses to assert its sources depict
// a real person, "" when it makes no such claim.
func referenceClaim(prompt string) string {
	low := strings.ToLower(prompt)
	for _, c := range referenceClaims {
		if strings.Contains(low, c) {
			return c
		}
	}
	return ""
}

// refuseInventedReference stops a render that treats the agent's OWN output as
// a photograph of a real person.
//
// The library already refuses to OFFER an agent-made picture as a reference —
// keptRefsFor drops them, PeopleWithPictures drops them, and keeping one says
// so in as many words. Nothing enforced it at the call, so the one path that
// mattered was open: the ring lists the agent's own renders (it has to — "edit
// the one you just made" is the common case), and a ref taken from there went
// straight through as a source with no check of where it came from.
//
// Observed: an agent with no photograph of anyone rendered a portrait
// unprompted, then on a later turn passed that render back in as "the reference
// photo" and asked for the face to stay recognizable. Nothing was recognizable.
// The face was invented in the first render and the second one inherited it,
// which is the compounding ImageOrigin was written to describe.
//
// TWO CONDITIONS, and both are needed.
//
// EVERY source is agent-made. One real photograph in the set means the call has
// something true to work from, and a composite that draws a likeness from the
// real one and a background from a render is exactly right.
//
// And the prompt CLAIMS a likeness. Without that this is iteration — "make it
// snowy", "warmer light", "lose the hat" — which is what the ring is for and
// what the whole edit path is built around. Refusing that would break the
// tool's most ordinary use to prevent its rarest.
func refuseInventedReference(sess *ToolSession, prompt string, refs []string) error {
	if len(refs) == 0 || strings.TrimSpace(prompt) == "" {
		return nil
	}
	for _, r := range refs {
		if !OriginForRef(sess, r).AgentMade() {
			return nil // something real in the set — nothing to launder
		}
	}
	claim := referenceClaim(prompt)
	if claim == "" {
		return nil // iterating on your own render, which is the point of the ring
	}
	fix := "If you meant to change a picture you made, say what should CHANGE and drop the likeness wording — it is your render, so there is no likeness to keep."
	if given := givenRefsFor(sess); given != "" {
		fix = "Pass a picture you were GIVEN instead: " + given + ". " + fix
	} else {
		fix = "You have not been given a picture of anyone, so there is no reference to pass — ask for one. " + fix
	}
	return fmt.Errorf("nothing was rendered. Your prompt says %q, but every picture you passed is one you MADE yourself. "+
		"A generated face is not a photograph of anyone: it was invented, and using it as the reference for another render "+
		"inherits the invention and drifts further from whoever it was meant to be. %s", claim, fix)
}

// givenRefsFor names the pictures in the ring that came from somewhere real, as
// a short list for the refusal above. Empty when there are none — which is its
// own answer, and gets a different sentence.
//
// Stable ids only. The refusal is telling the model what to pass on the retry,
// and a position would be stale by the time it does: the entries are listed
// newest-first and anything saved in between renumbers them.
func givenRefsFor(sess *ToolSession) string {
	const maxGiven = 4
	var out []string
	for _, r := range RecentImages(sess) {
		if r.Origin.AgentMade() || strings.TrimSpace(r.ID) == "" {
			continue
		}
		label := r.ID
		if d := strings.TrimSpace(r.Caption); d != "" {
			label += " (" + truncate(d, 60) + ")"
		} else if d := strings.TrimSpace(r.Note); d != "" {
			label += " (" + truncate(d, 60) + ")"
		}
		if out = append(out, label); len(out) == maxGiven {
			break
		}
	}
	return strings.Join(out, ", ")
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
