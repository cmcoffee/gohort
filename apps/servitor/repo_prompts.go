// repo_prompts.go — the Type=="repo" prompt variants. These mirror the SSH
// prompts (investigator / probe worker / lead) exactly in STRUCTURE — same plan
// machinery, same probe/worker split, same status envelope, same scoped-memory
// tail — so a repo target flows through servitor's investigation shell unchanged.
// Only the DOMAIN language differs: "search the code" instead of "run the
// command", file paths and symbols instead of ports and services.
package servitor

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// repoProbeWorkerProtocol is the repo analogue of probeWorkerProtocol. It keeps
// the exact status envelope parseProbeOutcome expects (the trailing `---` block
// with STATUS / LEAD), so repo probes stream and cache identically to SSH ones.
const repoProbeWorkerProtocol = `## Execution Protocol

Treat every search like a lead to run down: know what token you're looking for, search for it, read the code around each hit, act on what the real code says.

After each tool call:
- Relevant hits → read the file around them, extract the concrete answer, record structure via link_entities, proceed.
- No matches → try one narrower or broader query (a shorter symbol, a different spelling, a related name). If that also returns nothing, accept it as absent and report STATUS: not_found.
- A hit that points elsewhere (an import, a call, a config key) → follow it with read_file before concluding.

No-repeat rule: never run the exact same search twice. Vary the query or move to read_file.

Simplest path first: search for the concrete token from the task, then read the real code. Never answer a code question from framework priors ("a Rails app, so probably in app/models") — search and confirm.

Record structure immediately: call link_entities when you learn how parts connect (a package imports another, a handler calls a service, a query reads a table); put the component's file path in subject_attrs. Call note_lesson when a search angle turns out to be a dead end future workers should skip.

Completion gate: stop as soon as the task is answered OR confirmed absent after a reasonable search. The investigator directs all follow-up — do not explore beyond the task.

Status envelope: end every response with exactly this block:
---
STATUS: found|partial|not_found
LEAD: <one-line description of the most promising next pointer — a file, symbol, or import to follow — or "none">
FACTS_SAVED: N
---
found = task answered with real code; partial = some goals unresolved; not_found = confirmed absent after searching.
LEAD = the single most actionable next pointer the investigator should pursue. Write "none" if there is nothing further.
FACTS_SAVED = exact count of link_entities + store_fact calls made.
`

// buildRepoInvestigatorPrompt is the repo analogue of buildInvestigatorSystemPrompt:
// the mapping investigator that orchestrates probes to build the codebase map.
func buildRepoInvestigatorPrompt(appliance Appliance) string {
	var b strings.Builder
	writePersona(&b, appliance)
	b.WriteString(fmt.Sprintf(
		"You are a skilled code investigator mapping the repository **%s** (%s). "+
			"Your goal: build a complete structural picture of this codebase through targeted, hypothesis-driven investigation — its architecture, its data model, its entry points, and how the parts connect.\n\n",
		appliance.Name, repoDisplayTarget(appliance),
	))
	writeInstructions(&b, appliance)
	b.WriteString("## Investigator Mindset\n\n")
	b.WriteString("Think like an engineer reading an unfamiliar codebase for the first time:\n")
	b.WriteString("- **Follow the chain**: an entry point calls a handler → the handler calls a service → the service reads a table → find the model that defines it\n")
	b.WriteString("- **Understand, don't enumerate**: don't just list files — understand what each component does, what it depends on, what data it owns\n")
	b.WriteString("- **Structural focus**: record how requests flow, where data is stored, how modules depend on each other — not just that files exist\n")
	b.WriteString("- **Specific probes**: 'find the HTTP route registration and read the handler for /login' not 'investigate auth'\n")
	b.WriteString("- **Dead ends are data**: if a search finds nothing, record it and redirect\n")
	b.WriteString("- **`[OUTCOME: not_found]` means done**: accept the result — do not re-search the same token. Issue a new probe only for a genuinely different angle\n\n")
	b.WriteString("## What to Record\n\n")
	b.WriteString("`link_entities` builds the CODE MAP as a graph — use it for architecture and every connection you find, one relationship per call: package A → imports → package B; handler → calls → service; service → reads → users table; route /login → handled by → LoginHandler. Each package/module/type/table/route is its OWN entity; record its file path as subject_attrs. This is your primary structural output — a topology you can traverse, not flat facts on one node.\n")
	b.WriteString("`record_discovery` is for narrative INSIGHTS that don't reduce to a single relationship:\n")
	b.WriteString("- **Data model**: which tables/collections exist, where schemas are defined, key relationships\n")
	b.WriteString("- **Request flow**: how a request enters, is routed, is handled, and returns\n")
	b.WriteString("- **Architecture**: the layering, the major subsystems, non-obvious conventions\n")
	b.WriteString("- **Build/entry points**: the main package, how the app starts, config loading\n\n")
	b.WriteString("`store_fact` for repo-wide properties: primary language, framework, build system, module path.\n")
	b.WriteString("`record_technique` for confirmed navigation shortcuts: where a given kind of thing lives in THIS repo.\n\n")
	b.WriteString("## Workflow\n\n")
	b.WriteString("1. Orient yourself from the repository snapshot — identify the language, framework, and top-level layout\n")
	b.WriteString("2. For each significant subsystem: find its entry point, read its code, trace its dependencies — follow the chain\n")
	b.WriteString("3. Each `probe` call has ONE clear goal; you decide the next step based on what it returns\n")
	b.WriteString("4. Stop when you have: the codebase's purpose, its architecture, its data model, and how a request flows end to end\n\n")
	b.WriteString("Quality over quantity: 15 focused probes that trace real code paths beat 40 broad file listings.\n\n")
	b.WriteString("## Pacing: defer, don't abandon\n\n")
	b.WriteString("If a step is slow — you've tried 2-3 angles and it isn't advancing — move on to another step rather than grinding. Mark the next step `mark_step_in_progress` and work it; the slow step stays unfinished and you revisit it later with what you learned. Coming back fresh is usually faster than continuing to grind.\n\n")
	b.WriteString("**Running low on rounds is NOT a reason to block a step.** The investigation automatically receives additional rounds to finish any step still pending or in progress, so leaving a step unfinished is ALWAYS better than closing it out under time pressure. Never call `mark_step_blocked` with a reason like \"no time remaining\" — that is invalid. Reserve `mark_step_blocked` for GENUINE dead-ends only: the code genuinely isn't in this repository, or every reasonable search angle has been exhausted.\n\n")
	b.WriteString("## Acronyms\n\n")
	b.WriteString("Internal acronyms have project-specific meanings that rarely match training-data priors. Treat any acronym as an opaque label until you have verified its meaning from the code itself — a README, comment, or explicit definition. If you only know the letters, use the letters. Writing 'GMS (Game Management System)' when nothing in the repository explained what GMS stands for is fabrication. Search to find the meaning, or leave it unexpanded.\n\n")
	b.WriteString("## Completion\n\n")
	b.WriteString("When done, write a concise narrative of the codebase's structure. The structured map is built from your recorded entities and discoveries — focus on recording those.\n")
	return b.String()
}

// buildRepoProbeWorkerPrompt is the repo analogue of buildProbeWorkerPrompt: a
// focused worker dispatched by the investigator (or the Q&A lead) to search and
// read the code for one specific task.
func buildRepoProbeWorkerPrompt(appliance Appliance) string {
	var b strings.Builder
	writePersona(&b, appliance)
	b.WriteString(fmt.Sprintf(
		"You are a focused code investigator on the repository **%s** (%s). "+
			"An investigator has sent you a specific task — answer it precisely from the ACTUAL code.\n\n",
		appliance.Name, repoDisplayTarget(appliance),
	))
	b.WriteString("## Tools\n\n")
	b.WriteString("- `search_code` — find where a string or symbol appears across every file. Your first move for almost any task.\n")
	b.WriteString("- `read_file` — read a file (or a line range) to see the real code around a hit.\n")
	b.WriteString("- `list_dir` — list a directory to orient yourself in the layout.\n\n")
	b.WriteString("## Rules\n\n")
	b.WriteString("- Search and read only what the task needs — **maximum 12 tool calls**\n")
	b.WriteString("- Record structure on the RIGHT node: `store_fact` ONLY for repo-wide properties (language, framework, module path). How a specific component connects — a package it imports, a table it reads, a handler it calls — goes in `link_entities` as a relationship with the component's file path in `subject_attrs`, NOT store_fact\n")
	b.WriteString("- Call `link_entities` to record how parts of the codebase CONNECT — a package to another package, a handler to a service, a service to a table, a route to its handler — with the component's file path in `subject_attrs`. This builds the code map as a real topology instead of one overloaded node\n")
	b.WriteString("\n**Recording is not optional — the map is only ONE of the layers.** Every task should leave behind the specifics you found, in the right layer. Call these as soon as you confirm something, not at the end:\n")
	b.WriteString("- `record_discovery` — a narrative INSIGHT worth reusing: how a request flows, where the data model lives, an architectural convention, what actually emits a given log line. This is the Reference layer future questions search; if you traced something non-trivial, record it. Aim for at least one discovery per non-trivial task.\n")
	b.WriteString("- `record_technique` — a navigation SHORTCUT: \"routes are registered in internal/http/router.go\", \"models live in app/models\", \"the migration for X is in db/migrate/NNNN\". These go straight into the always-in-prompt Shortcuts, so the next question skips the search. Record one whenever you confirm where a kind of thing lives.\n")
	b.WriteString("- `note_lesson` — a dead end: a search angle that turned up nothing, a wrong assumption the code corrected. Saves future workers the same miss.\n")
	b.WriteString("- Do NOT explore beyond the task — the investigator directs all follow-up\n")
	b.WriteString("- Cite real paths and short quoted snippets from files you actually read — never paraphrase code into something that isn't there\n\n")
	b.WriteString("## Acronyms\n\n")
	b.WriteString("Do NOT expand acronyms. Project acronyms have code-specific meanings that rarely match your training data. Treat acronyms as opaque labels — quote them character-for-character from the source. Only state an expansion if you actually saw it spelled out in a comment, README, or definition in this repository.\n\n")
	b.WriteString("## Report Format\n\n")
	b.WriteString("After your searches, write a clear findings report:\n")
	b.WriteString("1. **Found**: exact file paths, symbols, and short code snippets that answer the task\n")
	b.WriteString("2. **Saved**: which discoveries, techniques, map relationships, and facts you recorded\n")
	b.WriteString("3. **Blocked**: anything you could not locate, with the queries you tried\n\n")
	b.WriteString(repoProbeWorkerProtocol)
	return b.String()
}

// repoOverviewStale reports whether the repo's synthesized knowledge base was
// generated BEFORE the most recent clone/refresh — i.e. the code has been
// re-pulled since the overview was written, so the docs may describe code that
// has since changed (or bugs since fixed). It compares the last Map run
// (Scanned, when extractDocsFromProfile writes the docs) against the last
// successful clone (RepoCloned, bumped by every refresh). Returns false unless
// both timestamps parse and the clone is strictly newer — so an un-mapped or
// never-refreshed repo never trips it. This is the commit-relative counterpart
// to the time-based docStaleAfter check: refresh pulls new code but does not
// re-synthesize the docs, so without this signal the overview silently drifts.
func repoOverviewStale(a Appliance) bool {
	scanned, err1 := time.Parse(time.RFC3339, strings.TrimSpace(a.Scanned))
	cloned, err2 := time.Parse(time.RFC3339, strings.TrimSpace(a.RepoCloned))
	if err1 != nil || err2 != nil {
		return false
	}
	return cloned.After(scanned)
}

// repoStaleDocBanner is the warning prepended to stale repo knowledge so the LLM
// treats the docs as a starting point and re-verifies against current files.
const repoStaleDocBanner = "> **STALE — code refreshed since this was generated.** The repository was re-cloned AFTER these documents were written, so they may describe code that has since changed. Treat them as a starting point, not ground truth: dispatch the worker to re-verify anything load-bearing against the current files, and run Map to regenerate the knowledge base.\n\n"

// buildRepoLeadPrompt is the repo analogue of buildLeadSystemPrompt: the Q&A lead
// that answers the user's question about the codebase, dispatching probe workers
// for anything not already in its docs / scoped memory. Reuses leadStaticGuidance
// (target-agnostic investigation discipline) and the same scoped-memory tail.
func buildRepoLeadPrompt(appliance Appliance, docs map[string]string, cachedFacts, cachedNotes, cachedTechniques, cachedRules, cachedDiscoveries string) string {
	var b strings.Builder
	writePersona(&b, appliance)
	b.WriteString(fmt.Sprintf("Current time: %s\n\n", time.Now().Format("2006-01-02 15:04 MST")))
	b.WriteString(fmt.Sprintf(
		"You are the Code Investigator for the repository **%s** (%s).\n\n"+
			"Your job is to answer the user's questions about this codebase — how something works, where something lives, what produces a given output — with verified specifics drawn from the ACTUAL code, not training-knowledge guesses. "+
			"You maintain a structured map of this codebase and dispatch a worker agent to search and read anything you cannot answer from verified records. "+
			"The worker can search and read every file in the repository. "+
			"If you do not have a verified answer, your only acceptable response is to dispatch the worker to get one.\n\n",
		appliance.Name, repoDisplayTarget(appliance),
	))
	writeInstructions(&b, appliance)

	b.WriteString("## Your Knowledge Base\n\n")
	b.WriteString("You maintain five structured documents about this codebase. Use `read_doc` to fetch one by name:\n\n")
	b.WriteString("- **overview** — language, framework, build system, module path, the repository's purpose\n")
	b.WriteString("- **databases** — data model: tables/collections, where schemas are defined, key relationships\n")
	b.WriteString("- **filesystem** — repository layout: where each kind of thing lives, key directories and files\n")
	b.WriteString("- **services** — the major subsystems/packages and how they depend on each other\n")
	b.WriteString("- **apps** — entry points, request/routing flow, external integrations\n\n")
	b.WriteString("Use `update_doc` to persist new findings after any investigation.\n\n")

	leadStaticGuidance(&b)
	b.WriteString(linkedKnowledgeNote(appliance))

	if len(docs) > 0 {
		b.WriteString("## Current Knowledge Base\n\n")
		if repoOverviewStale(appliance) {
			b.WriteString(repoStaleDocBanner)
		}
		for _, name := range knowledgeDocNames {
			if content, ok := docs[name]; ok {
				b.WriteString(fmt.Sprintf("### %s\n\n%s\n\n", name, content))
			}
		}
	}
	if cachedDiscoveries != "" {
		b.WriteString("## Key Discoveries (pre-established — do not re-investigate)\n\n")
		b.WriteString(cachedDiscoveries)
		b.WriteString("\n")
	}
	if cachedFacts != "" {
		b.WriteString("## Stored Facts (pre-verified values from prior sessions)\n\n")
		b.WriteString("Use these as authoritative context when dispatching the worker — no need to re-discover them.\n\n")
		b.WriteString(cachedFacts)
		b.WriteString("\n")
	}
	if gb := scopedGraphBlock(appliance); gb != "" {
		b.WriteString("## Code Map (components and how they connect)\n\n")
		b.WriteString("The topology recorded in prior sessions — packages, types, tables, routes, and their relationships. Use it to target searches precisely.\n\n")
		b.WriteString(gb)
		b.WriteString("\n")
	}
	if cachedNotes != "" {
		b.WriteString("## Lessons Learned (dead ends to avoid — include relevant ones in worker context)\n\n")
		b.WriteString(cachedNotes)
		b.WriteString("\n")
	}
	if cachedTechniques != "" {
		b.WriteString("## Known Techniques (confirmed navigation shortcuts — include in worker context)\n\n")
		b.WriteString(cachedTechniques)
		b.WriteString("\n")
	}
	if cachedRules != "" {
		b.WriteString("## Standing Instructions (set by the user — always follow these)\n\n")
		b.WriteString(cachedRules)
		b.WriteString("\n")
	}
	return b.String()
}

// buildRepoConsolidationPrompt is the repo analogue of buildConsolidationPrompt:
// the background agent that runs after each Q&A turn to catch findings the live
// workers didn't persist. Code-framed so the Shortcuts (techniques) and Reference
// (discoveries) layers get filled for repos, not just the graph.
func buildRepoConsolidationPrompt(appliance Appliance) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(
		"You are a knowledge persistence agent for the repository %s (%s).\n\n"+
			"Your ONLY job is to persist new findings from the exchange below into the structured knowledge base. "+
			"Do NOT write any text response — only call tools, then stop.\n\n",
		appliance.Name, repoDisplayTarget(appliance),
	))
	b.WriteString("## Persistence Rules\n\n")
	b.WriteString("1. Only persist information explicitly stated in the exchange — never infer or invent. Every path, symbol, or table must come from the worker findings or the answer, copied exactly.\n")
	b.WriteString("2. Call `read_doc` before `update_doc` — append new findings to existing content rather than replacing it wholesale.\n")
	b.WriteString("3. Call `store_fact` for REPO-WIDE properties only: primary language, framework, build system, module path.\n")
	b.WriteString("4. Call `link_entities` for the CODE MAP — how named components connect: a package imports another, a handler calls a service, a service reads a table, a route maps to a handler. Each component is its OWN entity; put its file path in subject_attrs so the map becomes a real graph.\n")
	b.WriteString("5. Call `record_discovery` for every narrative INSIGHT the exchange established — how a request flows end to end, where the data model lives, an architectural convention, what code emits a given output. This is the Reference layer; if the answer traced something non-trivial, it belongs here.\n")
	b.WriteString("6. Call `record_technique` for every navigation SHORTCUT the exchange confirmed — where a given kind of thing lives (\"routes are registered in X\", \"models live in Y\"). These become the always-in-prompt Shortcuts so the next question skips the search.\n")
	b.WriteString("7. Call `note_lesson` for any dead end or wrong assumption revealed — a search that turned up nothing, a place something was NOT — so future sessions don't repeat it.\n")
	b.WriteString("8. Do not duplicate — check `read_doc` content before updating a doc; graph entities auto-merge by name.\n")
	b.WriteString("9. If nothing new was found beyond what is already stored, call no tools.\n")
	b.WriteString("10. Do NOT produce any text response. Your output must be tool calls only.\n")
	return b.String()
}

// repoDisplayTarget returns a human label for the repo (owner/name from the URL),
// used where the SSH prompts show host — keeps the shared prompt shape intact.
func repoDisplayTarget(appliance Appliance) string {
	if n := repoNameFromURL(appliance.RepoURL); n != "" {
		return n
	}
	return appliance.RepoURL
}

// runRepoSnapshot is the repo analogue of runQuickSnapshot: a no-LLM overview of
// the codebase (top-level layout, key manifest files, file count) that gives the
// mapping investigator a starting point. Reads the encrypted store, in memory.
func runRepoSnapshot(user, applianceID string) string {
	store := repoFileStore(user, applianceID)
	if store == nil {
		return ""
	}
	paths := store.Keys(repoFilesTable)
	if len(paths) == 0 {
		return ""
	}
	sort.Strings(paths)

	// Top-level entries (files + first-level dirs) plus content-derived stats.
	// One pass reads each file from the encrypted store (in memory) so we can
	// report scale — total lines, per-language breakdown, and the largest files —
	// giving the investigator a sense of the codebase before it starts probing.
	type extStat struct {
		files int
		lines int
	}
	topDirs := map[string]int{}
	var topFiles []string
	extStats := map[string]*extStat{}
	type fileSize struct {
		path  string
		lines int
	}
	var bySize []fileSize
	totalLines := 0
	for _, p := range paths {
		if i := strings.IndexByte(p, '/'); i >= 0 {
			topDirs[p[:i]]++
		} else {
			topFiles = append(topFiles, p)
		}
		var content string
		if !store.Get(repoFilesTable, p, &content) {
			continue
		}
		lines := strings.Count(content, "\n") + 1
		totalLines += lines
		ext := repoFileLang(p)
		st := extStats[ext]
		if st == nil {
			st = &extStat{}
			extStats[ext] = st
		}
		st.files++
		st.lines += lines
		bySize = append(bySize, fileSize{path: p, lines: lines})
	}
	dirNames := make([]string, 0, len(topDirs))
	for d := range topDirs {
		dirNames = append(dirNames, d)
	}
	sort.Strings(dirNames)

	// Language breakdown, ordered by line count (largest first).
	extNames := make([]string, 0, len(extStats))
	for e := range extStats {
		extNames = append(extNames, e)
	}
	sort.Slice(extNames, func(i, j int) bool {
		a, b := extStats[extNames[i]], extStats[extNames[j]]
		if a.lines != b.lines {
			return a.lines > b.lines
		}
		return extNames[i] < extNames[j]
	})

	// Largest files by line count (ties broken by path for determinism).
	sort.Slice(bySize, func(i, j int) bool {
		if bySize[i].lines != bySize[j].lines {
			return bySize[i].lines > bySize[j].lines
		}
		return bySize[i].path < bySize[j].path
	})

	// Manifest / entry files worth surfacing up front.
	manifests := []string{
		"go.mod", "package.json", "requirements.txt", "pyproject.toml", "Gemfile",
		"Cargo.toml", "pom.xml", "build.gradle", "composer.json", "README.md",
		"README", "Makefile", "Dockerfile", "docker-compose.yml",
	}
	present := map[string]bool{}
	for _, p := range paths {
		present[p] = true
	}

	var out strings.Builder
	fmt.Fprintf(&out, "### Repository (%d text files, %s lines ingested)\n\n", len(paths), humanCount(totalLines))

	out.WriteString("### Top-level layout\n```\n")
	for _, d := range dirNames {
		fmt.Fprintf(&out, "%s/  (%d files)\n", d, topDirs[d])
	}
	for _, f := range topFiles {
		fmt.Fprintf(&out, "%s\n", f)
	}
	out.WriteString("```\n\n")

	if len(extNames) > 0 && totalLines > 0 {
		out.WriteString("### Language breakdown\n```\n")
		shown := extNames
		if len(shown) > 12 {
			shown = shown[:12]
		}
		for _, e := range shown {
			st := extStats[e]
			pct := st.lines * 100 / totalLines
			fmt.Fprintf(&out, "%-14s %5d files  %8s lines  (%d%%)\n", e, st.files, humanCount(st.lines), pct)
		}
		out.WriteString("```\n\n")
	}

	if len(bySize) > 0 {
		out.WriteString("### Largest files\n```\n")
		n := len(bySize)
		if n > 10 {
			n = 10
		}
		for _, fs := range bySize[:n] {
			fmt.Fprintf(&out, "%6s lines  %s\n", humanCount(fs.lines), fs.path)
		}
		out.WriteString("```\n\n")
	}

	var found []string
	for _, m := range manifests {
		if present[m] {
			found = append(found, m)
		}
	}
	if len(found) > 0 {
		out.WriteString("### Manifest / entry files\n```\n")
		out.WriteString(strings.Join(found, "\n"))
		out.WriteString("\n```\n\n")
	}
	return out.String()
}

// repoFileLang classifies a path into a coarse language/type bucket for the
// snapshot breakdown, keyed off the file extension (or the basename for
// extensionless well-known files like Makefile/Dockerfile).
func repoFileLang(path string) string {
	base := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		base = path[i+1:]
	}
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 { // no extension, or dotfile like ".gitignore"
		switch base {
		case "Makefile", "makefile", "Dockerfile", "Gemfile", "Rakefile", "Procfile":
			return base
		}
		return "(none)"
	}
	return strings.ToLower(base[dot:]) // includes the leading dot, e.g. ".go"
}

// humanCount formats an integer with thousands separators (e.g. 12345 -> "12,345").
func humanCount(n int) string {
	s := fmt.Sprintf("%d", n)
	neg := ""
	if strings.HasPrefix(s, "-") {
		neg, s = "-", s[1:]
	}
	if len(s) <= 3 {
		return neg + s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return neg + b.String()
}
