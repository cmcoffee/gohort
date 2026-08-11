# Servitor toolset appliances — investigating a service through curated tools

Status: **slices 1 and 2 built** (v0.6.019). A servitor appliance whose target is
not a host or a clone but a **curated set of already-authored tools**. GitLab
becomes a configuration rather than a pair of Go files, and so does everything
after it.

What shipped, and where it differs from the design below:

- `apps/servitor/toolset.go` — bindings, fingerprinting, resolution, the
  bindable-tools endpoint. `toolset_prompts.go` — a first cut of the four
  prompts. `tool_guard.go` — `assertAllowedWithBindings`, the per-appliance
  carve-out.
- **Fingerprints are stamped server-side on save, never accepted from the
  client.** A caller-supplied hash would let it bless a body nobody approved,
  which is the whole thing the pin prevents. They are computed against the
  OWNER's pool, so a shared appliance edited by an admin still pins the owner's
  tools.
- **A binding with no fingerprint is withheld**, not trusted. Only reachable via
  a hand-written record, but honoring it would make the pin optional.
- **The hash covers a toolbox's actions**, not just the parent — an action's URL
  changing under an approved binding is the same laundering shape.
- **Derived toolbox actions inherit their parent's posture and its sanction**,
  matched by longest prefix so `gitlab_issues` beats `gitlab` for
  `gitlab_issues_close`. Without that the guard would panic on a correct
  configuration; with a naive prefix match, `gitlab` would have sanctioned
  `gitlabber_delete_everything`.
- **Prompts are a first cut, not slice 3.** They carry the status envelope, the
  gap-vs-absence rule, the tool list, and the domain paragraph — enough to run
  real investigations — but not the full structural templates (pacing, acronym
  discipline, the complete recording guidance), so a toolset investigation is
  currently thinner than an SSH or repo one.
- **The snapshot binding is built** (slice 4's preferred option), since it is one
  flag on a binding and the resolution path was already there.
- `bindableTools` guards a nil `AuthDB` and returns nothing, so a deployment
  without the auth store withholds every binding and says why rather than
  crashing the request.

Fixed after first use (v0.6.020):

- **The resolved session had no `DB` handle**, so every api-mode bound tool
  failed with "requires a session with DB access" while looking correctly
  configured everywhere anyone would think to check — bound, fingerprint
  matching, listed in the prompt, visible in the picker.
- **`AuthDB` is a func var**, so calling it unguarded panicked rather than
  returning nil. Both call sites now go through `authDBOrNil`.

Workspace membership (the deferred slice 5 of the bundle spec, and its
equivalent here) also landed at v0.6.020: `wsMember.Kind` distinguishes
`repo` / `evidence` / `service` / `system` instead of folding everything
non-repo into "system", `Target` no longer returns a blank for the two new
types, the scout searches an evidence member's own logs rather than only what
was recorded about it, and the coordinator gained `search_evidence` alongside
`search_code`. A read-only workspace drill now WITHHOLDS an "ask"-posture tool
up front instead of offering it and denying each call — the posture is a
property of the binding, so that check runs before the pool lookup and needs no
store access.

## Still open: the model tier

`RouteStage.Private: true` locks every servitor stage to the local worker model
(`common.go:697`). That is a MODEL constraint, not a network one — a toolset
appliance reaches its service fine — but it means a technical investigation
through bound tools is reasoned about by the local worker.

Unpinning per TYPE is too coarse: ssh, command, repo and bundle all hold data
that should stay local, and a bundle of customer logs is the most sensitive
thing in the set. The shape that fits is a per-APPLIANCE flag defaulting to
pinned, letting an owner say "this target's contents may go to the lead tier" —
the same shape guides already has per document. Not built; it is a privacy
decision about real data rather than a code question.

## The idea

The Builder can already author a set of read tools against a service — GitLab
issues, merge requests, pipelines, files. What it cannot do is investigate with
them: no plan, no probe workers, no accumulated map of the project, no sense of
what it already established last week, no distinction between "I checked and it
is not there" and "I could not reach the thing that would tell me."

All of that exists in servitor, and none of it is SSH-specific.

> These mirror the SSH prompts exactly in STRUCTURE — same plan machinery, same
> probe/worker split, same status envelope, same scoped-memory tail — so a repo
> target flows through servitor's investigation shell unchanged. Only the DOMAIN
> language differs.
>
> — `apps/servitor/repo_prompts.go`, header

That has now been demonstrated four times: `ssh`, `command`, `repo`, and
`bundle`. Across all four, a type varies in exactly three things:

1. **which tools the worker gets** — `run_command` / `repoCodeTools` /
   `bundleCodeTools`
2. **domain language in four prompts** — investigator, probe worker, lead,
   consolidation
3. **the snapshot** that orients the investigator before it probes

Everything else is shared. So the type mechanism is already the general seam.
The limitation is that a type's tools must be Go code compiled into servitor —
which means one more pair of files for GitLab, then Jira, then ServiceNow, then
Datadog, forever.

This type closes that: **its toolset is data**.

### Why not a separate explorer agent or pipeline

Considered and rejected. It would fork the machinery — a second implementation
of plan-and-probe, the knowledge docs, staleness, the entity graph, and the
consolidation pass — and those two implementations would drift. Avoiding exactly
that split is why the repo type was written as a structural mirror instead of a
new thing.

### Why this is NARROWER than SSH, not wider

The instinct is that letting servitor call a network service loosens it. The
opposite is true, and `apps/servitor/appliance_tool.go` already argues it about
`run_command`:

> The model composes a shell string, so the injection surface is whatever it
> writes; the risk classifier runs against text that did not exist a second ago;
> and the permission an owner grants is a whole RISK CATEGORY — an open set of
> commands nobody has read.

A curated toolset inverts all three. The action space is finite and enumerable
instead of "whatever the model types". Nothing has to classify a string that did
not exist a second ago, because there is no string. And the grant is per tool
rather than per risk category — an owner approves things a person has read.

What changes is the DIRECTION of the boundary, not its size: from "local only,
network reached through binaries you installed" to "these specific calls." The
compile-time allow-list is simply the wrong shape for expressing that.

## What already exists

**The investigation shell**, switched by `appliance.Type` at nine sites:
`buildInvestigatorSystemPrompt`, `buildProbeWorkerPrompt`,
`buildLeadSystemPrompt`, `buildConsolidationPrompt`, the connection branch and
the scratch-directory skip in `runSession`, the worker-tool branch, the snapshot
branch, and the Map message. Adding a type today means touching those nine and
writing two files.

**Owner approval with frozen structure** — `appliance_tool.go`. An agent asks
for a capability in prose, servitor authors the template once, the owner
approves, and the model thereafter supplies values only. Notably it already
refuses to overwrite an approved tool, calling that "the laundering shape that
keeps recurring."

**Per-(agent, appliance) grants** — `command_grants.go`. Most-specific-wins,
present-but-empty means nothing auto-runs, and deliberately no wildcard.

**Builder-authored tools** — `TempTool` / `PersistentTempTool`
(`core/temp_tool_persist.go:331`), carrying `ApprovedAt`, `ScopeAgents`,
`AllowedUsers`, `Credential`, and `Mode`. Everything needed to define and own a
tool, already built and already in use.

**The posture vocabulary**, in three separate mechanisms that this type would
compose rather than replace: `ScopeAgents` (which agents may see it),
`NeedsConfirm` plus the confirm channel (ask before running), and
`TempTool.Credential` with the secured-credential binding (requires auth).

**Everything a target gets for free** once it is an appliance: knowledge docs
with staleness, the scoped graph, sharing, `Collections`, `LinkedRepos`,
workspace membership, and — as of the curator work — `record_finding` into the
documentation pipeline.

## What's missing

1. **No way to bind tools to an appliance.** Nothing on the record says "this
   target is investigated with these tools."
2. **`assertOnlyAllowedTools` is a compile-time map** (`tool_guard.go:101`) and
   *panics* on anything outside it. Correct for a fixed set of Go tools;
   unusable for a per-appliance list.
3. **Approval binds to a NAME, not a BODY.** `PersistentTempTool` records
   `ApprovedAt` but no fingerprint of what was approved, so a tool edited after
   approval keeps it. For SSH-minted tools the template is frozen at approval;
   for Builder tools it is not. This is the real risk in the whole design, and
   it is not about network reach.
4. **Prompts are hand-written Go per type**, four per type.
5. **The snapshot is a hand-written Go function per type.**
6. **Grants are risk CATEGORIES**, which are command-shaped ("database writes",
   "file deletion") and meaningless for a tool call. There is no per-tool
   posture within an appliance.

## The build

### Slice 1 — the `toolset` appliance, with body-pinned bindings

A sixth `Type: "toolset"`. It owns no host, no clone and no credentials of its
own; like `repo` and `bundle` it acquires no connection and gets no scratch
directory.

```go
// ToolBinding is one tool this appliance's investigations may call.
type ToolBinding struct {
    Name    string // the persisted tool's name
    Posture string // "allow" | "ask"
    // BodyHash fingerprints the tool AS APPROVED. Checked at every resolve;
    // a mismatch withholds the tool and says so.
    BodyHash  string
    BoundAt   string
    BoundBy   string
}
```

Plus a short `Domain` string on the record — what this target *is*, in the
owner's words ("a GitLab project: issues, merge requests, pipelines, and file
contents").

**Slice 1 and the body pin ship together, not one after the other.** A binding
that names a tool without pinning what it contained is a promise about a name,
and the gap between shipping the first and the second is exactly the window the
laundering shape lives in.

The hash covers everything that determines what the tool DOES — name,
description, parameters, required set, mode, request/script body, and credential
name — so a description-only edit re-prompts too. That is intentional: the
description is what the model acts on, and rewriting it changes behavior as
surely as rewriting the URL.

On mismatch the tool is **withheld**, not warned about. A tool that changed
since approval is a tool nobody has approved, and running it while displaying a
warning is the same thing as running it.

### Slice 2 — per-appliance tool resolution

`assertOnlyAllowedTools` grows a second mode: for a toolset appliance, the
allowed set is the record's bindings rather than the compile-time map. The
compile-time list stays exactly as it is for every other type — this is a
carve-out for one type, not a relaxation for servitor.

Resolution per run:

1. read the bindings,
2. load each persisted tool, in the OWNER's context (matching how a shared
   appliance resolves everywhere else),
3. drop any whose `BodyHash` no longer matches, and name them in the session so
   the owner sees why their investigation got quieter,
4. hand the rest to investigator and workers with `NeedsConfirm` set from
   `Posture`.

A `deny` posture is deliberately absent from the enum: an unwanted tool is
unbound, not bound-and-refused. A tool the model can see and cannot use produces
a probe that plans around it and reports being blocked.

### Slice 3 — generated prompts

The four prompts come from the same structural templates every type uses, filled
from `Domain` and the bound tool list.

The tool descriptions carry most of the domain knowledge already — a well
written `gitlab_list_merge_requests` says what a merge request is by saying what
the tool returns. What `Domain` adds is the three things a tool list cannot say:
what the target IS as a whole, what a good answer looks like for it, and **what
"absent" means here** — the distinction between "the project has no such
pipeline" and "no bound tool can see pipelines," which is this type's version of
the discipline every other servitor type has.

### Slice 4 — snapshot from a toolset

The genuinely open problem. Every other type has a cheap, no-LLM orientation
pass; a toolset has no equivalent by construction, because servitor does not
know which tool is the cheap overview.

Proposal, in preference order:

- **Nominate one.** A binding may be marked `snapshot: true`, and its zero-arg
  result becomes the orientation pass. One flag, owner-set, exact.
- **Fall back to no snapshot.** The investigator opens by calling tools to
  orient itself, which costs a round and works. Better than guessing: running
  every zero-required-argument tool would fire writes on any toolset that
  contains one, and "read tools only" is a convention nothing enforces.

### Slice 5 — the Map run

Mapping a toolset means: enumerate what the tools can see, record it as the five
knowledge docs, build the entity graph. Structurally identical to mapping a
repo; only the probes differ. No new machinery, and it is what makes the second
question about a target cheaper than the first — the whole point of putting this
inside servitor.

## Sequencing

Slices 1 + 2 together — the type is not safe to run without body-pinned
resolution, and neither half is useful alone.

Slice 3 next; until then the type can borrow the repo prompts, which are close
enough in shape to prove the plumbing while producing mediocre investigations.

Slice 4 after there is a real toolset to try it against. Slice 5 last.

Build GitLab as the proving ground before anything else adopts the type. The
part that cannot be verified from the code is whether a generated investigator
prompt produces good probes — and a bad one fails as vague findings rather than
as an error, which is the failure mode that hides.

## Open decisions

- **Who binds a tool?** Owner-only is the simple answer. The alternative mirrors
  `request_capability`: an investigator that hits a wall proposes a binding and
  the owner approves it, which is the same round trip that already works for
  minted commands. Proposal: owner-only for the first cut, since the tools
  already exist and binding is a two-click act, not an authoring one.
- **Does a toolset appliance get `run_command` at all?** Proposal: no. The
  narrowness is the point, and a toolset with a shell in it is an SSH appliance
  with extra steps.
- **Is `Domain` written or generated?** Proposal: written by the owner, with a
  generated first draft from the bound tools' descriptions. It is three
  sentences, and the owner is the only party who knows what a good answer looks
  like for their target.
- **What happens to an in-flight investigation when a bound tool's body
  changes?** Proposal: the running session keeps the tools it resolved at start
  — swapping implementations mid-investigation is worse than finishing under the
  set that was approved when it began — and the next run picks up the mismatch.
