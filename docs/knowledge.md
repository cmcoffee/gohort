# Knowledge

**Knowledge** holds **collections**: named sets of documents your agents
search when they answer. A collection is made once and read by as many agents
as you attach it to, so a product's reference, a team's runbooks or a standard
lives in one place and every agent that needs it works from the same copy.

Knowledge is one of three places an agent finds things:

- **Memory** is what an agent learns by itself, as you talk to it (see
  [Memory](memory.md)).
- **Knowledge** is documents you choose, searched by meaning.
- A **file store** is a folder of files an agent reads, searches and runs
  commands over, as files (see [File stores](file-stores.md)).

Knowledge searches by meaning, with an embedding model. The built-in embedder
works without setup; to use an embedding server instead, choose it under
**Administrator › Services › Embeddings**.

## Making a collection

Open **Knowledge** and press **+ New collection**. A short setup asks:

1. **Purpose**: what the collection is for, in your own words. Name the
   products, standards, sites or kinds of document: concrete names find
   better material than a topic does.
2. **Name**: a name and a description, drafted from what you said. Pick or
   edit them. The description matters: the searches that fill the
   collection are written from it.
3. **Kind**: how it is kept to start with (below).
4. **Sources**: sites to prefer, and sites to leave out. Both optional.
5. **Agents**: which of your agents read it. Shown when you have agents.

**Draft with AI** on the setup works through it with the assistant, when you
know the subject but not the words that find the right documents.

| Kind | For | What it sets |
|---|---|---|
| **Reference** | Docs on a product, API or standard, which change slowly | A plan is drafted for you to check and run; then it looks each month, up to 20 documents a run, preferring official sources |
| **Keep up with a topic** | News, releases, advisories | A plan is drafted; then it looks every week, up to 10 documents a run, and skips pages more than a year old |
| **My own files** | Material you upload yourself | Nothing is searched for |
| **Set it up yourself** | Your own mix | How often it looks, how much a run adds, and what runs first |

Everything a kind sets can be changed later on the collection's page. Without
web search on the server, nothing can look for material, so a collection is
**My own files** unless you choose otherwise.

## Adding documents yourself

On the collection's page:

- **Upload**: PDF, Word, Markdown, text, logs, CSV, JSON, YAML and HTML.
- **Paste text...**: a note, runbook, config or JSON from the clipboard, as
  one document under a title. Headings become sections. Pasting again under
  the same title replaces it.
- **From the web**:
  - **Articles to fetch**: particular articles the collection should hold.
    Paste a link, or describe one to search for ("the vendor's 2026 command
    reference"). A run of the plan fetches these first; **Fetch these only**
    fetches them now. Taking one off the list keeps its document.
  - **Find sources**: searches from the description and lists what it finds,
    each with its site and a line from it. Tick what you want and press
    **Add ticked**.
  - **Research & add**: researches a topic on the web and adds the write-up,
    with its sources, as one document.

What you pick yourself is never checked: you chose it.

## Curation: keeping it current

**Curation**, further down the page, is the plan that fills the collection
for you. The line under the collection's name says whether it runs on its own,
with a **Curation ↓** link to it.

- **Auto-Curation**: **Off** (it runs when you press **Run now**), **Every
  month** or **Every week**.
- **Draft** writes the plan from the description.
- **Search terms**: one web search each. A run searches these and keeps what
  fits.
- **Rules**: what to prefer and what to skip ("Prefer the vendor's docs site",
  "Skip forums"). Read when the search terms are written and when pages are
  checked.
- **Documents per run**: at most this many are added in one run.
- **Run now** runs the plan once, with progress and a Stop.

A run fetches the **Articles to fetch** first, then links agents suggested,
then the search terms.

### The check, and Held for you

Every page a run finds is **checked** before it goes in: does it fit what the
collection is for, and does it agree with what the collection already says? A
page from a secondary source (an article or post about the thing, rather than
its own word) also needs an independent source saying the same.

A page that does not pass waits under **Held for you**, with why it was held:

- **Add it**: put the page in anyway.
- **Replace the old one**: put it in and remove the document it contradicts.
- **Keep the old one**, or **Leave it out**: turn the page away.
- **Settle it**: two sides argue it out first, one for the page and one for
  what contradicts it, each citing what it read. Their citations are checked
  against those pages and a ruling is shown on the row. Nothing changes until
  you choose.

### Agents that submit documents

**Agents that submit documents**, at the end of Curation, lets your own
agents add to the collection, such as one that fetches the latest guide on a
schedule. For each, choose whether its documents are **Checked** (the same
check a run uses) or **Always allow**. An agent can add, never remove. One
that adds under a title already in the collection replaces that document, so
it can keep one current version.

## The documents

**Documents** lists what is in the collection.

- **Version** marks a document as an older version of another. The older one
  stays, ranked below the one that replaced it and labelled as older, so an
  agent reading it knows.
- Select documents and press **Remove selected** to delete them.
- **Audit** checks the oldest documents for staleness with web research and
  shows a report. It can take a minute or two.
- **Search** tries a question against the collection, to see what an agent
  would find.

## Giving it to agents

On the agent's page, open **Configure › Knowledge**. **Attached collections**
lists the collections you can read; toggle one to attach it. The same window
takes files private to that one agent (**Upload**), and **+ Create knowledge
for this agent** makes an empty collection named after the agent and attaches
it.

A collection can also go with a **skill**: under **Extensions › Skills**, a
skill's **Attached collections** are searched only on turns the skill is in
use.

An attached collection gives the agent:

- `knowledge_search`, which returns passages with the document and section
  they came from.
- `fetch_knowledge_doc`, which reads a whole document, a section of it, or the
  lines that match a word.

The agent sees when each document was added, so an old one reads as old.

### Suggestions from agents

An agent reading a collection never writes to it, because it would later read
its own conclusions back as fact. When it learns something the collection
should know, from a source it can name, it files a suggestion with
`suggest_collection_update`.

Suggestions wait under **Suggestions from agents** on the collection's page,
each with its source. **Fetch the link** adds the page the suggestion points
at, and **Dismiss** drops it. Only your own agents, and those of people you
gave **Add documents**, can suggest. Turn **Take suggestions from agents** off
to stop them.

## Sharing a collection

The owner shares a collection from **Shared with** on its page, with people
or a team. Each starts at **Read**; change it on their chip:

- **Read**: it appears in their collections, and they can attach it to their
  agents and search it.
- **Add documents**: they can upload, paste, fetch articles and research into
  it. Worth a pause: an agent takes a collection as fact, so whoever adds to
  it decides what every agent reading it believes.
- **Maintain**: adding, and curating besides: suggestions, held pages, the
  audit, the description, rules and articles.

There is only ever one copy: a document you add later is shared too, and one
you remove is gone for everyone. The name, who it reaches, Curation and
deleting it stay the owner's. A card's colour on the Knowledge page says how
you hold it: your own, one you maintain, or one shared with you.

The deployment can also have **shared knowledge** that everybody reads and
only an administrator changes. See [Sharing](sharing.md) for sharing across
gohort.

## Bulletins

A collection is found by search when a question matches. A **bulletin** is
pushed instead: a board holds one short post, and every agent that follows the
board sees it on every turn. Use it for a daily briefing or a message of the
day.

Under **Bulletins**, on the Knowledge page:

- **New board**: a name, what it carries, and **Current for (hours)**, after
  which followers stop seeing a post (0 keeps it current).
- **Edit** posts, up to 1,200 characters, replacing the last post. **Clear**
  empties the board.
- **Followers** chooses which agents read it, and **Posters** lets an agent
  post, with a `bulletin_board` tool.

An agent's **Configure › Knowledge** lists the boards it follows, under
**Bulletins it follows**.

## Export and import

**Export**, on a collection's page, downloads it as a bundle. The document
text travels; the search index is rebuilt where it is imported. **Import...**,
on the Knowledge page, previews a bundle and brings it into your own
knowledge.

## Tips

- Write the description as if it were the search: name the official
  reference, the command guide and the standards body, not just the subject.
- Start a Reference collection with **Run now** once, look over what came in
  and what was held, then turn on **Auto-Curation**.
- Use **Rules** to steer every run at once, rather than removing the same
  kind of page by hand.
- Keep one current version of a document: paste or submit under the same
  title, or mark the old one with **Version**.
- For log bundles and other files an agent should read as files, use a
  [file store](file-stores.md) instead.
