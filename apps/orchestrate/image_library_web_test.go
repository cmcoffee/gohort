package orchestrate

// The library view exists because there was nowhere to look. An agent handed
// back three of its own renders as three people's reference photos and nothing
// could have caught it earlier — every other surface describes the library in
// the agent's own words, which were confidently wrong. These pin the columns
// that make the difference between a listing and an audit.

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func TestOriginIsNeverBlank(t *testing.T) {
	// The column that matters most, and the one a listing is most tempted to
	// leave empty. A blank cell reads as "nothing to say"; the whole point is
	// that there IS something to say.
	if got := libraryOrigin(KeptImage{Origin: ImageOriginUnknown}); got != "unrecorded" {
		t.Errorf("an unrecorded origin must say so, got %q", got)
	}
	if got := libraryOrigin(KeptImage{Origin: ImageFromGenerated}); !strings.Contains(got, "agent") {
		t.Errorf("a render must be named as one, got %q", got)
	}
	if got := libraryOrigin(KeptImage{Origin: ImageFromEdited}); !strings.Contains(got, "agent") {
		t.Errorf("an edit is the agent's output too, got %q", got)
	}
	if got := libraryOrigin(KeptImage{Origin: ImageFromUser}); got != string(ImageFromUser) {
		t.Errorf("a known origin reads plainly, got %q", got)
	}
}

func TestSubjectSaysWhenNobodyRecordedOne(t *testing.T) {
	if got := librarySubject(KeptImage{}); !strings.Contains(got, "not labelled") {
		t.Errorf("an unlabelled entry is the one worth acting on, got %q", got)
	}
	// A name with no handle is a label somebody typed, not an identification
	// the transport confirmed — the same distinction the agent is shown.
	got := librarySubject(KeptImage{Subject: ImageSubject{Person: true, Name: "Rory"}})
	if !strings.Contains(got, "name only") {
		t.Errorf("an unanchored person should be marked, got %q", got)
	}
	anchored := librarySubject(KeptImage{Subject: ImageSubject{Person: true, Name: "Rory", Handle: "+15550199"}})
	if strings.Contains(anchored, "name only") {
		t.Errorf("an anchored person is not a bare label, got %q", anchored)
	}
	// A thing needs no handle to be fully identified.
	if got := librarySubject(KeptImage{Subject: ImageSubject{Name: "the office"}}); got != "the office" {
		t.Errorf("a non-person subject reads plainly, got %q", got)
	}
}

func TestNotesColumnStaysScannable(t *testing.T) {
	long := KeptImage{Note: strings.Repeat("a", 200)}
	if n := len([]rune(libraryShows(long))); n > 120 {
		t.Errorf("notes column is %d runes — it should truncate for scanning", n)
	}
	// Note and caption both appear when both exist: one is why it was kept,
	// the other is what is in frame, and they answer different questions.
	both := libraryShows(KeptImage{Note: "sent by craig", Caption: "man on a boat"})
	if !strings.Contains(both, "sent by craig") || !strings.Contains(both, "man on a boat") {
		t.Errorf("both the reason and the caption belong here, got %q", both)
	}
	if libraryShows(KeptImage{}) != "" {
		t.Error("nothing to say should render as nothing, not as punctuation")
	}
}

func TestKeptDateIsBlankRatherThanEpoch(t *testing.T) {
	// A legacy entry has no timestamp. Printing the zero time would put
	// "1 Jan 0001" beside every picture kept before this was recorded, which
	// reads as data rather than as its absence.
	if got := libraryWhen(time.Time{}); got != "" {
		t.Errorf("a missing date must render empty, got %q", got)
	}
	if got := libraryWhen(time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)); got != "7 Aug 2026" {
		t.Errorf("date format = %q", got)
	}
}

// The agent id is interpolated into a <script> block. Nothing produces an id
// with a quote in it today, which is exactly the condition under which this
// gets forgotten.
func TestAgentIDCannotEscapeTheScriptLiteral(t *testing.T) {
	head := imageLibraryHeadHTML(`a"); alert(1); //`)
	if strings.Contains(head, `"a"); alert(1); //"`) {
		t.Errorf("the id was interpolated raw:\n%s", head)
	}
	if !strings.Contains(head, `\"`) {
		t.Errorf("the id should be escaped as a JS literal:\n%s", head)
	}
	// And the handler is still registered under the name the row action calls.
	if !strings.Contains(head, "agent_image_label") {
		t.Error("the client action must keep its registered name")
	}
}
