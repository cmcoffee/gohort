# Knowledge: a guided start, and auto-curation

Status: steps 1 to 4 built (2026-11-14); step 5 proposed.

## Why

A new collection was two fields: a name, and a description (with Draft with
AI to word it). After that the person landed on the collection's page, where
the ways to fill it sit side by side: Auto-fill, Research, upload, and a
"Maintained by" agent. Every piece was there; nothing asked what the
collection is for, and nothing helped choose between them. Someone who
wants "a reference on our deployment tooling" had to know that Research
writes cited reports, that Auto-fill saves raw pages from searches it makes
up from the description, and that neither is the right call for their own
contracts.

This makes "+ New collection" a short guided setup that starts from what
the collection is for, and lets Knowledge look after a collection itself.

## What existed (verified in code)

| Piece | Where |
|---|---|
| New collection | `apps/knowledge/new_collection.go`: was name and description, Draft with AI |
| Auto-fill | `apps/orchestrate/collections.go` `collectionAutofill`: searches written from the description, raw pages saved, capped by the "Ingest & autofill" tunables |
| Articles to fetch | `apps/orchestrate/collection_articles.go`: a list of links or descriptions the collection must hold, fetched and marked as in |
| Research | `collections.go` `collectionResearch`: the hidden `seed-research` agent writes a cited report per topic, each saved as a document |
| Upload | the collection page: PDF, Word, Markdown, text, web pages |
| Who looks after it | `Collection.CuratorAgent` (core): that agent gets a tool scoped to this collection only (add a page, a file or text; list; remove) |
| Suggestions | agents reading the collection can suggest additions, queued for whoever decides |
| An app's own agent | `appagents.RegisterAppAgent`, as Scribe's Guide Author: hidden from pickers and from every outside caller when marked Hidden |

## The guided "+ New collection" (built)

A wizard in the page's dialog, its Back, Close and Next or Create in the
dialog's footer. What it is for and the name are required; everything else
can be skipped.

1. **Purpose.** One box: what the collection is for, in their own words.
   The products, standards, sites or kinds of document it is about, and how
   they will use it.
2. **Name.** A name and a description, drafted from what they said as the
   step opens (`/orchestrate/api/collection-suggest`, on `core/assist`):
   names offered as a list to pick from, with Suggest for another set; a
   description written to name the concrete things searches should find,
   with Suggest opening the assistant to work on it. Both editable.
3. **Details**, each optional: how current it has to be; a solid overview
   or comprehensive; mostly the web, mostly their own files, or both;
   sources to prefer; sources to leave out.
4. **Curation.** How it is kept current, as cards (below), and **To start
   with**, shown when web search is set up: Suggest sources (also what
   happens when nothing is chosen), Draft a plan to fill it, or Nothing yet.
   What is chosen starts as the collection is created, so the page it opens
   on is already at it, and neither adds anything until the person says.
5. **Agents**, when they have any: which of their agents read it, attached
   as an agent's Knowledge tab would.

The answers are not stored as answers. The create endpoint
(`collection_guided.go`) turns them into what a collection already has:

- **Description:** the one they settled on (or their own words if none),
  then a line per Details answer. Auto-fill, research, Find sources and the
  curator all read it, so what was asked keeps steering what is found.
- **Filter rules:** "Prefer <site>" and "Skip <site>", one per source,
  editable on the page. Find sources and the curator's brief read them.
- **Curator**, from the Curation choice, only an agent that resolves.
- **Attached agents**, their own only.

The plain create (a name and a description) is unchanged.

## Curation (built)

The collection page's "Maintained by" is now **Curation**, the same three
choices as the setup's cards, Auto-Curation and Hand Curated at either end:

| Choice | Stored as `CuratorAgent` | What happens |
|---|---|---|
| **Auto-Curation** | the Knowledge app's curating agent | it finds material that fits what the collection is for, adds it, and proposes removals |
| **Curation by Agent** | one of the person's agents, picked in a second select | as before: that agent keeps it with the tools it has |
| **Hand Curated** | empty | nothing goes in unless the person puts it there |

Curation by Agent is offered only to someone with agents, and choosing it
changes nothing until an agent is picked. The header says which it is, and
links down to the section.

The curating agent (`apps/knowledge/curator.go`) is registered hidden: in no
picker, not an agent another can hand work to, not reachable over MCP or the
model API, dispatching nowhere, storing no facts. It searches and reads
pages; adding comes with being a collection's curator, scoped to that
collection.

**Curate now** runs whoever is curator (`collection_curate.go`): one run per
collection, on its own context, alive with documents added and elapsed time,
rejoined by a page opened mid-run, with Stop and an outcome that carries the
curator's report. Auto-fill's document and time caps apply. Removals are
proposed in the report, never made.

## Suggested sources (built)

A section on the collection page, for whoever may add documents (owner and
contributors), `collection_suggest.go`:

- **Find sources** writes searches from the description, runs them in the
  background with progress and Stop, and keeps what is new: nothing already
  in the collection or on its list to fetch, nothing from a weak source or a
  site the rules skip, preferred sites first. Up to 30, each a title, its
  site and the line the search showed.
- They are listed as a checklist; **Add ticked** puts the ticked ones on
  Articles to fetch and fetches them in the background, reporting what could
  not be fetched. Only addresses the search offered are taken.
- Candidates are kept in memory for a day, not on the collection: a search's
  results waiting for a decision, which a restart costs only a new search.

## Fill it for me (built)

A section on the collection page beside Suggested sources, for the same
people (`collection_fill.go`):

- **Draft a plan** writes the web searches Auto-fill would run, from the
  description and rules, and proposes how many documents to add (20, within
  the cap), in the background.
- The plan is shown with its size before anything runs: "8 searches, up to
  20 documents. A run stops at 10 minutes." The searches are an editable
  list and the count a number.
- **Run this plan** runs it as edited: Auto-fill's own search and fetch
  (`autofillPass`, shared with the Auto-fill button), in the background,
  reporting each search and each page as it goes and the documents added,
  with Stop, ending with what could not be used. More searches than a run
  may make are refused; the count is held to the cap.
- A plan is kept in memory for a day, not on the collection.

## Still to build

5. **Refresh on a schedule** for an auto-curated collection: weekly, monthly
   or never, set on the collection page, on gohort's scheduler. The curating
   agent looks for what is new against the description, adds what fits, and
   leaves a note of what it did. The page says when the next refresh is due.
   Within Auto-fill's caps.

Progress, cost and caps hold throughout: anything that runs shows it is
alive and how far along, with a Stop; a plan shows its size before it runs;
the caps are the "Ingest & autofill" tunables an administrator sets.

## No migration

A collection's curator is the field it already has; Auto-Curation is a new
value in it, and a collection set to one of the person's agents, or to
nobody, reads and works as before. The guided setup writes a collection the
same shape the plain form does, and suggested sources are not stored.

## Decided

- **Removals:** proposed in the curator's report, for the person to
  confirm, never made on its own.
- **The default start:** Suggest sources, beside whichever curation is
  chosen; a plan is the other start offered.

## Open questions for the owner

1. **Can your assistant ask it for things?** Hidden, the curating agent
   works only from Knowledge's own pages. Should a person's own assistant be
   able to say "add this to my Kubernetes collection" and have it go through
   the curator, or is that left to Curation by Agent?
2. **Shared and deployment collections.** An administrator's
   deployment-wide collection: may it be auto-curated, and who pays for its
   runs? Today its page offers neither Curation nor Suggested sources.
