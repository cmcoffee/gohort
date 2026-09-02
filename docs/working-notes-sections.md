# Sections in working notes

An agent minting its own memory registers, without a new memory layer.

## The question this answers

> Does it make sense to let an LLM make its own memory registers, or does it
> already have that ability?

Half of it exists. What is missing is smaller than a layer, and the reason to
keep it small is the same reason the cap exists.

## What an agent can already define for itself

| Layer | Tool | Self-defined structure? |
|---|---|---|
| Rules | — (user-authored) | No. Human-owned policy, top of prompt. |
| Explicit Memory | `store_fact` | No. One flat list per `agent:<id>` namespace. |
| Working notes | `update_notes` | **One document, any internal shape.** |
| Reference Memory | `memory_save` | No. Free text, retrieved by similarity. |
| Graph memory | `link_entities` | **Fully open.** Entity, relation and attribute names are the model's to invent. |
| Knowledge | collections | No. Admin-curated corpora. |

So the model already invents structure in two places: any shape it likes inside
its notes document, and any vocabulary it likes in the graph.

## The gap

An agent cannot mint a NAMED, ALWAYS-IN-PROMPT register — a slot it addresses
by name and updates on its own.

The nearest thing is a heading inside its working notes, which it can already
write. What it cannot do is update that heading WITHOUT REWRITING EVERYTHING
ELSE, because `update_notes` replaces the whole block:

> "This REPLACES the whole block; it does NOT append. Re-state the complete
> current note each time."

## Why not a new layer

**A register is prompt weight.** Letting the agent mint registers is letting the
agent set its own prompt budget. Every context bug in this codebase has been an
unbudgeted growth nobody could see from the settings that looked relevant — the
tail counted in messages while the window counted tokens, then in a share of a
window that could be a million. A memory layer whose size is decided by the
thing being measured is that mistake with a shorter fuse.

**Six layers is already the limit of what fits in a tool description.** Every
memory tool carries a paragraph explaining when to reach for it instead of the
other five. A seventh whose members the agent invents makes that paragraph
unwritable, and an agent that cannot choose between layers writes to the wrong
one.

**The cap IS the feature.** From the store: *"it forces the agent to COMPRESS
its running state on every rewrite rather than hoard, keeping the per-turn
prompt cost fixed."* Sections must compete inside that budget, never add to it.

## The model

> A SECTION is a register the agent names. It has no separate storage, no
> separate cap, and no separate prompt block.

`OperatingNotes.Text` remains one document, one row, one cap of 1500 runes.
Sections are markdown `## ` headings inside it, and the tool learns to address
one.

```
## deployment quirks
llama slots are reassigned by LRU; a stable prefix does not mean a warm cache.

## in flight
Drafting section 3 of the onboarding guide. User wants the terse version.
```

Everything downstream is unchanged: `RenderOperatingNotesBlock` renders the same
text, the history ring keeps the same prior versions, the Memory modal edits the
same textarea, and an agent that never passes a section keeps today's behaviour
exactly.

## The tool

`update_notes` gains one optional parameter.

```
update_notes(text: "...")                      → replaces the whole block (today)
update_notes(section: "in flight", text: "…")  → replaces that section only
update_notes(section: "in flight", text: "")   → removes that section
```

Rules, in the order they matter:

1. **A section names itself.** No registry, no pre-declaration, no admin step. A
   section that does not exist is created; one written empty is removed.
2. **Section names are normalized** for matching (trimmed, case-folded) and
   stored as written. `In Flight` and `in flight` are one register — otherwise a
   model that varies its capitalization keeps two copies of its own state and
   they disagree.
3. **A sectionless write still replaces everything**, including sections. That
   is what "rewrite the whole block" has always meant, and an agent correcting a
   mess needs a way to say so.
4. **Untitled text stays untitled.** A document with a preamble above the first
   heading keeps it; a section write never disturbs it.
5. **Order is stable.** An updated section stays where it is; a new one is
   appended. A block that reshuffles itself each turn is a block nobody can read
   diffs of, and it breaks prompt-prefix caching for no reason.

## When it will not fit

The cap is on the DOCUMENT. A section write that would push the total past 1500
runes is **refused, not truncated**, and the refusal says what is in the way:

```
that would put your notes at 1,740 of 1,500 characters. Largest sections:
"deployment quirks" (620), "in flight" (410). Trim one and retry.
```

A memory layer that quietly loses its middle is worse than one that says no —
the agent cannot tell it was truncated, and the note it reads back next turn is
a sentence that stops. Naming the largest sections turns the refusal into an
instruction: the model has the block in front of it and can compress the part
that is actually costing.

The same refusal shape applies at the HTTP surface, which already returns 400
with the count.

## Storage and migration

**None.** `OperatingNotes.Text` is one string before and after; sections are a
convention parsed on write. Any existing note is a valid document with one
untitled section, and the first sectioned write leaves it in place.

This is the point of doing it this way. A registers table would need a schema, a
migration, a cap policy per register, a UI, an audit surface and a retention
rule; the same behaviour lands here as a parse and a splice.

## The parser

`core/notes` gains two unexported functions and one export:

- `splitSections(text) []section` — `{name, body}`, first entry unnamed when the
  document opens with prose.
- `joinSections([]section) string` — round-trips, and `join(split(x)) == x` for
  any document (the property worth a test: a parser that reformats other
  people's prose is a parser that loses it).
- `ApplyNoteSection(text, section, body) (string, error)` — the splice, exported
  because `update_notes` and the HTTP handler both need it and a second
  implementation is how they come to disagree.

Only `##` counts as a section heading. `#` is a document title, `###` is
structure inside a section, and a model writing `### notes` under a `## ` should
not find it silently promoted to a register.

## The prompt

`RenderOperatingNotesBlock` gains one line to its framing, and only one:

> Update a single section with `update_notes(section: "<name>", text: "...")`
> when only that part changed; rewrite the whole block when the shape of the
> work changes.

The existing framing — advisory, revise-don't-append, record the goal never the
tool call — is unchanged and still governs. The tool description gains the
parameter and the same one-line rule. Nothing else in the memory doctrine moves,
which is the test of whether this is a feature or a layer: a layer would need a
paragraph.

## The panel

The Memory modal's Working notes textarea already edits the raw document, so it
works on day one with no change — sections are visible as headings because they
ARE headings.

Worth adding after, not before: the character counter showing the largest
section beside the total, so an owner looking at a full block can see which
register is eating it. Same information the refusal gives the agent.

## Staging

*(Steps 1 and 2 built, v0.6.552.)*

1. **The parser.** `splitSections` / `joinSections` / `ApplyNoteSection` in
   `core/notes`, with the round-trip property test and the cases that break naive
   splitting: a `##` inside a fenced code block, a heading with trailing
   whitespace, CRLF, an empty document, a document that is one heading with no
   body.
2. **The tool.** `section` parameter on `update_notes`, the over-cap refusal
   naming the largest sections, and the reply telling the agent which section it
   wrote. Tests: create, update in place, remove, order stability, sectionless
   write still replaces everything, case-folded match.
3. **The HTTP surface.** `POST …/notes` accepts an optional `section` with the
   same semantics, so the owner-facing path and the agent-facing path cannot
   drift.
4. **The framing line** in `RenderOperatingNotesBlock` and the tool description.
5. **The counter** in the Memory modal.

Steps 1 and 2 are the feature. 3 through 5 are what stop it becoming a thing
only the model knows about.

One thing the build added that the scope did not name: a section write splices
into the RESOLVED notes, seed included. An agent configured with SeedNotes whose
store is still empty would otherwise have its first section write silently
discard the notes it was set up with — the seed renders in the prompt but is not
stored, so a splice against storage alone starts from blank.

## Explicitly out of scope

- **Per-section caps.** One budget, and sections compete inside it. Per-section
  caps are how a document grows to the sum of its parts.
- **A section registry, or admin-declared sections.** The agent names its own;
  that is the whole request.
- **Section-scoped history.** The ring keeps whole prior documents, which is
  what a revert needs.
- **Sections anywhere else.** Facts, graph and Reference Memory are unchanged.
  If sections earn their place here, that is an argument to revisit — after
  living with them, not before.
