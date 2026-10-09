package orchestrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func (t *chatTurn) appDefCreateOrUpdate(args map[string]any, isUpdate bool) (string, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	slug := slugify(stringArg(args, "slug"))

	var spec AppSpec
	// The html this app serves RIGHT NOW, captured before anything overwrites
	// it — an update is only recognizable as a wipe by comparison with what it
	// replaces (see appRewriteRisk).
	var priorHTML string
	if isUpdate {
		key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), name))
		existing, ok := LoadAppSpec(t.user, key)
		if !ok {
			return "", appNotFound(args, "to update")
		}
		spec = existing
		priorHTML = appSpecHTMLText(existing)
		if name != "" {
			spec.Name = name
		}
	} else {
		if name == "" {
			// Point at the OTHER action too. This fires when an author is
			// re-sending a large payload to fix one thing, and three times in a
			// row the reflex was to re-send the same create rather than to
			// switch verbs — an app that already exists is revised, not
			// recreated, and the message never said so.
			return "", errors.New("name is required to create an app, pass name:\"My App\" (the slug is derived from it; pass slug explicitly to override). If the app already exists, use action=\"update\" with id=\"<slug>\" instead: app_def(action=\"list\") shows what you have")
		}
		if slug == "" {
			slug = slugify(name)
		}
		if slug == "" {
			return "", errors.New("could not derive a slug from the name: pass an explicit slug")
		}
		if _, exists := LoadAppSpec(t.user, slug); exists {
			return "", fmt.Errorf("an app with slug %q already exists: use action=update, or pick a different name/slug", slug)
		}
		spec = AppSpec{Slug: slug, Name: name, Owner: t.user}
	}

	if d := strings.TrimSpace(stringArg(args, "description")); d != "" {
		spec.Desc = d
	}
	// notes: the standing account for the next author. Present-only, so an
	// update without it keeps what is there; present and empty clears. The cap
	// is refused up front with the overshoot, the way update_notes does, rather
	// than truncated — a summary cut mid-sentence is worse than none.
	if _, ok := args["notes"]; ok {
		spec.Notes = strings.TrimSpace(stringArg(args, "notes"))
		if over := spec.NotesOver(); over > 0 {
			return "", fmt.Errorf("notes is %d characters over the %d-character cap: it is a standing SUMMARY (purpose, decisions and why, open items), not a log; trim it and resend", over, spec.NotesCap())
		}
	}
	if rk := strings.TrimSpace(stringArg(args, "record_key")); rk != "" {
		spec.RecordKey = rk
	}
	if spec.RecordKey == "" {
		spec.RecordKey = "id"
	}
	if a := strings.TrimSpace(stringArg(args, "agent_id")); a != "" {
		if ag, ok := findAgentByNameOrID(t.udb, t.user, a); ok {
			spec.AgentID = ag.ID
		} else {
			spec.AgentID = a // store as given; resolution is the chat surface's problem (step 2)
		}
	}
	// pipeline_id: the multi-stage RUN this app's pipeline section drives.
	// Resolved to a stored id where possible so a later rename of the pipeline
	// doesn't unbind the app; an unresolvable value is kept verbatim, since the
	// binding is looked up again by name at serve time.
	// Accepted at the app level OR on the pipeline SECTION itself. The section
	// is the more natural place to write it — that is where the binding is
	// used — and an author who guesses that way is not wrong about anything
	// except which object holds the field. Silently ignoring it produced an app
	// with a pipeline section and no pipeline, which fails at serve time with
	// nothing on the authoring side to explain it.
	pipeRef := strings.TrimSpace(stringArg(args, "pipeline_id"))
	if pipeRef == "" {
		pipeRef = sectionPipelineRef(args["sections"])
	}
	// promoted is what the bound pipeline puts on a run's sidebar row. Captured
	// here because this is the only place the DEFINITION is in hand; a meta
	// field naming something it does not promote renders an empty pill, which
	// reads as a broken panel rather than as a name that does not resolve.
	var promoted []string
	if pipeRef != "" {
		if def, ok := t.app.LookupAppPipeline(t.user, pipeRef); ok {
			spec.PipelineID = def.ID
			promoted = def.SessionMeta
		} else {
			spec.PipelineID = pipeRef
		}
	}
	// full_width: opt the app's page into edge-to-edge layout. Only honored when
	// the key is present so an update without it keeps the existing choice.
	if _, ok := args["full_width"]; ok {
		spec.FullWidth = boolArg(args, "full_width")
	}
	// private_db: opt the app into its own dedicated database file. Only honored
	// when the key is present so an update without it keeps the existing choice.
	// No migration — records already in the shared store stay there.
	if _, ok := args["private_db"]; ok {
		spec.PrivateDB = boolArg(args, "private_db")
	}
	// shared_collections: names of the collections every user of the app reads
	// in common. Slugged like script names, de-duplicated, order kept. Passed
	// wholesale on update (omit to keep; [] to clear).
	// ask_daily_usd / ask_user_daily_usd: the page's daily budget for asking
	// the app's agent, whole app and per user. Kept when absent.
	if _, ok := args["ask_daily_usd"]; ok {
		spec.AskDailyUSD = floatArg(args, "ask_daily_usd")
	}
	if _, ok := args["ask_user_daily_usd"]; ok {
		spec.AskUserDailyUSD = floatArg(args, "ask_user_daily_usd")
	}
	if _, ok := args["shared_collections"]; ok {
		var names []string
		seen := map[string]bool{}
		for _, n := range stringSliceFromArgs(args, "shared_collections") {
			if n = slugify(n); n != "" && !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
		spec.SharedCollections = names
	}
	// Script-backed data sources (the "logic" seam): a table/display section can
	// be backed by a python script instead of the record store. Passed wholesale
	// replaces the stored set on update (omit to keep existing).
	//
	// "Replaces" is also how scripts got lost: an update carrying actions:[one
	// new action] deleted every other action the app had, and the save said
	// success. So a list that leaves out a stored name is refused below, the
	// same way a dropped functional section is.
	var parseNotes []string
	var droppedScripts []string
	if raw, ok := args["data_sources"]; ok && raw != nil {
		if arr, note, ok := appArrayArg(raw, "data_sources"); ok {
			var notes []string
			prior := appDataSourceNames(spec)
			spec.DataSources, notes = appDataSources(arr)
			parseNotes = append(parseNotes, notes...)
			if isUpdate {
				if risk := appDroppedScripts("data_sources", "data source", prior, appDataSourceNames(spec)); risk != "" {
					droppedScripts = append(droppedScripts, risk)
				}
			}
		} else {
			parseNotes = append(parseNotes, note)
		}
	}
	// Script-backed actions (the write-side logic seam): buttons that run a
	// script which returns records the framework persists.
	if raw, ok := args["actions"]; ok && raw != nil {
		if arr, note, ok := appArrayArg(raw, "actions"); ok {
			var notes []string
			prior := appActionNames(spec)
			spec.Actions, notes = appActionDefs(arr)
			parseNotes = append(parseNotes, notes...)
			if isUpdate {
				if risk := appDroppedScripts("actions", "action", prior, appActionNames(spec)); risk != "" {
					droppedScripts = append(droppedScripts, risk)
				}
			}
		} else {
			parseNotes = append(parseNotes, note)
		}
	}
	// Shared Python modules: passed wholesale replaces the set (omit to keep).
	if raw, ok := args["libraries"]; ok && raw != nil {
		libs, err := appLibraries(raw)
		if err != nil {
			return "", err
		}
		spec.Libraries = libs
		if len(libs) == 0 {
			spec.Libraries = nil
		}
	}
	if len(droppedScripts) > 0 && !boolArg(args, "confirm_rewrite") {
		return "", errors.New(appWithParseNotes(strings.Join(droppedScripts, "\n\n"), parseNotes))
	}
	parseNotes = append(parseNotes, appToolCapNotes(t.user, spec)...)
	// Declared tunables: the framework renders their Settings page and hands
	// them to every script as env vars. Passed wholesale replaces the list.
	if raw, ok := args["settings"]; ok && raw != nil {
		if arr, note, ok := appArrayArg(raw, "settings"); ok {
			var notes []string
			spec.Settings, notes = appSettings(arr)
			parseNotes = append(parseNotes, notes...)
		} else {
			parseNotes = append(parseNotes, note)
		}
	}

	// Build the Page from the declarative sections. On update with no sections
	// passed, keep the existing page.
	if raw, ok := args["sections"]; ok && raw != nil {
		// Every section gets a stable id (app_sections.go) before anything
		// reads the array, so the stored authoring form always carries them.
		raw = ensureSectionIDs(raw)
		args["sections"] = raw
		// Refuse an update that reads as a half-finished rewrite BEFORE it can
		// be stored. Everything downstream — the parser, the browser load —
		// passes a document that deleted its own game loop, so this is the
		// only place the loss is still visible.
		if isUpdate && !boolArg(args, "confirm_rewrite") {
			if risk := appRewriteRisk(priorHTML, appProposedHTMLText(raw)); risk != "" {
				return "", errors.New(risk)
			}
			// The same loss from the other direction: the sections array is
			// replaced wholesale, so a functional section left out of the new
			// one is deleted — silently, because what remains still renders.
			if risk := appDroppedFunctionSection(appSpecSections(spec), appProposedSections(raw)); risk != "" {
				return "", errors.New(risk)
			}
		}
		// The record schema the new sections imply, checked against the rows
		// before anything is stored: a field the update drops that records
		// still carry is refused by name (app_record_schema.go).
		nextFields := appRecordFields(raw, spec.RecordKey)
		if isUpdate && !boolArg(args, "confirm_rewrite") {
			prevFields := spec.RecordFields
			if len(prevFields) == 0 {
				prevFields = appRecordFields(appSpecSectionsRaw(spec), spec.RecordKey)
			}
			if stranded := appStrandedFields(prevFields, nextFields, appStoredRecords(t.user, spec)); len(stranded) > 0 {
				return "", errors.New(appStrandedFieldsMessage(stranded, spec.Slug))
			}
		}
		page, err := buildAppPage(spec, raw)
		if err != nil {
			return "", err
		}
		blob, err := page.ConfigJSON()
		if err != nil {
			return "", fmt.Errorf("render app page: %w", err)
		}
		spec.Page = blob
		spec.RecordFields = nextFields
		// Keep the AUTHORING sections next to the page they compiled into, so
		// action=get can hand back something action=update actually accepts.
		// The rendered page is not valid input; without this, revising an app
		// meant re-authoring it blind from the rendered shape (which fails the
		// section parser outright) — the "the Builder can't update its own app"
		// bug this field exists to close.
		if src, err := json.Marshal(raw); err == nil {
			spec.Sections = src
		}
		// Record the workbench body field on the spec so the co-author tool +
		// viewer agree on which field is the document body.
		if arr, ok := raw.([]any); ok {
			for _, item := range arr {
				mm, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if mm = normalizeSection(mm); strings.EqualFold(strings.TrimSpace(mapStr(mm, "kind")), "workbench") {
					spec.BodyField = firstNonEmptyStr(mapStr(mm, "body_field"), "content")
				}
			}
		}
		parseNotes = append(parseNotes, unknownSectionKeyNotes(raw)...)
		parseNotes = append(parseNotes, appShapeNotes(raw, strings.TrimSpace(spec.PipelineID) != "")...)
		parseNotes = append(parseNotes, appSessionMetaNotes(raw, promoted)...)
	} else if !isUpdate {
		return "", errors.New("sections is required to create an app")
	}

	verb := "Created"
	reason := "create"
	if isUpdate {
		verb, reason = "Updated", "update"
		if boolArg(args, "confirm_rewrite") {
			reason = "update (confirmed rewrite)"
		}
		// A section-level action composes a full update and names itself here,
		// so history says "update_section form-entry" rather than "update".
		if r := strings.TrimSpace(stringArg(args, "_reason")); r != "" {
			reason = r
		}
	}
	// The note describes THIS revision; one given with an earlier edit must
	// not be carried onto a revision it says nothing about.
	spec.ChangeNote = strings.TrimSpace(stringArg(args, "note"))
	// An app session's first build is the page. Flagging a typed-only app
	// on save and failing its verify did not move it: a build accepted the
	// failed verify, told the user so, and kept form + table + display.
	// Refused at create, the shortcut costs a deliberate "plain list: <why>".
	if !isUpdate {
		if p := t.appModeTypedOnlyProblem(spec); p != "" {
			return "", errors.New("NOT CREATED: " + p)
		}
	}
	saved := SaveAppSpecAs(spec, reason)
	msg := fmt.Sprintf("%s app %q at /apps/%s/ (revision %s): open it in the dashboard under My Apps. Records save to the app's own store; the table lists them. Revise with app_def(action=\"update\", id=%q, …). Status: %s.",
		verb, saved.Name, saved.Slug, saved.Updated, saved.Slug, saved.VerifyStatus())

	// On create, the checklist for a complete app: shown once, in the result
	// of the call that made it, where the next step is decided.
	plan := ""
	if !isUpdate {
		plan = appCompletePlan()
	}
	msg += "\n\n" + t.appInventoryLine(saved)
	if n := len(saved.Settings); n > 0 {
		names := make([]string, 0, n)
		for _, st := range saved.Settings {
			names = append(names, st.Name)
		}
		msg += fmt.Sprintf("\n\nSettings (%d): %s, a Settings button on the app's My Apps row opens the page; each reaches every script as an env var of that name.", n, strings.Join(names, ", "))
	}

	// Report any name-normalization or dropped entries up front — a
	// slugified data-source name silently breaks a source_script/fetch
	// reference the author spelled the original way, and a dropped entry
	// reads as saved when it wasn't. Every early return below carries the
	// same block, because a failed save is exactly when the author is reading
	// closely: a page fetching data/balance_step from a source registered as
	// balance-step was broken by the rename, and the note saying so used to
	// appear only on the success path, which that page never reached.
	msg = appWithParseNotes(msg, parseNotes)

	// Parse the inline JavaScript an html section carries. A script that
	// doesn't parse takes the WHOLE page down (a game is one section, so the
	// app is simply dead), and until this ran the write reported success and
	// the author found out one round-trip later from a browser check —
	// usually against the previous revision, which is how a single stray
	// token turns into six updates that never converge. Answer here, attached
	// to the write that caused it, naming the block and line.
	pageChecked := false // a browser loaded THIS revision, below
	if raw, ok := args["sections"]; ok && raw != nil {
		var scriptProblems []string
		for i, html := range appHTMLSectionScripts(raw) {
			probs, checked := htmlScriptSyntaxProblems(t.sandboxCallerCtx(), html)
			if !checked {
				continue // no verdict reachable — say nothing rather than accuse
			}
			for _, p := range probs {
				scriptProblems = append(scriptProblems, fmt.Sprintf("html section %d, %s", i+1, p))
			}
		}
		if len(scriptProblems) > 0 {
			return appWithParseNotes(fmt.Sprintf("%s app %q, BUT its inline JavaScript DOES NOT PARSE, the page will be blank/dead until this is fixed:\n- %s\n\nFix the markup with app_def(action=\"update\", id=%q, …) (it re-checks on save). Send the WHOLE corrected document, and do NOT tell the user the app is ready.",
				verb, saved.Name, strings.Join(scriptProblems, "\n- "), saved.Slug), parseNotes), nil
		}
		// Parsing says the document is well-formed, not that it is whole. A
		// page that calls a function nothing defines parses, loads, and (for a
		// canvas app, where nothing runs until the user clicks) reports clean
		// in the browser too — so this is the only check standing between the
		// author and a "success" on top of a dead app. The update path already
		// REFUSED this shape; reaching here means either a create, or a
		// rewrite the author explicitly confirmed. Say it plainly either way.
		if dangling := jsDanglingCalls(appProposedHTMLText(raw)); len(dangling) > 0 {
			return appWithParseNotes(fmt.Sprintf("%s app %q, BUT the page CALLS CODE IT NEVER DEFINES: it parses and loads, and then dies the moment anyone uses it. Nothing defines: %s\n\nEither add those functions or remove the calls to them. Fix it with app_def(action=\"replace_function\", …) if you are adding one back, or action=\"update\" for the whole document. Do NOT tell the user the app is ready.",
				verb, saved.Name, appNameList(dangling, 12)), parseNotes), nil
		}

		// Parsing is only the cheap half. An html section IS the page, so load
		// the revision that was just written and report what the browser says
		// about it. Doing this ON SAVE is the point: action=verify runs against
		// whatever is stored when IT runs, so an author who batches an update
		// and a verify in one turn verifies the copy it just replaced, reads a
		// report about code it already rewrote, and "fixes" a line that no
		// longer exists. Checking the write's own output cannot go stale.
		if len(appHTMLSectionScripts(raw)) > 0 {
			errs, checked := appPageRuntimeErrors(t.user, saved.Slug)
			pageChecked = checked
			if len(errs) > 0 {
				t.noteAppStanding(saved.Slug, false, "its page fails in a real browser: "+errs[0])
				return appWithParseNotes(fmt.Sprintf("%s app %q, BUT the page FAILS IN A REAL BROWSER, this is the revision you just saved, not an older one:\n- %s\n\nFix it with app_def(action=\"update\", id=%q, …) (it re-checks on save). Send the WHOLE corrected document, and do NOT tell the user the app is ready.%s",
					verb, saved.Name, strings.Join(errs, "\n- "), saved.Slug, plan), parseNotes), nil
			}
		}
	}

	// Auto-verify the data sources: they fire when the page first opens (a table or
	// display fetches them), so a script that crashes is exactly the "errors on
	// load" footgun. Run them here — read-only by design, safe to execute — and on
	// failure return an error-shaped result so the author fixes the script before
	// telling the user it's ready, rather than the user hitting the 500. Actions
	// (the write side; a fetch cap can reach an external API) are NOT auto-run —
	// they wait for an explicit app_def action=test.
	if len(saved.DataSources) > 0 {
		report, _, _, fail := t.checkScripts(saved, false, nil, nil)
		if fail > 0 {
			t.noteAppStanding(saved.Slug, false, "a data source fails its check (see the last app_def result)")
			return appWithParseNotes(fmt.Sprintf("%s app %q, BUT a data source FAILED to run, the app will error on load until this is fixed:\n\n%s\nFix the script with app_def(action=\"update\", id=%q, …) (it re-checks on save). Do NOT tell the user the app is ready yet.%s",
				verb, saved.Name, strings.TrimSpace(report), saved.Slug, plan), parseNotes), nil
		}
		msg += "\n\nData source check, all passed:\n" + strings.TrimSpace(report)
		msg += "\nTip: run app_def(action=\"test\", id=\"" + saved.Slug + "\", sample=[{…example form entry…}]) to confirm the full form→data-source→output chain produces real output."
	}
	contractBroken := false
	if len(saved.DataSources) == 0 {
		// No scripts to run, so the check above did not list these.
		if lines, n := appContractReport(saved); n > 0 {
			contractBroken = true
			msg += "\n\n" + strings.TrimSpace(lines)
		}
		msg += appBuildGaps(t.user, saved, appGapInput{})
	}
	// What to say about verification depends on what this save already did. An
	// html-section app was just loaded in a real browser above, so telling the
	// author to go verify it invites the exact loop this check exists to end:
	// a verify batched alongside the NEXT update reports on the revision being
	// replaced, and its findings read as fresh.
	if p := t.appModeTypedOnlyProblem(saved); p != "" {
		msg += "\n\nNOT DONE: " + p
		t.noteAppStanding(saved.Slug, false, "built only from typed sections in an app session")
	} else if contractBroken {
		t.noteAppStanding(saved.Slug, false, "the page calls an endpoint the app does not have (see the last app_def result)")
	} else if pageChecked {
		t.noteAppStanding(saved.Slug, true, "")
		// The Status clause up top was written before this save loaded the
		// page, and it still said "never verified: run verify", so one result
		// told the author both that the page had passed and that it had not
		// been checked.
		msg = strings.Replace(msg, "Status: "+saved.VerifyStatus()+".", "Status: loaded in a real browser by this save, no JS errors.", 1)
		msg += "\nThis save already parsed the inline JavaScript AND loaded /apps/" + saved.Slug + "/ in a real browser: it rendered with no JS errors. That check covered THIS revision, so you don't need a separate verify unless you change the app again."
	} else if !isUpdate || appUpdateChangesBehavior(args) {
		t.noteAppStanding(saved.Slug, false, "saved, and not yet verified in a browser: run app_def(action=\"verify\")")
		msg += "\nBefore telling the user the app is ready, run app_def(action=\"verify\", id=\"" + saved.Slug + "\"): it loads the page in a real browser and catches render/JS/fetch failures the script checks can't see. Run it in a LATER turn than the update, never batched alongside one: verify reads whatever is stored when it runs, so an update and a verify in the same turn can report on the copy you just replaced."
	} else {
		msg += "\nBefore telling the user the app is ready, run app_def(action=\"verify\", id=\"" + saved.Slug + "\"): it loads the page in a real browser and catches render/JS/fetch failures the script checks can't see. Run it in a LATER turn than the update, never batched alongside one: verify reads whatever is stored when it runs, so an update and a verify in the same turn can report on the copy you just replaced."
	}
	return msg + plan, nil
}

// appCompletePlan is what a complete first build includes, in the create
// result. It lived in the notes param's description, read once at most, and
// a build with that text in its catalog still stated no plan and shipped the
// literal request; the call that just made the app is where the next step is
// actually decided.
func appCompletePlan() string {
	return "\n\nCOMPLETE IT BEFORE YOU REPLY. A request names what an app is for, not every part of it, and the user should not have to come back for the obvious. Check THIS app against each and add what is missing now:\n" +
		"1. Real data from where it lives, one of the owner's tools first (capabilities tool:<name>, call_tool), never invented values standing in for it.\n" +
		"2. What a person enters is saved AND shown back, so they can see, correct and remove it.\n" +
		"3. Pictures where they carry meaning (an icon per weather, a chart for numbers over time): made, found or generated, never left for the user to supply.\n" +
		"4. A setting for any value a person would want to change (units, a default, a count).\n" +
		"5. A first visit that says what to do, not a blank page.\n" +
		"Stay inside the app's purpose: this is what a complete version of THAT includes, not extra features. Then write the plan into notes (update notes=), and open your reply with one or two lines naming what you added beyond the request and anything you left out and why."
}

// floatArg reads a number argument, as JSON sends it or as text; 0 when it is
// neither.
func floatArg(args map[string]any, key string) float64 {
	switch v := args[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%g", &f); err == nil {
			return f
		}
	}
	return 0
}

// appUpdateChangesBehavior reports an update that touches what the app does,
// which a check has to see again. A notes or name edit leaves a verified app
// verified.
func appUpdateChangesBehavior(args map[string]any) bool {
	for _, k := range []string{"sections", "data_sources", "actions", "libraries", "settings", "shared_collections", "record_key", "agent_id", "pipeline_id"} {
		if _, ok := args[k]; ok {
			return true
		}
	}
	return false
}
