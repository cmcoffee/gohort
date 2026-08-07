// Who a kept picture is OF.
//
// The library could already hold a headshot; what it could not do is say whose.
// Name, note and caption are all free text chosen by the agent, so "which image
// is what" came down to the model reading its own filenames back and guessing —
// and when a request named two people, guessing twice.
//
// IDENTITY KEYS ON THE HANDLE, NEVER THE DISPLAY NAME. Same rule the speaker
// grounding runs on, for the same reason: a display name is the sender's to
// choose, so a name-keyed library lets anyone claim to be anyone by renaming
// themselves, and two people who happen to share a first name collapse into one
// face. The handle is the transport's attribution and is not the sender's to
// pick.
//
// A name-only subject is still allowed and still useful — a pet, a house, a
// person who has never messaged in. It just cannot be authoritative about
// identity, and the parts of this that matter for identity check for a handle
// before they trust anything.
package core

import "strings"

// ImageSubject is who or what a kept picture depicts.
type ImageSubject struct {
	// Person separates "this is what Rory looks like" from "this is the logo".
	// It changes how the manifest presents the entry and, more importantly,
	// what the agent is told it may do with it.
	Person bool `json:"person,omitempty"`
	// Name is for prose — what to call them when talking to someone.
	Name string `json:"name,omitempty"`
	// Handle is the identity. Empty when the subject never messaged in, which
	// is not an error: it means this entry cannot be matched to a speaker, only
	// to a name someone typed.
	Handle string `json:"handle,omitempty"`
	// Owner marks the person the agent works for. Worth its own field rather
	// than a name comparison at read time — the owner check belongs to the
	// layer that has the trusted handle, not to whoever renders a manifest.
	Owner bool `json:"owner,omitempty"`
}

// Named reports whether the subject says anything at all.
func (s ImageSubject) Named() bool {
	return strings.TrimSpace(s.Name) != "" || strings.TrimSpace(s.Handle) != ""
}

// Key is what "one picture per subject" is one OF.
//
// The handle when there is one, so a person who renames themselves keeps a
// single entry rather than accumulating one per alias. Otherwise the folded
// name, which is the best available and is why a name-only subject is not
// treated as an identity anywhere it would matter.
//
// Returns "" for an unnamed subject, and callers must read that as "no key" —
// not as a key that happens to be empty, which would make every unattributed
// picture replace every other one.
func (s ImageSubject) Key() string {
	if h := strings.TrimSpace(s.Handle); h != "" {
		return "h:" + strings.ToLower(h)
	}
	if n := strings.TrimSpace(s.Name); n != "" {
		return "n:" + strings.ToLower(n)
	}
	return ""
}

// SameSubject reports whether two subjects are the same one.
//
// Deliberately NOT "the names look alike": two subjects with handles match only
// on the handle, so Rory renaming himself Craig does not take over Craig's
// entry. Only when neither side has a handle does the name decide, because then
// it is all there is.
func SameSubject(a, b ImageSubject) bool {
	ka, kb := a.Key(), b.Key()
	return ka != "" && ka == kb
}

// SubjectLabel is how a subject reads in a manifest: the name when there is
// one, else the handle, so an entry kept from a number nobody has named is
// still addressable rather than blank.
func SubjectLabel(s ImageSubject) string {
	if n := strings.TrimSpace(s.Name); n != "" {
		return n
	}
	return strings.TrimSpace(s.Handle)
}

// ResolveKeepSubject works out who a keep is of, from what the agent said and what
// the turn actually knows.
//
// The agent supplies a NAME — it is talking to people and that is what it has.
// The handle is never taken from the agent: it is attached here, by matching
// that name against the person whose message this turn is answering, whose
// handle arrived with the transport. So "keep this as a picture of Rory" while
// Rory is the one talking produces a handle-keyed entry, and the same call made
// about someone absent produces a name-only one that is honest about being a
// label rather than an identity.
//
// isPerson is the agent's own read of whether this is a person at all. Taken at
// its word: it can see the picture and this decides presentation, not
// permission.
func ResolveKeepSubject(sess *ToolSession, of string, isPerson bool) ImageSubject {
	name := strings.TrimSpace(of)
	if name == "" {
		return ImageSubject{}
	}
	s := ImageSubject{Person: isPerson, Name: name}
	if sess == nil {
		return s
	}
	// The speaker is the one person this turn can attribute with certainty.
	// Matched on the name the agent used, case-insensitively, because the agent
	// is quoting back the display name it was shown.
	if speaker := strings.TrimSpace(sess.SpeakerName); speaker != "" && strings.EqualFold(speaker, name) {
		s.Handle = strings.TrimSpace(sess.SpeakerHandle)
		s.Owner = sess.SpeakerIsOwner
	}
	return s
}

// SubjectForRef reports who the picture behind one reference is of.
//
// Only KEPT images carry a subject — a ring entry is positional and a media id
// is turn-scoped, and neither has anywhere to record one. So this answers for
// the library and returns the zero subject for everything else, which callers
// must read as "nobody said", never as "nobody".
func SubjectForRef(sess *ToolSession, ref string) ImageSubject {
	name := strings.TrimSpace(ref)
	if sess == nil || name == "" {
		return ImageSubject{}
	}
	name = strings.TrimPrefix(strings.ToLower(name), RecentImageRefPrefix)
	for _, k := range KeptImages(sess) {
		if strings.EqualFold(k.Name, name) {
			return k.Subject
		}
	}
	return ImageSubject{}
}

// SubjectsForRefs is SubjectForRef across a call's whole image list, index for
// index — so position N of the result describes position N of the refs, and a
// reference with no subject leaves a zero entry rather than shifting the rest.
// Callers turn that index into "the first image", so the alignment is the whole
// contract.
func SubjectsForRefs(sess *ToolSession, refs []string) []ImageSubject {
	out := make([]ImageSubject, len(refs))
	for i, r := range refs {
		out[i] = SubjectForRef(sess, r)
	}
	return out
}

// PeopleWithPictures lists every person the library holds a usable likeness of.
//
// Agent-made entries are excluded: a face this agent generated is not evidence
// of what anyone looks like, so offering it as the picture to pass would launder
// an invention into a reference — the same rule the manifest and the schema
// list already apply, applied here so a runtime check cannot undo it.
func PeopleWithPictures(sess *ToolSession) []KeptImage {
	var out []KeptImage
	for _, k := range KeptImages(sess) {
		if k.Subject.Person && k.Subject.Named() && !k.Origin.AgentMade() {
			out = append(out, k)
		}
	}
	return out
}

// SameDisplayedPerson reports whether two subjects would read to a PERSON as
// the same person.
//
// Deliberately looser than SameSubject, and deliberately not a replacement for
// it. SameSubject decides whether one entry REPLACES another, so it has to be
// strict: it is the impersonation guard, and a name match that reached across a
// handle boundary would let somebody retire the owner's picture of a third
// party by messaging in under the right display name.
//
// This decides only whether to WARN, which is a question with no destructive
// answer. So it catches the case the strict rule cannot: a name-only entry the
// owner labelled and a handle-anchored one the agent kept later are two rows
// for one person, and "the picture of Rory" is ambiguous again — the exact
// problem subjects were added to end.
func SameDisplayedPerson(a, b ImageSubject) bool {
	if !a.Person || !b.Person || !a.Named() || !b.Named() {
		return false
	}
	if ha, hb := strings.TrimSpace(a.Handle), strings.TrimSpace(b.Handle); ha != "" && hb != "" {
		// Both attributed: the handle is the answer, and two different handles
		// are two different people whatever they call themselves.
		return strings.EqualFold(ha, hb)
	}
	// At least one is a label rather than an identification, so the name is all
	// there is to go on — which is precisely why this is a warning and not a
	// merge.
	na, nb := strings.TrimSpace(a.Name), strings.TrimSpace(b.Name)
	return na != "" && strings.EqualFold(na, nb)
}

// SubjectCollisions maps each image's name to the refs of other images showing
// the same person, so a listing can say which row it is duplicating rather than
// only that it duplicates something.
//
// Quadratic, over a library capped at 50. Kept obvious rather than indexed:
// SameDisplayedPerson is not an equivalence relation (a name-only "Rory" can
// match two entries with different handles, which do not match each other), so
// grouping by a computed key would quietly give a different — and wrong —
// answer.
func SubjectCollisions(all []KeptImage) map[string][]string {
	out := map[string][]string{}
	for i, a := range all {
		for j, b := range all {
			if i == j {
				continue
			}
			if SameDisplayedPerson(a.Subject, b.Subject) {
				out[a.Name] = append(out[a.Name], b.Ref)
			}
		}
	}
	return out
}
