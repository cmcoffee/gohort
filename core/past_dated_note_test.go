package core

import (
	"strings"
	"testing"
	"time"
)

// A saved note about something still to come is held back from the prompt
// once the day it names has passed, as the ARC Raiders free window should
// have been; a note about the past stays, and so does one before its day.
func TestANoteAboutAPastDayIsHeldBack(t *testing.T) {
	loc := time.FixedZone("PDT", -7*3600)
	saved := time.Date(2026, time.October, 9, 15, 0, 0, 0, loc)
	facts := []MemoryFact{
		{ID: "f1", Note: "Craig's free ARC Raiders window closes Monday (Mon Oct 12, 2026).", Created: saved},
		{ID: "f2", Note: "Met Dana yesterday (Thu Oct 8, 2026) at the studio.", Created: saved},
		{ID: "f3", Note: "Craig prefers short replies.", Created: saved},
	}
	block := func(now time.Time) string {
		prev := memoryBlockNow
		memoryBlockNow = func() time.Time { return now }
		defer func() { memoryBlockNow = prev }()
		return RenderMemoryFactsBlock(facts)
	}
	before := block(time.Date(2026, time.October, 11, 9, 0, 0, 0, loc))
	if !strings.Contains(before, "closes Monday (Mon Oct 12, 2026)") {
		t.Errorf("a note before its day was held back:\n%s", before)
	}
	after := block(time.Date(2026, time.October, 16, 15, 0, 0, 0, loc))
	if strings.Contains(after, "closes Monday (Mon Oct 12") || !strings.Contains(after, "1. [held back: about days that have now passed]") {
		t.Errorf("a note past its day still reads as current:\n%s", after)
	}
	if !strings.Contains(after, "Met Dana yesterday (Thu Oct 8, 2026)") || !strings.Contains(after, "Craig prefers short replies.") {
		t.Errorf("a note about the past, or one with no date, was held back:\n%s", after)
	}
}
