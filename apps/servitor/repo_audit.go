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
	return "You are auditing servitor's stored knowledge about the code repository " + repoDisplayTarget(a) + ", which was JUST re-pulled and may have changed. Your only job is to keep the stored knowledge TRUE to the current code, both the knowledge docs and the discrete facts.\n\n" +
		"Verify every claim against the CURRENT code using search_code, read_file, and list_dir, then:\n" +
		"1. DOCS: if the code now contradicts a doc (renamed/removed/added subsystems, changed data model, moved entry points, new or dropped services), rewrite the corrected version with update_doc(doc, content) — the FULL corrected markdown, not a diff. If a doc still matches, leave it.\n" +
		"2. FACTS: if a stored fact's value is now different, correct it with store_fact(key, value). If a fact's subject no longer exists in the code (a removed service, dropped table, deleted route), retire it with retire_fact(key). Only retire a fact you have VERIFIED is gone from the code; if unsure, leave it.\n\n" +
		"Correct, do not re-derive. Preserve everything still accurate and change only what the code contradicts; do not reword for style, expand scope, or invent new docs or facts. If nothing has changed, make no changes. When finished, give a one-paragraph summary of what you corrected or retired and what you verified as still current."
}

// runRepoMemoryAudit verifies the appliance's stored knowledge docs against the
// freshly-cloned code and auto-corrects the stale ones via update_doc. It runs
// under the caller's session ctx (so a Cancel aborts it) and streams progress on
// sid. Docs already corrected before a cancel are kept — each update_doc write is
// a self-contained, verified rewrite, so a partial run leaves valid state.
//
// It reconciles both the knowledge docs (rewritten via update_doc) and the
// discrete scoped facts (a changed value corrected via store_fact, an obsolete
// fact dropped via retire_fact, backed by RemoveScopedApplianceFact — the
// graph-attr delete added for this pass).
func (T *Servitor) runRepoMemoryAudit(ctx context.Context, sid, user string, udb Database, appliance Appliance) {
	docs := allDocs(udb, appliance.ID)
	factsBlock := strings.TrimSpace(scopedFactsBlock(udb, appliance))
	if len(docs) == 0 && factsBlock == "" {
		emit(sid, probeEvent{Kind: "status", Text: "No stored knowledge to validate yet — run Map System to build it."})
		return
	}

	var claims strings.Builder
	for _, name := range knowledgeDocNames {
		if c := strings.TrimSpace(docs[name]); c != "" {
			fmt.Fprintf(&claims, "## Stored doc: %s\n%s\n\n", name, c)
		}
	}
	if factsBlock != "" {
		fmt.Fprintf(&claims, "## Stored facts (key: value)\n%s\n\n", factsBlock)
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

	factCorrected := map[string]bool{}
	storeFact := AgentToolDef{
		Tool: Tool{
			Name:        "store_fact",
			Description: "Correct (or add) a discrete fact — a short key: value about this system — when the current code shows the stored value is now different. Overwrites the existing value for that key. Keep it specific: a version, port, path, table, or framework name.",
			Parameters: map[string]ToolParam{
				"key":   {Type: "string", Description: "The fact key, e.g. \"http_port\" or \"orm\"."},
				"value": {Type: "string", Description: "The corrected value, verified against the current code."},
			},
			Required: []string{"key", "value"},
		},
		Handler: func(args map[string]any) (string, error) {
			key := strings.TrimSpace(fmt.Sprint(args["key"]))
			value := strings.TrimSpace(fmt.Sprint(args["value"]))
			if key == "" || value == "" {
				return "", fmt.Errorf("key and value are required")
			}
			recordScopedApplianceFact(appliance, key, value, "long")
			factCorrected[key] = true
			emit(sid, probeEvent{Kind: "status", Text: "Corrected fact: " + key})
			return "Stored " + key + ".", nil
		},
	}

	factRetired := map[string]bool{}
	retireFact := AgentToolDef{
		Tool: Tool{
			Name:        "retire_fact",
			Description: "Retire a stored fact whose subject no longer exists in the current code — a removed service, dropped table, deleted route. Only retire a fact you have VERIFIED is gone from the code; if unsure, leave it. Use the exact key from the stored facts list.",
			Parameters: map[string]ToolParam{
				"key": {Type: "string", Description: "The exact fact key to retire, from the stored facts list."},
			},
			Required: []string{"key"},
		},
		Handler: func(args map[string]any) (string, error) {
			key := strings.TrimSpace(fmt.Sprint(args["key"]))
			if key == "" {
				return "", fmt.Errorf("key is required")
			}
			if RemoveScopedApplianceFact(appliance, key) {
				factRetired[key] = true
				emit(sid, probeEvent{Kind: "status", Text: "Retired obsolete fact: " + key})
				return "Retired " + key + ".", nil
			}
			return "No stored fact with key " + key + " (already gone, or wrong key — check the stored facts list).", nil
		},
	}

	tools := append(repoCodeTools(user, appliance.ID), updateDoc, storeFact, retireFact)

	emit(sid, probeEvent{Kind: "status", Text: "Validating stored knowledge against the new code…"})
	userMsg := "Here is servitor's currently stored knowledge about this repository (docs and discrete facts). The code was just re-pulled. Verify each item against the CURRENT code: correct a stale doc with update_doc, correct a changed fact value with store_fact, and retire_fact any fact whose subject no longer exists. Leave accurate items untouched.\n\n" + claims.String()

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
	if len(factCorrected) > 0 || len(factRetired) > 0 {
		emit(sid, probeEvent{Kind: "status", Text: fmt.Sprintf("Facts: %d corrected, %d retired.", len(factCorrected), len(factRetired))})
	}

	changed := len(corrected) > 0 || len(factCorrected) > 0 || len(factRetired) > 0
	summary := "Validated the stored knowledge against the new code; nothing needed correcting."
	if changed {
		summary = "Validated and reconciled the stored knowledge against the new code."
	}
	if resp != nil {
		if s := strings.TrimSpace(resp.Content); s != "" {
			summary = s
		}
	}
	probeSessions.AppendEvent(sid, probeEvent{Kind: "reply", Text: summary}, false)
}
