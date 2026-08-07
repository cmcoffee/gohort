package core

// A face library keyed on a display name is a library anyone can take over by
// renaming themselves, and one that lets three pictures of the same person
// accumulate has only pushed "which image is what" down a level. These pin both.

import (
	"strings"
	"testing"
)

// subjectSession is a session with a speaker, which is what lets a keep anchor
// to a handle at all.
func subjectSession(t *testing.T, name, handle string, owner bool) *ToolSession {
	t.Helper()
	sess := imageSpaceSession(t)
	sess.AgentID = "agent1"
	sess.SpeakerName, sess.SpeakerHandle, sess.SpeakerIsOwner = name, handle, owner
	return sess
}

func TestSubjectAnchorsToTheSpeakersHandle(t *testing.T) {
	sess := subjectSession(t, "Rory", "+15550199", false)

	// The agent names the person it is talking to: the handle comes from the
	// transport, never from the agent.
	got := ResolveKeepSubject(sess, "Rory", true)
	if got.Handle != "+15550199" {
		t.Errorf("a keep about the speaker should carry their handle, got %q", got.Handle)
	}
	if !got.Person || got.Name != "Rory" {
		t.Errorf("subject = %+v", got)
	}
	if got.Owner {
		t.Error("a non-owner speaker must not be marked owner")
	}

	// Somebody who is NOT talking gets a name and nothing else. That is not a
	// failure — it is the honest answer, and the manifest says so out loud.
	absent := ResolveKeepSubject(sess, "Henry", true)
	if absent.Handle != "" {
		t.Errorf("a subject nobody attributed must have no handle, got %q", absent.Handle)
	}
	if !absent.Named() {
		t.Error("a name-only subject is still a subject")
	}
	// And no subject at all stays empty, so an ordinary keep is unaffected.
	if s := ResolveKeepSubject(sess, "  ", true); s.Named() {
		t.Errorf("an empty of= is not a subject, got %+v", s)
	}
}

func TestOwnerFlagComesFromTheFrameworkNotTheName(t *testing.T) {
	sess := subjectSession(t, "Craig", "+15550100", true)
	if s := ResolveKeepSubject(sess, "Craig", true); !s.Owner {
		t.Error("the framework said this speaker is the owner; the subject should say so")
	}
	// The same NAME on a session where the framework did not say owner must not
	// become the owner. This is the whole reason the flag is carried rather than
	// recomputed from the name at read time.
	other := subjectSession(t, "Craig", "+15559999", false)
	if s := ResolveKeepSubject(other, "Craig", true); s.Owner {
		t.Error("a matching display name must never confer owner")
	}
}

func TestIdentityIsTheHandleNotTheName(t *testing.T) {
	craig := ImageSubject{Person: true, Name: "Craig", Handle: "+15550100"}
	// The attack this exists to stop: a third party renames themselves to the
	// owner's display name. Same name, different handle, must stay separate.
	impostor := ImageSubject{Person: true, Name: "Craig", Handle: "+15550199"}
	if SameSubject(craig, impostor) {
		t.Error("two handles are two people however they spell their names")
	}
	// The same person renaming themselves keeps ONE entry.
	renamed := ImageSubject{Person: true, Name: "C", Handle: "+15550100"}
	if !SameSubject(craig, renamed) {
		t.Error("one handle is one person however they spell their name")
	}
	// Nothing at all is not a key. Without this every unattributed picture
	// would count as the same subject and replace every other one.
	if SameSubject(ImageSubject{}, ImageSubject{}) {
		t.Error("an unnamed subject must not match another unnamed subject")
	}
	if SameSubject(ImageSubject{Name: "Bess"}, ImageSubject{}) {
		t.Error("a named subject must not match an unnamed one")
	}
	// With no handles on either side the name is all there is, and it decides.
	if !SameSubject(ImageSubject{Name: "Bess"}, ImageSubject{Name: "bess"}) {
		t.Error("name-only subjects match on the folded name")
	}
}

func TestOnePicturePerPerson(t *testing.T) {
	sess := subjectSession(t, "Rory", "+15550199", false)
	rory := ResolveKeepSubject(sess, "Rory", true)

	if _, err := RecordKeep(t, sess, "rory_beach", rory); err != nil {
		t.Fatal(err)
	}
	// A newer picture of the same person, kept under a DIFFERENT name. The old
	// one must go: two pictures of Rory is the ambiguity the subject removes.
	if _, err := RecordKeep(t, sess, "rory_office", rory); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, k := range KeptImages(sess) {
		names = append(names, k.Name)
	}
	if len(names) != 1 || names[0] != "rory_office" {
		t.Fatalf("one current picture per person, got %v", names)
	}

	// A DIFFERENT person is untouched by that rule.
	henry := ImageSubject{Person: true, Name: "Henry"}
	if _, err := RecordKeep(t, sess, "henry_1", henry); err != nil {
		t.Fatal(err)
	}
	if got := len(KeptImages(sess)); got != 2 {
		t.Errorf("two people = two pictures, got %d", got)
	}
	// And an ordinary keep with no subject never supersedes anything.
	if _, err := RecordKeep(t, sess, "brand_mark", ImageSubject{}); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordKeep(t, sess, "house_style", ImageSubject{}); err != nil {
		t.Fatal(err)
	}
	if got := len(KeptImages(sess)); got != 4 {
		t.Errorf("subjectless keeps must not replace each other, got %d", got)
	}
}

func TestManifestSeparatesPeopleAndRefusesToInventFaces(t *testing.T) {
	sess := subjectSession(t, "Rory", "+15550199", false)
	if _, err := RecordKeep(t, sess, "rory", ResolveKeepSubject(sess, "Rory", true)); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordKeep(t, sess, "henry", ImageSubject{Person: true, Name: "Henry"}); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordKeep(t, sess, "brand_mark", ImageSubject{}); err != nil {
		t.Fatal(err)
	}
	m := KeptImageManifest(sess)

	people := strings.Index(m, "People you have a picture of")
	other := strings.Index(m, "Other images you have kept")
	if people < 0 || other < 0 {
		t.Fatalf("people and things need separate headings, got:\n%s", m)
	}
	if people > other {
		t.Errorf("people lead — they answer the question a named request asks:\n%s", m)
	}
	// An anchored identification shows the handle; an unanchored one says it is
	// only a label. Reading them the same way is how a guess becomes a fact.
	if !strings.Contains(m, "+15550199") {
		t.Errorf("an anchored subject should show what it is anchored to:\n%s", m)
	}
	if !strings.Contains(m, "name only") {
		t.Errorf("an unanchored subject must say so:\n%s", m)
	}
	// The rule that stops the whole failure: no face invented for someone we
	// have no picture of.
	if !strings.Contains(m, "Never render a face from a description") {
		t.Errorf("the manifest must forbid inventing a face:\n%s", m)
	}
	// The brand mark is not a person and must not be offered as a likeness.
	if strings.Contains(m[people:other], "brand_mark") {
		t.Errorf("a logo is not a person:\n%s", m)
	}
}

// A picture the agent MADE is not evidence of what anyone looks like, and that
// matters more for a face than for a logo, not less.
func TestAMadePictureOfAPersonIsStillNotAReference(t *testing.T) {
	sess := subjectSession(t, "Rory", "+15550199", false)
	ref := RecordRecentImage(sess, testPNG(t, 8, 8), "generated: a man on a beach", ImageFromGenerated)
	if _, err := KeepImageOf(sess, ref, "fake_rory", "", ImageSubject{Person: true, Name: "Rory"}); err != nil {
		t.Fatal(err)
	}
	m := KeptImageManifest(sess)
	if !strings.Contains(m, "MADE BY YOU") {
		t.Errorf("provenance must survive being given a subject:\n%s", m)
	}
}

// RecordKeep puts one picture in the ring and keeps it under name with subject.
func RecordKeep(t *testing.T, sess *ToolSession, name string, subject ImageSubject) (KeptImage, error) {
	t.Helper()
	ref := RecordRecentImage(sess, testPNG(t, 8, 8), "received from a contact", ImageFromUser)
	if ref == "" {
		t.Fatal("could not stage a picture to keep")
	}
	return KeepImageOf(sess, ref, name, "", subject)
}

// The prompt is where a good reference gets thrown away. An agent that passes
// the picture AND writes "Rory, a man with a short beard, on a beach" hands the
// renderer two subjects and it draws the words — so the manifest has to say to
// leave them out, not merely to pass the id.
func TestPeopleManifestSaysToKeepThemOutOfThePrompt(t *testing.T) {
	sess := subjectSession(t, "Rory", "+15550199", false)
	if _, err := RecordKeep(t, sess, "rory", ResolveKeepSubject(sess, "Rory", true)); err != nil {
		t.Fatal(err)
	}
	m := KeptImageManifest(sess)
	if !strings.Contains(m, "leave them OUT of the prompt") {
		t.Errorf("the manifest must say the subject does not belong in the prompt:\n%s", m)
	}
	if !strings.Contains(m, "draw the words") {
		t.Errorf("it must say WHY, or it reads as an arbitrary rule:\n%s", m)
	}
}

// Unknown is not "given". Entries kept before provenance was recorded read back
// unknown, and unknown used to print under the heading that says these are
// real — so a library built up over months presented every legacy render as a
// photograph. Asked for three reference photos, an agent delivered three of its
// own pictures and said they were real, because the manifest told it they were.
func TestUnrecordedOriginIsNotPresentedAsReal(t *testing.T) {
	sess := subjectSession(t, "Rory", "+15550199", false)
	ref := RecordRecentImage(sess, testPNG(t, 8, 8), "from somewhere", ImageOriginUnknown)
	if _, err := KeepImageOf(sess, ref, "rory", "Real photo of Rory", ImageSubject{Person: true, Name: "Rory"}); err != nil {
		t.Fatal(err)
	}
	m := KeptImageManifest(sess)
	if !strings.Contains(m, "ORIGIN NOT RECORDED") {
		t.Errorf("an unrecorded origin must say so:\n%s", m)
	}
	if !strings.Contains(m, "NO RECORDED ORIGIN") {
		t.Errorf("the people section needs the caveat too:\n%s", m)
	}
	// The agent's own note claiming it is real must not settle the question —
	// that is prose it wrote, not a record of where the pixels came from.
	if !strings.Contains(m, "Do not call it a photo") {
		t.Errorf("a confident note must not override missing provenance:\n%s", m)
	}
}

// A recorded origin still reads cleanly — the caveat must not attach itself to
// entries that actually know where they came from.
func TestARecordedOriginCarriesNoCaveat(t *testing.T) {
	sess := subjectSession(t, "Rory", "+15550199", false)
	if _, err := RecordKeep(t, sess, "rory", ResolveKeepSubject(sess, "Rory", true)); err != nil {
		t.Fatal(err)
	}
	m := KeptImageManifest(sess)
	if strings.Contains(m, "ORIGIN NOT RECORDED") || strings.Contains(m, "NO RECORDED ORIGIN") {
		t.Errorf("a known-origin entry must not be caveated:\n%s", m)
	}
}
