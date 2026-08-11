package orchestrate

import (
	"fmt"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The cortex card is the standing thread's only record of what a channel turn
// DID. Names alone were not enough: "used: shell" says nothing when the command
// is the entire content of the call, which is why an owner could not tell what
// an agent had already done on their behalf.
func TestToolCallBriefCarriesTheSalientArgument(t *testing.T) {
	for name, tc := range map[string]struct {
		call ToolCall
		want string
	}{
		"shell shows the command": {
			ToolCall{Name: "shell", Args: map[string]any{"command": "systemctl status gohort"}},
			"shell(systemctl status gohort)",
		},
		"search shows the query": {
			ToolCall{Name: "web_search", Args: map[string]any{"query": "kiteworks release notes"}},
			"web_search(kiteworks release notes)",
		},
		"grouped tool shows the action": {
			ToolCall{Name: "archetype", Args: map[string]any{"action": "read", "slug": "kb"}},
			"archetype(action=read)",
		},
		"no args stays a bare name": {
			ToolCall{Name: "survey"}, "survey",
		},
		// No recognized key: fall back to a STABLE pick (sorted) so the brief
		// is still more than a bare name and does not vary run to run.
		"unrecognized keys still say something": {
			ToolCall{Name: "custom", Args: map[string]any{"zeta": "last", "alpha": "first"}},
			"custom(alpha=first, zeta=last)",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := toolCallBrief(tc.call); got != tc.want {
				t.Errorf("brief = %q, want %q", got, tc.want)
			}
		})
	}
}

// Long values and newlines must not wreck the card — it is a pointer, not a body.
func TestToolCallBriefClipsAndFlattens(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := toolCallBrief(ToolCall{Name: "shell", Args: map[string]any{"command": "echo\n" + long}})
	if strings.Contains(got, "\n") {
		t.Errorf("a brief must be one line: %q", got)
	}
	if len(got) > 90 {
		t.Errorf("brief not clipped (%d chars): %q", len(got), got)
	}
}

// With arguments present the card goes one per line; three name(arg) briefs
// comma-joined are unreadable.
func TestToolsUsedNoteFormatting(t *testing.T) {
	out := toolsUsedNote([]string{"shell(uptime)", "web_search(kiteworks)"})
	if !strings.Contains(out, "↳ ran:") || !strings.Contains(out, "\n   • shell(uptime)") {
		t.Errorf("expected a per-line list:\n%s", out)
	}
	// Bare names keep the compact one-line form.
	out = toolsUsedNote([]string{"survey", "recall"})
	if out != "↳ used: survey, recall" {
		t.Errorf("bare names should stay on one line: %q", out)
	}
	// Overflow is reported, never silently dropped.
	many := make([]string, 12)
	for i := range many {
		many[i] = fmt.Sprintf("t%d(a)", i)
	}
	if out = toolsUsedNote(many); !strings.Contains(out, "and 4 more") {
		t.Errorf("overflow must be stated:\n%s", out)
	}
}

// Silence is the norm in a group room, and the silent path returned before the
// cortex append — so a turn that ran tools and then said nothing left no record
// anywhere the owner looks. That is precisely the turn worth recording: an
// action with no reply attached to explain it.
func TestSilentTurnStillCardsWhatItRan(t *testing.T) {
	// The note is what the card carries; assert it survives the silent shape.
	note := toolsUsedNote([]string{"web_search(world news)", "arm_monitor(name=inbox)"})
	if note == "" {
		t.Fatal("tools note must render for a silent turn")
	}
	obs := strings.TrimSpace("what's happening?" + "\n" + note + "\n↳ stayed silent (nothing sent to the channel)")

	for _, want := range []string{
		"what's happening?",       // what came in
		"web_search(world news)",  // what it ran, with the argument
		"arm_monitor(name=inbox)", // and the second call
		"stayed silent",           // and that nothing went out
	} {
		if !strings.Contains(obs, want) {
			t.Errorf("card missing %q:\n%s", want, obs)
		}
	}
}

// A silent turn that ran NOTHING should not manufacture a card — the feed is
// pointers to things that happened, not a log of every inbound message.
func TestSilentTurnWithNoToolsWritesNothing(t *testing.T) {
	if note := toolsUsedNote(nil); note != "" {
		t.Errorf("no tools should mean no note, got %q", note)
	}
}
