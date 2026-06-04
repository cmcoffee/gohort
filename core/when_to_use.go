// Two-description split: every agent / skill / collection carries a
// user-facing Description AND an LLM-facing WhenToUse. The Description
// sells the thing to a human; WhenToUse tells ANOTHER model the concrete
// situations that should trigger picking it (delegate to an agent,
// consult a skill, search a collection). It's generated from the
// Description by a worker LLM on save and shown UN-truncated in the
// available-agents / available-skills routing blocks — where the 140-char
// Description cutoff was actively hiding the routing cues that made the
// LLM under-delegate.

package core

import (
	"context"
	"strings"
	"time"
)

// WhenToUseFunc is the registration shim that lets core generate a
// WhenToUse line without importing the app that owns the worker LLM. The
// orchestrate app sets it at startup (see Routes). nil when no LLM is
// wired — GenerateWhenToUse then returns "" and callers fall back to the
// plain Description.
//
// kind is "agent" / "skill" / "collection" (prompt framing); name and
// description are the thing's own user-facing values.
var WhenToUseFunc func(ctx context.Context, kind, name, description string) (string, error)

// GenerateWhenToUse produces the LLM-facing routing cue for a thing the
// model later has to PICK. Best-effort and bounded: returns "" on a
// missing generator, an empty description, a timeout, or an error — the
// caller keeps Description as the fallback, so a failed generation never
// blocks the save or loses data.
func GenerateWhenToUse(kind, name, description string) string {
	description = strings.TrimSpace(description)
	if WhenToUseFunc == nil || description == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, err := WhenToUseFunc(ctx, kind, name, description)
	if err != nil {
		Debug("[when_to_use] %s %q generation failed: %v", kind, name, err)
		return ""
	}
	return strings.TrimSpace(out)
}
