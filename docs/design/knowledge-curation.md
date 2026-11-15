# Knowledge: a guided start, and auto-curation

Status: proposed (2026-11-14). Nothing built.

## Why

A new collection is two fields: a name, and a description (with Draft with
AI to word it). After that the person lands on the collection's page, where
the ways to fill it sit side by side: Auto-fill, Research, upload, and a
"Maintained by" agent. Every piece is there; nothing asks what the
collection is for, and nothing helps choose between them. Someone who
wants "a reference on our deployment tooling" has to know that Research
writes cited reports, that Auto-fill saves raw pages from searches it makes
up from the description, and that neither is the right call for their own
contracts.

This makes "+ New collection" a short guided setup that asks what the
collection is for and how they want it filled, and lets Knowledge look
after a collection itself.

## What exists (verified in code)

| Piece | Where |
|---|---|
| New collection | `apps/knowledge/new_collection.go`: name and description, Draft with AI |
| Auto-fill | `apps/orchestrate/collections.go` `collectionAutofill`: searches written from the description, raw pages saved, capped by the "Ingest & autofill" tunables |
| Research | same file, `collectionResearch`: the hidden `seed-research` agent writes a cited report per topic (up to five a run), each saved as a document |
| Upload | the collection page: PDF, Word, Markdown, text, web pages |
| Who looks after it | `Collection.CuratorAgent` (core), set as "Maintained by" on the collection page: that agent gets a tool scoped to this collection only (add a page, a file or text; list; remove) and does nothing until asked in a chat or by a schedule |
| Suggestions | agents reading the collection can suggest additions, queued for whoever decides |
| An app's own agent | `appagents.RegisterAppAgent`, as Scribe's Guide Author: the app's, set up in the app, hidden from pickers and from every outside caller when marked Hidden |

## Proposal

### A. A guided "+ New collection"

Steps, in the first-run wizard's style: each a few choices to click, with
room for their own words.

1. **What is it for?** Cards: answer questions about our product or docs;
   keep up with a topic; a reference for a technology; my own files (notes,
   contracts, reports). Or in their words. With a name.
2. **A few questions, from that answer.** How current it has to be;
   sources they trust, or want left out; a solid overview or comprehensive;
   mostly the web or mostly their own files. Each one optional.
3. **How to fill it.** Three cards:
   - **Fill it for me.** A plan is drafted from the answers (the topics it
     will research, the kinds of sources, about how many documents) and
     shown with its size before anything runs: "about 12 searches, up to 30
     documents". They approve or adjust; it runs with progress, and the
     collection is auto-curated from then on.
   - **Suggest sources, I'll pick.** Candidates (sites, pages, papers)
     found from the answers, each a title, a site and a line, with a tick.
     What is ticked is added. The default: the likeliest to collect the
     right material, and they see what is out there.
   - **I'll choose myself.** A short menu, each entry saying what it is
     good for: upload files (your own contracts, notes, exports), add web
     pages or a whole site, paste text.
4. **Keep it current, and who uses it.** Whether it stays auto-curated, and
   which of their agents should read it (attaching it now, the way an
   agent's Knowledge tab does).

The answers become the collection's description, which is what Auto-fill
and research already read, so what was asked keeps steering what is found.
The plan is shown, not stored.

### B. Auto-curated

The collection page's "Maintained by" becomes **Curation**:

| Choice | What happens |
|---|---|
| **Auto-curated** | Knowledge's own curating agent looks after it: fills it, takes suggestions, keeps it current |
| **By one of my agents** | as today |
| **By hand** | as today: nobody adds to it but the person |

Auto-curated is Knowledge's own app agent, built for this job: it reads the
collection's description, plans searches, judges sources, adds what fits,
and proposes removals for what has gone stale. It has no name on screen,
the choice says what it does. It is **hidden**: not in any picker, not an
agent another agent can hand work to, not reachable over MCP or the model
API. It runs only when Knowledge runs it: the guided setup, a refresh, a
suggestion to check. Underneath it uses what Research and Auto-fill
already use: web search, page fetching, cited reports.

Stored as it is today: `CuratorAgent` holds the curating agent's id, so
Auto-curated is that field set to Knowledge's agent.

### C. Keeping it current

An auto-curated collection can refresh on a schedule (weekly, monthly, or
never), set on the collection page: the curating agent looks for what is
new against the description, adds what fits, and leaves a note of what it
did. It runs on gohort's scheduler (core's, the one schedules and monitors
use), so nothing new runs underneath. The page says when the next refresh
is due, and offers Refresh now. Each refresh shows its size and stays
within the same caps as Auto-fill.

### D. Progress, cost and caps

- Anything that runs shows that it is alive and how far along it is (the
  house rule): "8 of 12 searches, 14 documents added, 41s", with a Stop.
- A plan shows its size before it runs, so a person sees the cost of a
  fill before paying it.
- The caps stay the "Ingest & autofill" tunables an administrator already
  sets.

## No migration

A collection's curator is the field it already has. Auto-curated is a new
value in it (Knowledge's agent's id); a collection set to one of the
person's agents, or to nobody, reads and works exactly as now. The guided
setup writes a collection the same shape the current form does.

## Steps

1. Knowledge's curating agent, registered as a hidden app agent, and
   Curation on the collection page with its three choices.
2. The guided "+ New collection": what it is for, the questions, and "I'll
   choose myself".
3. "Suggest sources, I'll pick": candidates found and ticked.
4. "Fill it for me": the plan, its size, approval, the run with progress.
5. Refresh on a schedule for an auto-curated collection.

## Open questions for the owner

1. **Can your assistant ask it for things?** Hidden as proposed, the
   curating agent works only from Knowledge's own pages. Should a person's
   own assistant be able to say "add this to my Kubernetes collection" and
   have it go through the curator, or is that left to "By one of my
   agents"?
2. **Removing documents.** Should auto-curation remove what has gone stale
   on its own, or only propose removals for the person to confirm? Proposed:
   only propose.
3. **Shared and deployment collections.** An administrator's
   deployment-wide collection: may it be auto-curated, and who pays for its
   runs?
4. **The default route.** "Suggest sources, I'll pick" is proposed as the
   default. Or "Fill it for me", for the fastest start?
