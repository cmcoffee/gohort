# Knowledge: checking a page before it goes in

Status: built (2026-11-15), with Settle it for held pages. Real-model behaviour not yet seen live.

## Why

A collection is what agents read as fact, and Auto-Curation adds to it
unattended, on a schedule. Today nothing checks what an added page says. The
curator is told to read a page and prefer primary sources, and Auto-fill can
ask a judge whether a page fits, but no step asks whether a page agrees with
what the collection already holds. A page that says a setting defaults to on,
added beside the vendor's documentation that says off, goes in silently, and
the next agent that searches gets whichever chunk ranks higher.

This adds one check, in front of every way a machine adds to a collection,
that asks two things: does the page fit, and does it conflict with what is
already there. A conflicting page is not added. It waits for the owner, with
what it conflicts with.

## What exists (verified in code)

| Path | Who picks the page | What checks it now |
|---|---|---|
| `collections` tool `add_url` (`collections_tool.go:349`), also the steward tool's `add_url` | an agent: the curator, or an owner's agent | nothing; fetched, extracted, ingested |
| `add_text`, `add_file` (same tool) | an agent | nothing |
| Auto-fill, and Fill it for me's run (`autofillPass`, `collections.go`) | Auto-fill's searches | `judgeAutofillCandidate`, only when the collection's `ClassifyOnAutofill` is ticked (off by default); judges fit and quality, may return cleaned text; on a judge failure the page is ingested anyway |
| Articles to fetch, Find sources ticks, Fetch on a suggestion, upload, paste | the owner | nothing, and none needed: the owner chose it |

- **Retrieval:** `SearchCollections(ctx, db, user, ids, q, k)` is the
  retrieval agents use (hybrid, per-document diversified, relevance floor).
- **Owner's queue:** `collection_suggestions.go` keeps a durable queue of
  suggestions (`collection_suggestions` table, at most 50 per collection),
  shown on the collection's page with Fetch and Dismiss, and worked by the
  curator through its `suggestions` / `resolve_suggestion` actions.
- **Judgement shape:** `core/decide.Verdict` is the shape for a new
  judgement: reasons before the answer, and the System One form.

## The design

### 1. One check, for every way a machine adds

`vetPage(ctx, llm, c, user, candidate) vetResult` in
`apps/orchestrate/collection_vet.go`, called from:

- the scoped `collection` tool of the agent in charge (the curator, or the
  agent picked for Curation by Agent): its `add_url` and `add_text`;
- `autofillPass`, in place of `judgeAutofillCandidate`.

Owner actions (the last row above) do not go through it: the owner picking a
page is the decision the check would otherwise hold for.

When it runs:

- **The agent in charge:** always. Nobody is watching those adds.
- **Auto-fill and Fill it for me:** always on a curated collection; on a
  Hand Curated one when the existing "check pages" box is ticked
  (`ClassifyOnAutofill`, no new field).
- **Not checked:** Builder's general `collections` tool, the only other way an
  agent adds. It is offered to Builder alone, which acts on the user's own
  instruction, so it is treated like the owner picking. An agent that only
  reads a collection cannot add at all; it files a suggestion.
- **`add_file`** on the scoped tool is not checked: the curator has no tool
  that saves files, so nothing reaches it that way today.

When the check cannot run (model error, timeout), the page is **not added**,
and the result says it could not be checked. This changes today's
behaviour: a failed judge used to mean ingested unchecked. The change only
covers what a machine picked, so the cost of failing closed is a page added
on the next run instead.

### 2. Check against the collection, not just for fit

One model call per candidate, written as a `decide.Verdict`:

- **The prompt gets:** the collection's name, description and rules; the
  candidate (address, title, the first ~4,000 characters of its text,
  fenced as untrusted); and **what the collection already says about it**,
  the top 6 chunks from `SearchCollections` queried with the candidate's
  title and opening text, each cut to 600 characters, with its document's
  title. An empty collection skips that part.
- **Weigh steps,** written before the answer:
  1. `fit`: does it fit what the collection is for and its rules.
  2. `against`: for each excerpt about the same thing, does the page state
     something different (a value, version, date, default, rule or
     conclusion). Quote both sides and name the document.
  3. `why`: the reason, one line.
- **Answers:**

| Answer | Means | What happens |
|---|---|---|
| `adds` | fits, and says something the collection does not, without contradicting it | added |
| `supersedes` | a newer statement of the same thing from the same or a more authoritative source (a new version of the same documentation) | added, and the old document is named as a proposed removal, the way the curator already proposes them |
| `conflicts` | contradicts a document already there, and is not plainly its newer version | held for the owner (3) |
| `drop` | does not fit, low quality, or adds nothing new | not added, with the reason |

Two readings keep the answer honest: a `conflicts` when the collection held
nothing on the page (no excerpts) is read as `adds`, since there was nothing
to contradict; a `supersedes` that names no excerpt is read as `adds`, so
nothing is proposed for removal on a guess.

`supersedes` is there for the scheduled refresh, which goes looking for new
versions. Without it every update would be held as a conflict. If the check
mistakes a conflict for a newer version, the page goes in, but the owner
sees the old document named as a proposed removal.

**Text cleaning:** the current judge's `cleaned_text` (the model passing back
the page minus clutter) is dropped. A model retyping a page can change
what it says, which is what this check exists to prevent; the HTML filter
stays.

The judgement gets a route stage (`app.orchestrate.collection.vet`, worker by
default), and check cases in `core/checktest`, so it can be mapped to the
Offload model or System One like the other judgements once it scores.

### 3. Hold conflicts for the owner

A held page goes into the existing suggestions queue as a row, with one new
field:

```go
Kind string `json:"kind,omitempty"` // "held": a page the check held for the owner
```

- **Text:** "Held: this page conflicts with <document>. It says <its
  side>; <document> says <the other>." (from the `against` step).
- **Source:** the page's address (for `add_text`, what the agent gave as its
  source). From: "Collection check".
- **The owner** sees it in the collection's Suggestions with a "Held" mark.
  Fetch adds it anyway (an owner action, so unchecked) and Dismiss drops it.
  The page copy that says suggestions go to the agent in charge gains a line:
  held pages wait for you.
- **The curator does not see held rows:** `suggestions` leaves them out.
  `add_url` of an address that is held answers "held for the owner, not
  added" without asking the model again, so a curator cannot re-add a held
  page by trying twice.
- **What gets reported:**
  - the tool result tells the agent the page was held and why, and to say so
    in its reply, which is the run's outcome on the page and a refresh's
    last note;
  - Auto-fill lists it with status `held` and counts it ("2 held for you
    below"); the Fill run's outcome says how many were held.
- **Text that contradicts** (the curator's `add_text`) is turned away, not
  held: there is no page to add later. The tool result tells the curator to
  say so.
- **When the queue is full** (50), the page is not added and not held; the
  result says the queue is full.

**Stored change:** new optional fields on suggestion rows (`kind`, `title`,
`against_doc`, `against_title`, and the ruling's `ruling`, `ruling_why`,
`ruling_at`). Rows already stored have none of them and stay ordinary
suggestions. No migration.

### Settling a held page

Each held page is a row in a "Held for you" section on the collection's page
(`apps/knowledge/held.go`), with what it contradicts, and four ways to deal
with it:

| Button | Does |
|---|---|
| Add it | adds the page beside the document (the owner's pick, so unchecked) |
| Replace the old one | adds the page and removes the document it contradicts |
| Keep the old one | drops the page |
| Settle it | argues it out first, and shows a ruling; changes nothing |

Settle it is a pipeline on core's own stages, run in the background with a
spinner, progress, Stop and rejoin (the curation run registry):

1. **The two sides.** Each is given the point in dispute and the part of its
   own text that bears on it: the page is fetched again and its paragraphs
   sharing the most words with the conflict are kept; the document's part
   comes from the collection's retrieval, or the whole document when the
   search ranks it out.
2. **argue:** a panel of two voices, "The new page" and "The collection's
   document", over two rounds, each round reading what the other said. When
   the deployment can search, each voice looks things up before it speaks
   (two pages a round) and cites them by number.
3. **check:** the pipeline's verify stage puts each cited claim to the model
   against the page it cites, and lists citations naming nothing read and
   figures found in none of the pages.
4. **rule:** a `decide.Verdict` given the dispute, both arguments, the
   citation check and the pages read. Answers: `page` (replace the
   document), `document` (keep it), `both` (different versions or cases:
   add it beside), `unsettled`. A claim whose citation did not hold counts
   for nothing.

The ruling is saved on the row and shown in its Ruling column and under
Settle it. The owner then presses the button it points to.

## Cost

- **Model calls:** one per candidate page that reaches the check (after the
  existing filters and the already-ingested check), at about 2,500 tokens in.
  A curator run is capped at the Auto-fill document limit, so a run costs at
  most that many calls more than today.
- **Time:** a search of the collection and one call per page, inside the
  run's existing time limit; the run's progress line counts pages checked.
- **Settle it,** only when pressed: four argument calls (two voices, two
  rounds), with search four calls choosing what to look up and up to eight
  pages read, one call per cited claim in the citation check, and the
  ruling. Within the run time limit; Stop ends it.

## Settled (owner, 2026-11-15)

1. **Agent adds:** checked. In practice that is the agent in charge; see
   "Not checked" above for Builder.
2. **Text cleaning:** dropped.
3. **When the check cannot run:** the page is not added.

Check cases are registered (`app.orchestrate.collection.vet`, six cases) so
the check can be scored on the judge page. It is not marked offloadable yet:
that needs its System One form and scores first.

## Tests

- **Every add path:** each one calls the check exactly when the design says,
  and owner actions never do.
- **Each answer:** each does what the table says (a scripted model).
- **Failure:** a check that fails adds nothing.
- **Held pages:** held rows are invisible to the curator; a held address
  cannot be added again by an agent; Fetch adds it.
- **Old rows:** rows stored before the field read as ordinary suggestions.
- **Mutants:** on the gates.
- **Real model:** a live pass against an owner collection, with a page known to conflict.

## Out of scope

- A second, independent source for claims that rest on one non-primary
  page.
- Deep research, which would need a hook the private research app plugs into.
- Research reports saved into a collection (`collections.go:649`). They come
  with their own citations and are not checked here.

## Second source (2026-11-15)

Status: built (2026-11-15), `apps/orchestrate/collection_second_source.go`.
Real-model behaviour not yet seen live.

### Why

The check above catches a page that contradicts the collection. It does not
catch a page that says something new and wrong: a blog post, forum answer or
news write-up with a claim nothing in the collection covers goes in as fact.
The curator is told to prefer primary sources, but nothing holds it to that.

### The rule

A page a machine picked, from a **secondary** source, goes in only when an
**independent** source says the same thing. A primary source goes in on its
own word, as now.

- **Primary:** the thing's own word: the project's or vendor's documentation,
  the standard, the law, the paper, the official announcement.
- **Secondary:** an article, blog post, forum answer, news story or summary
  about it. The curator's own text (`add_text`) counts as secondary: it is an
  agent's conclusion, which is exactly what should not become fact unchecked.
- **Independent:** another site, saying it on its own rather than quoting or
  linking the page.

### How

1. **The check says which.** Two more steps in the existing check's verdict,
   so no extra call: `source` (primary or secondary, one clause why) and
   `claim` (what the page adds that the collection does not say, in one
   sentence). Only a page answered `adds` or `supersedes` and marked
   secondary goes on to step 2.
2. **Look for a second source.** A pipeline on core's stages, as Settle it
   is: a **gather** stage searches for the claim and reads the best three
   pages (it already skips weak sources and takes at most two pages a site),
   then a judge stage (a `decide.Verdict`) is given the claim, the page's
   address, and the numbered pages read. Answers:

| Answer | Means | What happens |
|---|---|---|
| `confirmed` | an independent page says the same | added; the result names that page |
| `contradicted` | an independent page says otherwise | held, with what it says and its address |
| `unconfirmed` | none of the pages read says it | held, as unconfirmed |

   A page on the same site as the candidate, or one that only quotes it,
   does not confirm it.
3. **When it cannot run** (no search set up, search or reading fails): not
   added, the same as the check failing.

### Held pages

The held row says why it was held, in one new optional field on the row,
`held_for`: `conflict` (as now), `contradicted` or `unconfirmed`. The page's
buttons follow from it:

| Held for | Buttons |
|---|---|
| conflict | Settle it, Add it, Replace the old one, Keep the old one (as now) |
| contradicted | Settle it (against the outside page), Add it, Leave it out |
| unconfirmed | Add it, Leave it out |

Settle it against an outside page: the second side is the outside page,
fetched again, instead of a document in the collection. Everything else in
the settle pipeline is unchanged; a `page` ruling means add it, `document`
means leave it out.

Rows held before this have no `held_for` and read as `conflict`. No
migration.

### Cost

Only for secondary pages that pass the check: one or two searches, up to
three pages read, one model call. The run's progress line says
"confirming N of M".

### Settled (owner, 2026-11-15)

1. **Unconfirmed:** held for you.
2. **Contradicted by an outside page:** held, with Settle it against that page.
3. **The curator's own text:** needs a second source; without one it is
   turned away (there is no page to hold), and the curator is told why.

The check's verdict reads only an answer starting "primary" as primary: a
reply that leaves the step out gets the stricter reading. The same site is
never a second source, whatever the ruling says, and a ruling that names no
page it read counts as unconfirmed. Check cases are registered
(`app.orchestrate.collection.second_source`, five cases).
