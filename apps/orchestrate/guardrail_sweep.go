package orchestrate

// A one-time sweep, not a migration that lives forever.
//
// Guardrail exceptions used to come in two kinds. A CONDITION was prose the
// check read under every rule that linked it; a PERSON was an identity the
// framework resolved itself, and a rule linked to one was dropped before the
// check ran. One authored list, two mechanisms, and a rule linking by NAME
// across both — so "craig" could mean either and the link reached whichever was
// stored first.
//
// Only conditions remain. Identity is the roster's job alone
// (AgentRecord.AuthorizedIdentities), and a rule yields to it through the "@"
// marker it already had.
//
// This moves what the old person exceptions were carrying onto the roster, so
// nobody loses an exemption in the change. It is registered as a maintenance
// button rather than a startup pass because a migration that runs on every boot
// forever is the thing this deployment already has too much of: it runs once,
// says what it did, and this file is then a deletion.

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterMaintenanceFunc("Migrations", "guardrail_person_exceptions",
		"Move person exceptions onto the authorized roster",
		"Guardrail exceptions are conditions now, and identity belongs to the roster. This moves any exception still marked as a person onto that agent's roster and drops it from the exception list. Run once; it reports what it moved and is safe to run again.",
		func(ctx context.Context) int { return sweepPersonExceptions(ctx) })
}

// sweepPersonExceptions folds every agent's person-kind exceptions into its
// roster. Returns how many agents were changed, for the maintenance panel.
func sweepPersonExceptions(ctx context.Context) int {
	app, ok := FindAgent("orchestrate")
	if !ok {
		return 0
	}
	T, ok := app.(*OrchestrateApp)
	if !ok || T.DB == nil {
		return 0
	}
	changed := 0
	for _, u := range AuthListUsers(AuthDB()) {
		if u.Username == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			break
		}
		udb := UserDB(T.DB, u.Username)
		for _, a := range listAgents(udb, u.Username) {
			moved, kept, roster := splitPersonExceptions(a)
			if len(moved) == 0 {
				continue
			}
			a.GuardrailExceptions = kept
			a.AuthorizedIdentities = roster
			// Through the normal save, so the change files a revision and is
			// one rollback away rather than a restore.
			if _, err := saveAgentAs(udb, a, "person exceptions moved to the roster"); err != nil {
				Err("[orchestrate.guardrail] sweep could not save agent %q: %v", a.ID, err)
				continue
			}
			changed++
			ReportMaintenanceProgress(ctx, fmt.Sprintf("%s · moved %d", a.Name, len(moved)))
			Log("[orchestrate.guardrail] agent=%s moved %d person exception(s) to the roster: %s",
				a.ID, len(moved), strings.Join(moved, ", "))
		}
	}
	ReportMaintenanceOutcome(ctx, fmt.Sprintf("%d agent(s) changed", changed))
	return changed
}

// splitPersonExceptions reports the person entries to move, the exceptions that
// stay, and the roster they should land on.
//
// The roster is de-duplicated case-insensitively against what is already there,
// because an owner who hit this confusion is likely to have listed the same
// person BOTH ways while trying to make it work — which is the state that
// produced the bug report.
func splitPersonExceptions(a AgentRecord) (moved []string, kept []GuardrailException, roster []string) {
	roster = append(roster, a.AuthorizedIdentities...)
	onRoster := map[string]bool{}
	for _, id := range roster {
		onRoster[strings.ToLower(strings.TrimSpace(id))] = true
	}
	for _, e := range a.GuardrailExceptions {
		if !strings.EqualFold(strings.TrimSpace(e.Kind), "person") {
			kept = append(kept, e)
			continue
		}
		text := strings.TrimSpace(e.Text)
		if text == "" {
			// Nothing to move and nothing to keep: a person entry with no
			// identity on it never matched anybody.
			continue
		}
		moved = append(moved, text)
		if key := strings.ToLower(text); !onRoster[key] {
			onRoster[key] = true
			roster = append(roster, text)
		}
	}
	return moved, kept, roster
}
