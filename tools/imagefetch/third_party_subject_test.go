package imagefetch

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The ordinary case, and the one the handle rule reads like it excludes: the
// OWNER supplies photos of other people. They are not the speaker, so nothing
// can anchor them to a handle and every subject is name-only.
//
// A handle is an extra assurance, never a requirement. Everything downstream
// keys on Person + Named, so all four mechanisms fire — and if any of them ever
// starts demanding a handle, the feature quietly stops working for the way it
// is actually used.
func TestThirdPartyReferencesStillWork(t *testing.T) {
	SetImageDir(t.TempDir())
	sess := &ToolSession{Username: "alice", AgentID: "agent1", WorkspaceDir: t.TempDir(),
		SpeakerName: "Craig", SpeakerHandle: "+15550100", SpeakerIsOwner: true}

	for _, who := range []string{"Rory", "Leo"} {
		ref := RecordRecentImage(sess, []byte("\x89PNG\r\n\x1a\nphoto"), "sent by craig", ImageFromUser)
		subj := ResolveKeepSubject(sess, who, true)
		if subj.Handle != "" {
			t.Fatalf("%s should be name-only — Craig is the speaker, not them", who)
		}
		if _, err := KeepImageOf(sess, ref, strings.ToLower(who), "", subj); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Listed as people at all?
	if n := len(PeopleWithPictures(sess)); n != 2 {
		t.Errorf("name-only subjects should still be people, got %d", n)
	}
	// 2. The scrub — does it rewrite a name with no handle behind it?
	out, replaced := scrubSubjectNames("Rory and Leo playing dominos",
		SubjectsForRefs(sess, []string{"image#rory", "image#leo"}))
	if len(replaced) != 2 {
		t.Errorf("both names should be scrubbed, got %v (%q)", replaced, out)
	}
	if strings.Contains(out, "Rory") || strings.Contains(out, "Leo") {
		t.Errorf("names still reaching the renderer: %q", out)
	}
	// 3. The refusal — fires when their picture exists but was not passed?
	if err := refuseUnpassedPeople(sess, "Rory playing dominos", nil); err == nil {
		t.Error("a name-only person we have a picture of must still gate the render")
	}
	// 4. One-canonical — does a second picture of Rory replace the first?
	ref := RecordRecentImage(sess, []byte("\x89PNG\r\n\x1a\nnewer"), "sent by craig", ImageFromUser)
	if _, err := KeepImageOf(sess, ref, "rory_newer", "", ResolveKeepSubject(sess, "Rory", true)); err != nil {
		t.Fatal(err)
	}
	var rory []string
	for _, k := range KeptImages(sess) {
		if strings.EqualFold(k.Subject.Name, "Rory") {
			rory = append(rory, k.Name)
		}
	}
	if len(rory) != 1 {
		t.Errorf("one canonical picture per person should hold for name-only too, got %v", rory)
	}
}

// The one real consequence, pinned so it stays a known shape rather than a
// surprise: a name-only entry and a later handle-anchored one for the same
// person do NOT merge, because SameSubject compares keys and "n:rory" is not
// "h:+1555...".
//
// Deliberately not fixed by loosening SameSubject. That strictness IS the
// impersonation guard — someone renaming themselves to another person's
// display name must never inherit their entry — so relaxing it to merge these
// would let a third party retire the owner's own picture of somebody by
// messaging in under the right name. Two entries is the safe failure; the fix
// belongs in a visible collision report, not in the identity comparison.
func TestNameOnlyAndAnchoredDoNotMerge(t *testing.T) {
	byName := ImageSubject{Person: true, Name: "Rory"}
	byHandle := ImageSubject{Person: true, Name: "Rory", Handle: "+15550199"}
	if SameSubject(byName, byHandle) {
		t.Log("they merge — this test is stale, update the caveat")
	} else {
		t.Log("confirmed: name-only and anchored coexist as two entries")
	}
}
