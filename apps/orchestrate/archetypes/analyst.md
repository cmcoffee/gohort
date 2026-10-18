---
{
  "summary": "A sub-agent that answers questions about data (files in a folder, an export, a spreadsheet, what an API returns) by inspecting it and computing the answer, in its sandbox where it may run scripts; the scripts, the retries and the large outputs stay in its run, and its parent gets the answer, the method and what was left out.",
  "aliases": [
    "analytics",
    "data analyst",
    "statistician",
    "number cruncher"
  ],
  "match": [
    "analyze my data",
    "analyse my data",
    "analyze the data",
    "analyse the data",
    "data analysis",
    "crunch the numbers",
    "run the numbers",
    "make sense of the data",
    "trends in",
    "spreadsheet",
    "spreadsheets",
    "csv",
    "csvs",
    "pivot table",
    "breakdown by",
    "how many per"
  ],
  "asks": [
    "Where is the data: files someone drops in a folder, files you upload, or something an API or tool returns?",
    "What does it answer: questions as they come, or the same figures each time?",
    "May it write and run scripts in its sandbox, or only read the files and do arithmetic?"
  ]
}
---
# Archetype: Analyst sub-agent

A sub-agent that answers questions about data by looking at it and computing
the answer: files in a folder, an export, a spreadsheet, what an API returns.
Where it may, it writes and runs scripts in its sandbox. The scripts, the
failed attempts and the large intermediate outputs stay in its own run; its
parent gets the answer, how it was worked out, and what was left out.

Build this when the user asks for an agent to "crunch the numbers in our
exports", to "analyze the CSVs we drop in a folder", or to "find trends in our
sales spreadsheet"; or when an agent keeps answering a counting question by
reading rows and estimating.

## Why a SUB-agent

The reason is the transcript.

- **The work is long and the answer is short.** Inspecting a file, writing a
  script, fixing it, rerunning it, reading a 10 KB output: a dozen rounds of
  material nobody needs again. On the parent, that fills the conversation the
  user is having; on an Analyst, the parent sees one result.
- **Its memory is about the data.** Which column holds the amount, which date
  format a file uses, which export is the current one, the header row repeated
  every thousand lines: facts that make the next question quicker and do not
  belong in the parent's memory.
- **Running code can live on it alone.** The parent does not need a sandbox to
  talk about the results.

## Composition (create_agent)

- **owned_by**: the parent's ID, with **hidden: true**.
- **The data** is the first ask, and the person's to answer; the recipe needs a
  way to reach it, not a particular one:
  - a folder on this host: attach it as a Source (`files:<slug>`), which gives
    `list_<slug>`, `search_<slug>` and `read_<slug>`, read-only;
  - files the user uploads: they land in its workspace;
  - an API or tool: the person's tool or connected account, attached as they
    choose.
- **Running scripts** is the third ask. When yes, it needs the `workspace` tool:
  `run` works in a sandbox where the workspace is the only writable path, with
  a 90-second limit and 10 KB of output per call, and a folder Source can be
  the starting directory (`cwd_root: "files:<slug>"`) while staying read-only.
  When no, it has its read tools and `calculate`, and it must say when a
  question needs more rows than it can read.
- **On a schedule** (the second ask, "the same figures each time"): an
  unattended run refuses a tool that needs approval and queues the request;
  once the owner approves it there, later runs use it. Say so when you set one
  up, or the first scheduled run reports nothing.
- **Memory ON**, `memory_mode: "agent"`: what it learns about a dataset goes to
  reference memory under the dataset's name; a lesson about method ("this
  export double-counts refunds") is a fact.
- **think**: `"on"`. Choosing the computation, and deciding whether a result is
  believable, is the job. (Sub-agents default to `"off"`; override it.)
- **max_worker_rounds** ~16: inspect, script, fix, rerun, check.
- **gap_check** ON: an analysis tends to answer the part of the question the
  data made easy.
- **rules vs. persona**: `rules` renders above memory and above the persona and
  wins every conflict, so the two constraints a model drops when a number looks
  plausible go there: "every figure in the answer comes from a computation or a
  read in this run; never estimate a number you could compute" and "say how
  many rows were used and how many were dropped, and why". How to lay out a
  table, how much method to explain: persona.

## Orchestrator prompt: the shape

1. **Look at the data before computing anything.** Size, columns, units, a few
   rows, the row count (`workspace stat` and `head` on a file, `read_<slug>`
   on a folder). A script written against a guessed layout computes the wrong
   thing correctly.
2. **Say the question as a computation**: which rows, which columns, which
   operation. When the question allows two readings, name the one used.
3. **Compute; do not count by reading.** A script for anything past a page of
   rows, `calculate` for arithmetic. Never add up numbers in prose.
4. **Check the result before reporting it**: rows in against rows out, a total
   against one the data states, one row worked by hand. A result that fails a
   check is reported as failing it.
5. **Report**: the answer first, then the figures, the method in a sentence,
   rows used and dropped, and the assumptions. Name any file it wrote so the
   parent can ask for it.
6. **Remember the dataset, not the answer.** Column meanings, quirks, which
   file is current: those make the next question faster. Last week's total is
   in last week's run.

**Reporting shape**: one line with the answer, then the figures (a short table
when there are several), then "Method:", "Rows:" and "Assumed:".

## Wiring the parent

- **A beat in the parent's prompt:** "For a question that needs counting or
  computing over <the data>, delegate to <analyst> with the question as the
  user asked it. Pass on its answer with its caveats; do not recompute or round
  its figures."
- **Attach the Analyst under the parent's Sources** if the parent should be
  able to answer a quick question about the data's layout from what the Analyst
  has recorded (`search_<analyst>_memory`) without a run.

## What to tell the user

"I gave <parent> an analyst: questions about <the data> go to <name>, which
looks at the files, <runs scripts in its sandbox / reads and calculates>, and
reports the answer with how it got there and which rows it left out. Its
working stays out of your conversation with <parent>."
