package imagefetch

// A check that rewrites a user's request has to be wrong far less often than
// the thing it fixes. These pin the precision filters as hard as the behavior:
// missing a lowercase "rory" costs a nudge, but rewriting "put a mark on it"
// corrupts the request, and a check that corrupts requests gets turned off.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func person(name string) ImageSubject { return ImageSubject{Person: true, Name: name} }

func TestAttachedNameBecomesItsPosition(t *testing.T) {
	got, replaced := scrubSubjectNames("Rory on a beach at sunset", []ImageSubject{person("Rory")})
	if got != "the person in the first image on a beach at sunset" {
		t.Errorf("got %q", got)
	}
	if len(replaced) != 1 || replaced[0] != "Rory" {
		t.Errorf("the model has to be told what changed, got %v", replaced)
	}

	// Two people, two positions — the blend case, and the reason the subject
	// list is index-aligned with the images list.
	got, _ = scrubSubjectNames("Rory and Henry shaking hands", []ImageSubject{person("Rory"), person("Henry")})
	if !strings.Contains(got, "the person in the first image") || !strings.Contains(got, "the person in the second image") {
		t.Errorf("each name takes its own position, got %q", got)
	}
}

func TestPossessiveKeepsItsGrammar(t *testing.T) {
	// "Rory's face" must not become "the person in the first image's face",
	// which reads as the image's face rather than the person's.
	got, _ := scrubSubjectNames("Rory's face on the body in the second", []ImageSubject{person("Rory")})
	if got != "the first image's face on the body in the second" {
		t.Errorf("got %q", got)
	}
	// The curly apostrophe a phone keyboard produces counts too.
	got, _ = scrubSubjectNames("Rory’s hair", []ImageSubject{person("Rory")})
	if got != "the first image's hair" {
		t.Errorf("got %q", got)
	}
}

func TestOrdinaryWordsSurvive(t *testing.T) {
	// The false positive that would matter most: a name that is also a common
	// noun, used as the noun.
	for _, c := range []struct{ prompt, name string }{
		{"put a mark on it", "Mark"},
		{"a rose garden at dawn", "Rose"},
		{"a bill on the table", "Bill"},
		{"in may, under a bright sky", "May"},
	} {
		got, replaced := scrubSubjectNames(c.prompt, []ImageSubject{person(c.name)})
		if got != c.prompt || len(replaced) > 0 {
			t.Errorf("%q with subject %q was rewritten to %q", c.prompt, c.name, got)
		}
	}
	// Whole words only: a name inside a longer word is not that name.
	got, _ := scrubSubjectNames("a Marketing brochure", []ImageSubject{person("Mark")})
	if got != "a Marketing brochure" {
		t.Errorf("embedded match was rewritten: %q", got)
	}
	// A short name is not worth the collision rate.
	got, _ = scrubSubjectNames("An Al Fresco setting", []ImageSubject{person("Al")})
	if got != "An Al Fresco setting" {
		t.Errorf("a two-letter name should not be matched: %q", got)
	}
}

func TestOnlyPeopleAreScrubbed(t *testing.T) {
	// A pet's or a place's name is a useful noun to a renderer and carries no
	// identity prior to fight with. Replacing it loses information.
	thing := ImageSubject{Name: "Bess"}
	got, replaced := scrubSubjectNames("Bess in the snow", []ImageSubject{thing})
	if got != "Bess in the snow" || len(replaced) > 0 {
		t.Errorf("a non-person subject must be left alone, got %q", got)
	}
	// And an image with no subject at all contributes nothing.
	got, _ = scrubSubjectNames("Rory on a beach", []ImageSubject{{}})
	if got != "Rory on a beach" {
		t.Errorf("an unsubjected image cannot claim a name, got %q", got)
	}
}

func TestNothingToDoIsNoChange(t *testing.T) {
	// The overwhelmingly common case: the prompt is already positional, or
	// there are no subjects. It must cost nothing and say nothing.
	got, replaced := scrubSubjectNames("the face from the first picture on the body in the second",
		[]ImageSubject{person("Rory"), person("Henry")})
	if got != "the face from the first picture on the body in the second" {
		t.Errorf("a correct prompt was altered: %q", got)
	}
	if n := scrubNote(replaced); n != "" {
		t.Errorf("no change means no note, got %q", n)
	}
}

func TestPositionWordsRunOutGracefully(t *testing.T) {
	for i, want := range []string{"first", "second", "third", "fourth", "#5"} {
		if got := positionWord(i); got != want {
			t.Errorf("positionWord(%d) = %q, want %q", i, got, want)
		}
	}
}

// libraryWith stages one kept picture of a person and returns the session.
func libraryWith(t *testing.T, name string) *ToolSession {
	t.Helper()
	SetImageDir(t.TempDir())
	sess := &ToolSession{Username: "alice", AgentID: "agent1", WorkspaceDir: t.TempDir()}
	ref := RecordRecentImage(sess, []byte("\x89PNG\r\n\x1a\nphoto"), "received from a contact", ImageFromUser)
	if ref == "" {
		t.Fatal("could not stage a picture")
	}
	if _, err := KeepImageOf(sess, ref, strings.ToLower(name), "", person(name)); err != nil {
		t.Fatal(err)
	}
	return sess
}

// The check the user asked to move earlier: a prompt naming somebody whose
// picture we HOLD, with nothing passed, must not reach the backend at all. A
// render that invents the wrong face has already cost the time and is already
// deliverable — a note under it is something the model can read and ship anyway.
func TestRenderIsRefusedBeforeItInventsAFaceWeHave(t *testing.T) {
	sess := libraryWith(t, "Rory")

	err := refuseUnpassedPeople(sess, "Rory standing on a beach", nil)
	if err == nil {
		t.Fatal("a prompt naming someone we have a picture of must not render")
	}
	// The refusal has to carry the fix, or the model retries the same call.
	for _, want := range []string{"image#rory", "images", "not capitalized"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q: %v", want, err)
		}
	}
	// Nothing ran — said plainly, so it is not reported to the user as a
	// failed render.
	if !strings.Contains(err.Error(), "nothing was rendered") {
		t.Errorf("the refusal must say nothing happened: %v", err)
	}
}

func TestPassingThePictureSatisfiesTheCheck(t *testing.T) {
	sess := libraryWith(t, "Rory")
	// Same prompt, but the reference is attached: allowed, and the name is
	// rewritten to where the picture is rather than left to fight with it.
	out, note, err := checkPromptSubjects(sess, "Rory standing on a beach", []string{"image#rory"})
	if err != nil {
		t.Fatalf("passing the picture must be enough: %v", err)
	}
	if !strings.Contains(out, "the person in the first image") {
		t.Errorf("the name should have become a position: %q", out)
	}
	if strings.Contains(out, "Rory") {
		t.Errorf("the name must not reach the backend: %q", out)
	}
	if note == "" {
		t.Error("a rewritten prompt must be reported, or the model writes the same one next time")
	}
}

// An unrelated prompt is untouched — the overwhelmingly common call, and the
// one where a false positive would be noticed first.
func TestAnUnrelatedPromptIsNotBlocked(t *testing.T) {
	sess := libraryWith(t, "Rory")
	out, note, err := checkPromptSubjects(sess, "a snowy mountain at dusk", nil)
	if err != nil {
		t.Fatalf("unrelated prompt blocked: %v", err)
	}
	if out != "a snowy mountain at dusk" || note != "" {
		t.Errorf("unrelated prompt altered: %q / %q", out, note)
	}
}

// A face the agent GENERATED is not a likeness of anyone, so it must not be
// offered as the picture to pass — that would launder an invention into a
// reference, which is the rule the manifest and schema already enforce.
func TestAGeneratedFaceIsNotOfferedAsTheReference(t *testing.T) {
	SetImageDir(t.TempDir())
	sess := &ToolSession{Username: "alice", AgentID: "agent1", WorkspaceDir: t.TempDir()}
	ref := RecordRecentImage(sess, []byte("\x89PNG\r\n\x1a\nrender"), "generated: a man", ImageFromGenerated)
	if _, err := KeepImageOf(sess, ref, "rory", "", person("Rory")); err != nil {
		t.Fatal(err)
	}
	if err := refuseUnpassedPeople(sess, "Rory on a beach", nil); err != nil {
		t.Errorf("an agent-made face is not a likeness and must not gate a render: %v", err)
	}
}
