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
