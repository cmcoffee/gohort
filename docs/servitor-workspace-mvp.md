# Servitor Workspaces — cross-appliance investigation (MVP)

Status: design. Captures the direction of a "master" appliance that queries
several existing appliances — repos *and* SSH boxes — at once, so the decisions
are settled before code. Builds directly on the per-appliance machinery
(ingest, docs, scoped graph, refresh/staleness) already in `apps/servitor/`.

## The idea

Today every servitor appliance is investigated in isolation: one repo, or one
SSH box, per session. Real questions cross that boundary:

> "This log line is showing up on the lab box — which repo's code emits it, and
> what triggers it?"

The box knows *what's running* (grep the live log, find the process, read the
config that's deployed). The repos know *why the code does it* (which function
builds that string, who calls it, what config path reaches it). Neither alone
answers "who and why."

A **workspace** is a new appliance whose configuration is a checkbox list of
existing appliances. It owns no store of its own — it references members and
adds a coordinator layer that fans a question out to the relevant members and
stitches the answers together.

```
                    Workspace  (master appliance: member IDs + coordinator lead)
                        │
        ┌───────────────┼────────────────┬───────────────┐
        ▼               ▼                 ▼               ▼
   repo: logging-lib  repo: service-B  ssh: lab-box   repo: orchestrator
   (search code)      (search code)    (run commands) (search code)
        └───────────────┴─────── each member keeps its OWN ───────────┘
                    store · docs · scoped graph · creds · refresh lifecycle
```

## Why this fits servitor's grain

Servitor already runs SSH appliances and repo appliances through the **same
investigation shell**: `buildLeadSystemPrompt` (`web.go`) delegates to
`buildRepoLeadPrompt` for repos, and both share `leadStaticGuidance`, the
lead→worker dispatch loop, and the scoped-memory tail. The only real difference
is the worker's verb — an SSH member's worker *runs commands*, a repo member's
worker *searches code*. Both workers already exist.

So a workspace is a coordinator over machinery that is already built. It is not
a rebuild. Members keep everything: `resolveAppliance` already runs a member in
its owner's context (creds, clone, accumulated knowledge), so no new credential
model is needed.

## The coordinator: scout, then drill

A workspace question runs in two phases so we never pay for investigations that
find nothing.

**Scout (cheap, always).** Find which members are relevant to the question. The
scout differs by member type:

- **Repos are pre-ingested** → union search across their ingested code stores.
  Repos are already Source-partitioned in the store, so this is a multi-member
  variant of `searchRepo` (`repo_backend.go`).
- **SSH boxes are live** → you cannot "union search" a running server. The scout
  for a box reads its accumulated **map** (the knowledge docs / facts / scoped
  graph we already maintain via `allDocs` + `scopedGraphBlock`). Live commands
  are deferred to the drill.

**Drill (only where it matters).** Dispatch a focused member sub-agent into just
the members that scored relevant, each using that member's own docs/graph and
the type-appropriate worker:

- repo member → the existing repo probe worker (search + read files)
- ssh member  → the existing SSH command worker (run commands on the box)

**Synthesize.** The coordinator lead collects the per-member findings and writes
the cross-domain answer, stitching the trace across boundaries at synthesis time
(log string on the box ← function in repo A ← service configured on the box).

```
question ─► SCOUT ─┬─ repos: union code search ──┐
                   └─ boxes: read the map ───────┤► relevant members
                                                 │
                   DRILL (relevant only) ─┬─ repo worker: search/read code
                                          └─ ssh worker: run commands
                                                 │
                   SYNTHESIZE ◄──────────────────┘  cross-domain answer
```

Pure per-member fan-out (a full sub-agent into every member for every question)
is the tempting version, but it burns N worker contexts even for members with
zero relevant hits. Scout-then-drill keeps the depth without the waste.

## Data model

A workspace is a new `Type` on the existing `Appliance` record (`web.go`), so it
flows through the same create/edit/list/share plumbing:

- `Type == "workspace"`
- `Members []string` — member appliance IDs (new field, `json:"members,omitempty"`)
- reuses `Name`, `Instructions`, `PersonaName`/`PersonaPrompt`, `Shared`, `Owner`

No `Host`/`RepoURL`/creds of its own. On create/edit, the member picker lists the
user's SSH + repo appliances (and shared ones they can see) as checkboxes and
stores the selected IDs.

Member resolution at query time reuses `resolveAppliance(userID, udb, memberID)`
per member, so ownership/sharing and per-member creds are already handled. A
member the user can no longer see is skipped with a note, not an error.

## Component breakdown

**Reuse as-is (no changes):**
- per-repo ingest, refresh, doc-staleness flag, repo probe/worker loop
- per-box SSH connection pool, command worker, the 5 knowledge docs
- `resolveAppliance` (member context + creds), `scopedGraphBlock`, `allDocs`
- the lead→worker dispatch loop and scoped-memory tail

**New (small):**
- `Type == "workspace"` + `Members []string` on the Appliance record
- member-checkbox picker in the create/edit form (`web_assets.go`)
- a union code-search primitive over N repo members (multi-member `searchRepo`)
- `buildWorkspaceLeadPrompt` + a member-scout summary block (per-member: type,
  a one-line map/overview, whether it had scout hits)
- a coordinator dispatch tool: "investigate member <id> for <sub-question>" that
  spins up the member's existing worker in that member's context, returns findings

**Deferred (v2, not MVP):**
- a **persistent cross-domain graph** (product-level edges like
  `service-B —emits→ log-format` recorded across members). This is where the
  graph O(n) scan concern (`FindGraphEntity`, `GraphEdgesTo`) starts to bite at
  workspace scale, so build it only once the MVP proves the workflow. See the
  orchestrate graph-index notes.
- parallel drill fan-out (MVP can drill members sequentially first)
- workspace-level accumulated memory (MVP leans on each member's own memory)

## Build slices

1. **Record + UI.** `Type=="workspace"` + `Members` field; member-checkbox
   picker; list/edit/share. A workspace that lists its members and does nothing
   else yet. Verifies the data model end-to-end.
2. **Union scout.** Multi-member code search over repo members + a map-read for
   SSH members; a scout that returns "relevant members" for a question. No drill
   yet — surface the scout result so we can eyeball relevance quality.
3. **Coordinator drill + synthesis.** `buildWorkspaceLeadPrompt` + the member
   dispatch tool; sequential drill into relevant members; stitch at synthesis.
   This is the first end-to-end answer to the log-line question.
4. **Polish.** Parallel drill, better scout ranking, empty-state UX, and decide
   whether workspace-level memory is worth it.

Slices 1–3 are the MVP. Each is independently shippable and testable.

## Open questions

- **Name.** "Workspace" vs "Investigation" vs "Product". Workspace reads
  general; Product implies code-only, which undersells the mixed use case.
- **Scout ranking.** Union code search gives per-repo scores; the box map-read
  is qualitative. How does the coordinator compare "repo hit at 0.7" against
  "box map mentions this subsystem" to pick drill targets? MVP: drill any member
  above a code-score floor OR whose map the lead judges relevant, and let the
  lead decide — tune later.
- **Drill budget.** Cap on how many members a single question drills into, to
  bound cost on large workspaces.
- **Staleness surfacing.** The scout leans on each member's map; a workspace is a
  natural place to show "3 of 5 members have stale overviews — re-map?" (reuses
  the repo `repoOverviewStale` signal and the SSH doc age).

## References

- `apps/servitor/` — per-appliance investigation shell this builds on
- doc-staleness + refresh: `repoOverviewStale` (`repo_prompts.go`), the
  commit-relative flag added alongside this work
- orchestrate graph-index scale notes (deferred v2 cross-domain graph)
