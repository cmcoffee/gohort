---
{
  "summary": "An agent that gathers from several sources on a schedule and posts one short summary of what is new, to a bulletin board other agents see on every turn, to the owner's Cortex, or to a chat; the gathering stays in its run, and every reader gets the same few lines without fetching anything themselves.",
  "aliases": [
    "roundup",
    "briefing",
    "newsletter",
    "summarizer"
  ],
  "match": [
    "daily digest",
    "weekly digest",
    "morning digest",
    "morning briefing",
    "daily briefing",
    "news roundup",
    "weekly roundup",
    "round up",
    "today's headlines",
    "the headlines",
    "bulletin board",
    "to a bulletin",
    "post a summary",
    "summary of what's new",
    "what's new in",
    "daily summary",
    "weekly summary",
    "reports back"
  ],
  "asks": [
    "What should it gather from, and how often?",
    "Where does the summary go: a bulletin board your agents read, your Cortex, or a chat?",
    "What makes something worth including, and should it post when nothing is new?"
  ]
}
---
# Archetype: Digest

An agent that gathers from several sources on a schedule and posts one short
summary of what is new: to a bulletin board other agents see on every turn, to
the owner's Cortex, or to a chat. The reading stays in its run; every reader
gets the same few lines without fetching anything themselves.

Build this when the user asks for "a daily digest of our team's news", wants
an agent to "post today's headlines to a bulletin board", or asks for "a
morning briefing of what's new in our repos"; or when several agents each run
the same lookup on every turn to learn the same thing.

## Not a watcher

A scheduled watcher looks at one thing and reports a value, or stays silent
until it crosses a line. A digest reads several sources, chooses what matters,
and writes a summary every time it runs. When the request is "tell me only if
X happens", that is the watcher recipe; when it is "tell me what happened",
it is this one.

And when a board only needs one value with no judgement (a price, a status),
a watch monitor created with `bulletin=<board>` posts it with no model at all.
Use a Digest when the post has to be written.

## Why a separate agent

- **The transcript.** Reading ten sources is long; the post is six lines. On
  the agent people talk to, the reading fills the conversation; on a Digest,
  nobody sees it.
- **One read for every reader.** Agents that follow the board get the post on
  their turn without a tool call, so the lookup runs once a day instead of once
  per turn per agent.
- **Its memory is what it posted.** "What is new" needs what was there last
  time, and which sources are noise; that belongs to the Digest.

Make it a sub-agent (**owned_by** the parent, **hidden: true**) when one agent
is its main reader. Make it top-level when its board serves agents that no one
parent owns.

## Composition (create_agent)

- **The sources** are the first ask and the person's to choose; the recipe
  needs a way to read each one (`fetch_url` or `web_search` for the web, a
  folder or guide attached as a Source, the person's API tool). Nothing that
  writes or sends beyond where the summary goes.
- **The schedule**: `schedule` with `when: "every"`, `time` as the user said it
  ("daily 07:30", "weekdays 08:00"), `agent` set to the Digest, and `then` as
  the mission ("read the sources and post today's summary"). Not a monitor: a
  digest runs every time; that is the point. A parent can schedule its own
  sub-agent.
- **The destination** is the second ask:
  - **A bulletin board**: `bulletins create {name, desc, ttl_hours}`, then
    `allow_poster {board, agent}` and `follow {board, agent or "all"}` for the
    readers. A sub-agent posts under its parent's right, so grant the parent.
    Set `ttl_hours` a little past the cadence (26 for daily): a run that fails
    lets the post go out of date instead of showing yesterday's as today's. A
    post holds at most 600 characters, and a longer one is refused.
  - **The owner's Cortex**: the run's own report is the digest; no tool.
  - **A chat**: the messaging tool from its channels (a sub-agent sends through
    its parent's), or hand it to a Correspondent if one holds the messaging.
- **Memory ON**, `memory_mode: "agent"`: what it last posted per source goes to
  reference memory; a lesson ("this feed repeats yesterday's items") is a
  fact.
- **think**: `"off"`. It runs on every fire, and what to include is decided by
  the criteria in its prompt, not deliberated each time.
- **max_worker_rounds** ~10: one read per source, then write and post.
- **gap_check** OFF: the question is the same every run, and the pass costs a
  model call on every fire.
- **rules vs. persona**: `rules` renders above memory and above the persona and
  wins every conflict, so the two things a summary drifts into go there:
  "every line comes from a source read in this run; never carry an item
  forward from memory as if it were new" and "when a source could not be read,
  say which one rather than leaving it out". Tone and layout: persona.

## Orchestrator prompt: the shape

1. **Read every source, once.** No exploring past them: a scheduled run that
   wanders is a bill nobody watched.
2. **Keep what is new** since the last post; recall what that was before
   writing.
3. **Choose by the owner's criteria** (the third ask). Fewer lines that matter
   beat a complete list.
4. **Write to fit where it goes.** A board takes about six short lines, each
   naming its source; a chat takes less. The Cortex report can carry more.
5. **Post it.** When nothing is new, do what the third ask said: say so in one
   line, or post nothing and let the board's post go out of date. Never repost
   the last one as today's.
6. **Name what failed.** A source that timed out is a line in the post.
7. **Remember what was posted**, per source, for the next run.

## Wiring the readers

- **Followers need no tool.** An agent that follows the board sees the latest
  post on each of its turns, with when it was posted. Tell its prompt what the
  board is: "The <board> bulletin is <digest>'s summary of <topic>, refreshed
  <cadence>. Answer questions about <topic> from it first, and say how old it
  is when that matters."
- **A fresh one on request**: a beat in the parent's prompt, "When the user
  asks for the latest on <topic> now, delegate to <digest>."

## What the framework does on its own

Say these to the user rather than putting them in the prompt:

- A board's post goes out of date after its `ttl_hours`, and followers stop
  seeing it.
- A schedule whose runs keep failing backs off: each failure in a row pushes
  the next run further out (twice the cadence, then four times, up to
  sixteen), and the first run that works resets it.
- The owner can write or clear the board from Knowledge > Bulletins.

## What to tell the user

"I set up <name>: every <cadence> it reads <sources> and posts what is new to
<the <board> board / your Cortex / <chat>>. <readers> see the post on every
turn without looking anything up. If a source can't be read, the post says
which one; if a run is missed, the post goes out of date after <ttl> hours
rather than passing as current."
