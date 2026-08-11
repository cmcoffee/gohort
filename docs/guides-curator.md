# The Guide Curator — one agent decides what a guide says

Status: **slices 1, 2 and 4 built** (v0.6.018). An editorial agent that owns the
guide corpus. Producers stop choosing a destination and stop writing section
titles; they report findings. The curator decides what is worth keeping, where
it belongs, what it replaces, and what to throw away.

Two decisions are settled and are load-bearing for the rest of the design: the
curator **writes on its own authority** and reports afterwards in a digest, and
it **may create guides**. Both are expanded in "Authority" below.

What shipped, and where it differs from the design below:

- `core/docs/findings.go` — the `FindingTarget` seam, an optional interface on
  a `DocumentTarget`, mirroring `ReferencingDocumentTarget`.
  `apps/guides/findings.go` — the inbox and the digest.
  `apps/guides/curator.go` — the agent and its decision kit.
  `apps/guides/curator_schedule.go` — threshold + interval firing.
  `apps/guides/curator_web.go` / `curator_ui.go` — the digest surface.
- **`push_to_guide` was NOT removed.** Slice 5 retires the direct paths and is
  out of scope; servitor gained `record_finding` alongside it. The old tool
  stays for the case it is actually right for — the user named a destination in
  their request.
- **`supersede` refuses a single-observation finding outright**, rather than
  discouraging it in the prompt. A rule that consequential should not depend on
  the model having read the prompt.
- **A `hold` deliberately does not drain the finding.** Every other outcome
  removes it from the queue; a hold is a deferral, so it stays for a later batch
  with more context or a guide that now exists.
- **Findings the curator never decided are re-queued AND recorded as holds** it
  did not write. Silently re-queueing would make a curator that ignores half its
  batch indistinguishable from one with nothing to do.
- **The run restores the user's open-guide marker.** The Author's incorporate
  path moves it as a side effect, which is fine when a person pushed a section
  and disorienting when a background batch reopens three guides.
- **Per-user run locking**, because threshold firing and the interval tick
  landing together is not hypothetical — it is what happens when a burst of
  findings arrives near an interval boundary, and both runs would file the same
  findings.
- Slice 3's per-section provenance ledger is **not** built. Supersede works and
  records the replaced text in the digest, which is enough to review and undo
  one; what is missing is the durable per-claim history that makes staleness
  computable. Sections written before it exists will not get it retroactively.

## The idea

Today a servitor worker, mid-investigation, decides:

- that a finding is worth documenting at all,
- which of the user's guides it goes in,
- what the section should be called.

Those are three editorial judgments, made by an agent whose actual job is
answering one probe, and whose success condition is "I found something." Nothing
in that loop is asking whether the Ops runbook wants this. The predictable result
is a guide that grows into an append log of whatever each investigation happened
to notice.

The fix is to give the corpus a single writer with editorial authority. Sources
emit findings; the curator decides. That is also the only way the questions that
matter become answerable at all — *does this duplicate something already in
here, does it contradict it, does it supersede it* — because those questions need
a view of the whole corpus and of the other findings in the batch, and a
per-push producer has neither.

## What already exists

More than it first appears. A middleman is already in the path — it just has no
authority.

**The cross-app write seam.** `core/docs/document_writer.go` is a
`DocumentTarget` registry: a writer app registers under a stable kind and other
apps push sections into it without importing it. Guides registers as kind
`"guide"` (`apps/guides/push_target.go:17`). The producer never sees the
target's database.

**The read seam, in the other direction.** `core/sources` is the mirror:
servitor exposes each appliance as a `ReferenceSource` of kind `"system"`
(`apps/servitor/reference_source.go:22`), a guide attaches selections
(`Guide.References`, `apps/guides/data.go:46`), and the Guide Author pulls them
with `pull_reference` (`apps/guides/coauthor.go:452`).

**The Guide Author agent** (`app-guides-author`) with real co-author tools that
write into the open guide's section list: `list_sections`, `add_section`,
`edit_section`, `draft_section` (`apps/guides/coauthor.go`).

**And a partial middleman already.** `guideTarget.Append` does NOT blind-append
into an existing guide — it hands the content to the Guide Author via
`runIncorporate` (`apps/guides/coauthor.go:810`), whose prompt says to merge into
an existing section or add a fitting one, in the guide's voice, without
duplicating.

So the piece people usually build first is built. What it lacks is authority: by
the time `runIncorporate` is reached, the producer has already decided the guide
and the title, and the only remaining question is how to phrase the insertion.
The agent is a formatter, not an editor.

## What's missing

1. **Editorial judgment sits with the producer.** `push_to_guide`
   (`apps/servitor/web.go:3802`) *requires* `guide` and `section_title`. The
   servitor worker picks both, mid-probe.
2. **The new-guide path skips the agent entirely.** With an empty `docID`,
   `Append` creates the guide and drops the content in as its first section, no
   author involved (`apps/guides/push_target.go:76`).
3. **One synchronous agent run per push, each with `FreshSession`.** A burst of
   findings is a burst of full agent runs, none of which can see the others. Any
   dedup *within* a batch is structurally impossible.
4. **No provenance for a claim.** `Guide.References` records the sources a *user*
   attached to a guide. Nothing records that this paragraph came from that
   investigation of that appliance at that time. So "where did this come from"
   and "is this still true" cannot be answered, and neither can be automated.
5. **No rejection path.** `Append` either writes or errors. There is no way for
   the receiving side to conclude "this is noise" or "this belongs nowhere."
6. **No supersede or contradict.** `runIncorporate` is told not to duplicate, but
   it has no record of what an earlier push asserted, so a finding that reverses
   a previous one reads as a second opinion sitting next to the first.
7. **Every producer grows its own push tool.** Servitor has one. Techwriter and
   codewriter have `save_to_*` with no guide path. Each new source is another
   copy of the same three editorial decisions.

## The build

### Slice 1 — findings, not destinations

A new core seam beside `DocumentTarget`: producers submit a **finding** and name
no destination.

```go
type Finding struct {
    Content    string   // what was learned, in markdown
    Topic      string   // the producer's one-line framing, NOT a section title
    Provenance Origin   // source kind, item id, run id, observed-at
    Confidence string   // verified | probable | single-observation
}
```

`Topic` is deliberately not `SectionTitle`. A producer naming a section is
already choosing a shape for the document; naming a topic is reporting what the
finding is about, which is the thing it actually knows.

`push_to_guide` loses its `guide` and `section_title` parameters and becomes a
finding submission. The tool's description changes from "add this to a guide" to
"report this so it can be documented" — which is also a more honest description
of what a probe worker is in a position to assert.

### Slice 2 — the curator

A sibling of the Guide Author (`app-guides-curator`), invoked over a **batch** of
findings, holding the whole corpus:

| Tool | What it decides |
|---|---|
| `list_guides` / `list_sections` / `read_section` | what the corpus already says |
| `place_finding(finding_id, guide_id \| new_guide, section?)` | where it goes, if anywhere |
| `supersede(section_id, finding_id)` | this replaces what was there |
| `flag_contradiction(section_id, finding_id, note)` | these disagree; a human should look |
| `discard(finding_id, reason)` | not worth documenting |

Placement delegates the actual prose to the existing `runIncorporate` path, which
already knows how to weave into a section in the guide's voice. The curator
decides; the author writes. That split keeps both prompts small and stops the
curator from becoming a second, competing writer.

`discard` is a first-class outcome and must be recorded with its reason, not
dropped. A curator that silently discards is indistinguishable from one that is
broken, and the discard log is the only evidence available for tuning it.

### Slice 3 — the provenance ledger

Per section, the findings that produced it: finding id, source kind + item,
run id, observed-at, and whether it was superseded.

This is what makes the rest of the design work rather than being bookkeeping for
its own sake:

- `supersede` needs something to point at.
- "is this stale" becomes computable — a section whose newest provenance entry is
  four months old, about a system re-mapped last week, is a candidate for
  re-verification. Servitor already has exactly this concept for its own
  knowledge docs (`docStaleAfter`, `apps/servitor/knowledge.go:14`).
- A reader can ask where a claim came from, which is the difference between a
  guide people trust and a guide people spot-check.

### Authority: it writes, and it reports

The curator commits its decisions without asking. Guides already save every
change as a revision, so nothing it does is unrecoverable, and the alternative —
a review queue — is worse in the specific way that matters: a queue nobody drains
is a slower discard, and it puts a human back in exactly the position this design
is trying to relieve them of. Approval before every placement would also make the
curator strictly more work than the current per-push write.

What replaces approval is the **digest**: one record per curator run, saying what
it did and why, in enough detail to disagree with.

```go
type CuratorRun struct {
    ID        string
    Started   string
    Findings  int
    Placed    []Placement    // finding → guide, section, merged-or-added
    Superseded []Supersede   // finding → the section text it replaced
    Contradictions []Flag    // finding vs section, and the curator's note
    Discarded []Discard      // finding + reason
    Created   []NewGuide     // guide id, title, and the findings that justified it
    Held      []Held         // finding + why it fit nowhere
}
```

Three properties this needs to have, or auto-write stops being safe:

- **It is surfaced, not merely stored.** A digest written to a table nobody opens
  is the same as no digest. It lands in the Guides app as a reviewable run list,
  and a run that created a guide or flagged a contradiction also notifies.
- **Superseded text is kept in the digest, not just in the revision history.**
  "What did this replace" is the question a reader of the digest is actually
  asking, and making them diff two revisions to find out is how the digest stops
  getting read.
- **Every outcome appears, including the ones that did nothing.** Discards and
  holds are the entries that reveal a miscalibrated curator; a digest listing
  only successful placements looks identical whether it is working well or
  throwing away everything hard.

Each run's decisions are reversible from the digest itself — a per-entry undo
that reverts to the revision before that placement — so disagreeing with the
curator costs one click rather than an edit.

### Authority: it may create guides

Creating a guide is a larger claim than placing a section: it asserts that a
topic deserves its own document. The curator may make that claim, bounded:

- only from a batch containing **several findings on one topic** that fit no
  existing guide — one orphan finding is a `Held`, not a new document;
- the creation is named prominently in the digest, with the findings that
  justified it, so a wrong one is visible immediately rather than discovered
  months later as a stub nobody wrote;
- a newly created guide starts with the sections those findings support and
  nothing else. The curator does not outline a document it has no material for —
  an empty scaffold of headings reads as a guide that exists and is unfinished,
  which is worse than one that never got created.

The bound matters because the failure mode is asymmetric. A missed guide is a
`Held` finding somebody notices; a spurious guide is a document in the user's
corpus that looks authoritative and is nearly empty.

### Slice 4 — batching

The curator runs over accumulated findings, not per submission: on a threshold
(N pending) or an interval, whichever comes first, through the existing trigger
and scheduler machinery rather than a new timer.

Batching is not an optimization here, it is what makes the editorial judgments
possible. Three findings from three probes about the same service can only be
merged into one section by something that sees all three.

### Slice 5 — retiring the direct paths

- `push_to_guide`'s destination arguments go.
- The blind new-guide create goes; a new guide becomes a curator decision.
- The UI ↗ Guide button stays direct. A **user** choosing a guide is a
  deliberate editorial act and should not be second-guessed by an agent; the
  curator exists to supply the judgment an automated producer lacks, not to
  overrule a person.

## Sequencing

Slices 1 + 2 + 4 are the minimum that changes anything: findings go to an inbox,
a curator drains it on a schedule, placement reuses the author that already
exists.

Slice 3 can trail by one commit, but not more — `supersede` is unimplementable
without it, and retrofitting provenance onto sections written without it means
those sections never get it.

Slice 5 last, once the curator has been observed making decisions on real
findings.

## Open decisions

Settled (see "Authority" above): the curator auto-writes and reports in a
digest, and it may create guides under the stated bound.

Still open:

- **What happens to a finding it cannot place?** Proposal: held with a reason
  rather than discarded, so a topic that keeps arriving and keeps not fitting
  becomes visible as a gap in the corpus rather than as silence.
- **Per-user, or per-guide-owner?** Guides can be shared for edit, so findings
  about a shared guide may arrive from a user who is not its owner. The curator
  should run in the OWNER's context on the owner's store, matching how servitor
  resolves a shared appliance.
- **Confidence handling.** Whether a single-observation finding is placed at all,
  or held until corroborated, is a policy the curator prompt can hold — but it
  needs the field to exist from slice 1 to have the option later.
