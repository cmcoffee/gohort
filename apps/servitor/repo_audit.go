package servitor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// buildRepoAuditPrompt is the system prompt for the memory-validation pass that
// runs after a repo re-clone: verify servitor's stored knowledge docs against
// the freshly-pulled code and correct only what the code now contradicts.
func buildRepoAuditPrompt(a Appliance) string {
	return "You are auditing servitor's stored knowledge about the code repository " + repoDisplayTarget(a) + ", which was JUST re-pulled and may have changed. Your only job is to keep the stored knowledge docs TRUE to the current code.\n\n" +
		"For each stored doc you are given:\n" +
		"1. Verify its claims against the CURRENT code using search_code, read_file, and list_dir.\n" +
		"2. If the code now contradicts the doc (renamed/removed/added subsystems, changed data model, moved entry points, new or dropped services), rewrite the corrected version with update_doc(doc, content). Provide the FULL corrected markdown for that doc, not a diff.\n" +
		"3. If a doc still matches the code, leave it untouched — do NOT call update_doc for it.\n\n" +
		"Correct, do not re-derive. Preserve everything still accurate and change only what the code contradicts; do not reword for style, expand scope, or invent new docs. If nothing has changed, make no update_doc calls. When finished, give a one-paragraph summary of what you corrected and what you verified as still current."
}

// runRepoMemoryAudit verifies the appliance's stored knowledge docs against the
// freshly-cloned code and auto-corrects the stale ones via update_doc. It runs
// under the caller's session ctx (so a Cancel aborts it) and streams progress on
// sid. Docs already corrected before a cancel are kept — each update_doc write is
// a self-contained, verified rewrite, so a partial run leaves valid state.
//
// v1 scope is knowledge docs only. Discrete-fact reconciliation is deferred: the
// fact store recently split, and the live path (graph-scoped entity attrs) has no
// correction/retire primitive yet, so auto-correcting facts is its own task.
func (T *Servitor) runRepoMemoryAudit(ctx context.Context, sid, user string, udb Database, appliance Appliance) {
	docs := allDocs(udb, appliance.ID)
	if len(docs) == 0 {
		emit(sid, probeEvent{Kind: "status", Text: "No stored knowledge to validate yet — run Map System to build it."})
		return
	}

	var claims strings.Builder
	for _, name := range knowledgeDocNames {
		if c := strings.TrimSpace(docs[name]); c != "" {
			fmt.Fprintf(&claims, "## Stored doc: %s\n%s\n\n", name, c)
		}
	}

	corrected := map[string]bool{}
	updateDoc := AgentToolDef{
		Tool: Tool{
			Name:        "update_doc",
			Description: "Replace one knowledge doc with a corrected version. Use ONLY when the current code contradicts the stored doc. doc must be one of: overview, databases, filesystem, services, apps. Provide the full corrected markdown as content, not a diff.",
			Parameters: map[string]ToolParam{
				"doc":     {Type: "string", Description: "Which doc to correct: overview | databases | filesystem | services | apps."},
				"content": {Type: "string", Description: "The full corrected markdown for this doc."},
			},
			Required: []string{"doc", "content"},
		},
		Handler: func(args map[string]any) (string, error) {
			doc := strings.ToLower(strings.TrimSpace(fmt.Sprint(args["doc"])))
			valid := false
			for _, n := range knowledgeDocNames {
				if n == doc {
					valid = true
					break
				}
			}
			if !valid {
				return "", fmt.Errorf("doc must be one of: %s", strings.Join(knowledgeDocNames, ", "))
			}
			content := strings.TrimSpace(fmt.Sprint(args["content"]))
			// writeDoc does not guard empty content, so refuse it here — an
			// empty rewrite would erase a doc the audit was meant to preserve.
			if len(content) < 20 {
				return "Refused: content is empty or too short to be a real doc — not overwriting.", nil
			}
			writeDoc(udb, appliance.ID, doc, content)
			corrected[doc] = true
			emit(sid, probeEvent{Kind: "status", Text: "Corrected knowledge doc: " + doc})
			return "Updated the " + doc + " doc.", nil
		},
	}

	tools := append(repoCodeTools(user, appliance.ID), updateDoc)

	emit(sid, probeEvent{Kind: "status", Text: "Validating stored knowledge against the new code…"})
	userMsg := "Here is servitor's currently stored knowledge about this repository. The code was just re-pulled. Verify each doc against the CURRENT code and correct any that are now wrong or stale with update_doc; leave accurate docs untouched.\n\n" + claims.String()

	resp, _, err := T.RunAgentLoop(ctx, []Message{{Role: "user", Content: userMsg}}, AgentLoopConfig{
		SystemPrompt:    buildRepoAuditPrompt(appliance),
		Tools:           tools,
		MaxRounds:       40,
		RouteKey:        "app.servitor",
		MaskDebugOutput: true,
		SerialTools:     true,
		ChatOptions:     []ChatOption{WithThink(false)},
	})

	if ctx.Err() != nil {
		emit(sid, probeEvent{Kind: "status", Text: "Memory validation cancelled — any docs already corrected this run are kept."})
		return
	}
	if err != nil {
		emit(sid, probeEvent{Kind: "status", Text: "Memory validation hit an error: " + err.Error()})
		return
	}

	// The docs were just re-verified against the fresh clone, so clear the
	// "code refreshed since this was generated" stale banner by advancing
	// Scanned to now (repoOverviewStale compares RepoCloned vs Scanned).
	var rec Appliance
	if udb.Get(applianceTable, appliance.ID, &rec) && rec.ID != "" {
		rec.Scanned = time.Now().Format(time.RFC3339)
		udb.Set(applianceTable, appliance.ID, rec)
	}

	if len(corrected) > 0 {
		names := make([]string, 0, len(corrected))
		for d := range corrected {
			names = append(names, d)
		}
		sort.Strings(names)
		emit(sid, probeEvent{Kind: "status", Text: fmt.Sprintf("Corrected %d doc(s): %s", len(names), strings.Join(names, ", "))})
	}

	summary := "Validated the stored knowledge against the new code; nothing needed correcting."
	if len(corrected) > 0 {
		summary = "Validated and reconciled the stored knowledge against the new code."
	}
	if resp != nil {
		if s := strings.TrimSpace(resp.Content); s != "" {
			summary = s
		}
	}
	probeSessions.AppendEvent(sid, probeEvent{Kind: "reply", Text: summary}, false)
}
