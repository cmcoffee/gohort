---
{
  "summary": "An agent that checks something on a clock and reports what it found, every time it looks or only when what it sees crosses a line.",
  "aliases": [
    "watcher",
    "monitor",
    "scheduled",
    "watch"
  ],
  "match": [
    "every n minutes",
    "every five minutes",
    "every hour",
    "every morning",
    "watch this",
    "watch our",
    "keep an eye on",
    "alert me if",
    "alert me when",
    "tell me when it changes",
    "let me know when",
    "notify me when",
    "goes down",
    "on a schedule",
    "check it regularly",
    "every n hours",
    "every n seconds",
    "every day",
    "every week",
    "watches our",
    "watches the",
    "watch the",
    "watch a page",
    "check it every",
    "checks every",
    "daily",
    "nightly",
    "when it changes",
    "if it goes down",
    "when it goes down"
  ],
  "asks": [
    "What should it look at, and how often?",
    "Do you want to hear from it every time, or only when something crosses a line?"
  ]
}
---
# Archetype: Scheduled watcher

An agent that checks something on a clock and reports what it found — every time it looks, or only when what it sees crosses a line.

A status endpoint, a queue, a page, a room: the subject varies, the shape does
not. Half of building one is choosing the trigger, and that is the half that
goes wrong.

Build this when the user asks for "check X every N minutes", "watch this and
tell me when it changes", "keep an eye on Y", "alert me if Z goes down", "let
me know when the PR is merged", or any request whose shape is *a thing to
look at* plus *a cadence*.

## Choose the trigger FIRST — it decides everything else

This is the step that goes wrong, and it goes wrong in one direction: reaching
for an event monitor because the request contains "every 5 minutes", when the
job is unconditional work on a clock.

**Does the user want to hear from it every time it looks?**

- **Yes → a standing agent** (`create_standing_agent`). "Fetch the status and
  tell me the value" has no trigger to wait for: the schedule *is* the trigger.
  Give it `interval_seconds` or `cron`, and a `mission` saying what to do and
  what to report.
- **No, only when something changes or crosses a line → an event monitor**
  (`create_event_monitor`). A monitor exists to stay SILENT until then.

**Two signs you picked the monitor for a schedule's job.** Both mean stop and
use a standing agent instead:

- You are writing a checker brief that tells the agent to always end its answer
  with the match word, so it fires every interval.
- You are leaving `threshold` empty, or picking a comparison that is always
  true, so it fires whatever the value is.

A condition you cannot write down is a condition that does not exist.

## Bounding it — say when it stops

An unbounded watch runs until somebody remembers to stop it. If the user put a
limit in the request, it belongs on the record, not in the agent's head:

- **A count of alerts** → `stop_after` on the monitor. It pauses itself on the
  last fire, kept and resumable.
- **A count of runs** → `max_attempts` on the standing agent.
- **A finish line in words** → `until` on either: "the ready field reports
  true", "the PR is merged". Every fire is judged against that sentence, and
  the one that reaches it is the last. On a standing agent, pair it with
  `max_attempts` so a goal that never arrives stops and says so instead of
  going quiet.

"Check it twice and stop" is `until` + `max_attempts` on a standing agent — not
a monitor with a fire cap, because the monitor only counts fires it actually
made.

## Picking a monitor kind — cheapest that detects the change

1. **`webhook`** — the external system POSTs to a minted URL. No polling at all.
2. **`http_poll`** — fetches a URL, extracts a value (`json_path` or `regex`),
   compares it (`compare_op` + `threshold`). No LLM.
3. **`watch`** — invokes a TOOL each interval and hashes its output; wakes only
   when the output changes. No LLM until something does. This is the one for
   "tell me when this chat/page/roster changes".
4. **`poll`** — runs an LLM checker agent every interval. The most expensive by
   a wide margin. Reserve it for a fuzzy condition no value or hash can express.

`interval_seconds` has a floor of 30 and should match how fast the thing can
actually change; a human reply or a deploy is minutes, not seconds.

**The edge-trigger rule, which surprises people:** a monitor fires on the
crossing INTO the condition and re-arms only when a later check finds it false
again. A condition that can never go false fires exactly once and then goes
quiet forever — the schedule keeps running and nothing else happens. If the
thing being watched stays tripped for long stretches, either that single alert
is what you want, or the job was a standing agent all along.

## Composition (create_agent)

- **allowed_tools**: only what looks at the subject — `fetch_url`,
  `web_search`, `browse_page`, `screenshot_page` for the open web; the specific
  API or credential-backed tool when the subject is a system. A watcher reports;
  it should not carry authoring, messaging or destructive tools unless the user
  asked for a watcher that also acts.
- **Conductor tools ON** if this agent is the one that will SET UP its own
  watches. Without it there is no `create_standing_agent` and no
  `create_event_monitor`, and the agent will improvise something else rather
  than say it cannot. An agent that is merely *run by* a schedule does not need
  them.
- **Memory ON**. A watcher that cannot remember what it saw last time reports
  every observation as if it were new. Findings it saves are what make "it went
  from 4 to 11 over the week" possible.
- **max_worker_rounds** ~8. A check is one fetch and a judgement, not a
  research project; a low ceiling keeps a scheduled agent from turning a
  transient failure into a long expensive turn.
- **gap_check** OFF. The question is fixed and narrow, and the extra pass costs
  a model call on every fire.
- **rules vs. persona** — `rules` renders above memory and above the persona and
  wins every conflict, so what the watcher must never do belongs there: "report
  what you observed this run and nothing else — if the check failed, say it
  failed rather than reporting the last known value". A scheduled run has no
  reader in the moment to catch an invented number.

## Orchestrator prompt — the shape

The persona is a reporter with one beat. Cover these:

1. **Look, once.** Call the one tool that answers the question. Do not go
   exploring: a scheduled run that wanders is a bill nobody watched.
2. **Say what you observed, in the words a person can act on.** The value, the
   state, the change since last time. Lead with it — a fire that buries its
   finding under process is a fire the owner learns to skip.
3. **Say when you could not look.** A failed fetch, a timeout, an error is a
   REPORT, not silence and not a guess. Name what failed.
4. **Say what changed, not just what is.** "still 200" is worth one line;
   "changed from 200 to 503 since the last check" is the whole point.
5. **Stop.** No follow-up work unless the mission asked for it. A watcher that
   starts fixing things is a different agent with a different approval story.

## What the framework does on its own

Say these to the user rather than building them into the prompt — they are
already true, and an agent told to do them again will do them twice:

- A check that keeps failing stops itself after three consecutive failures and
  says why, rather than retrying into the void forever.
- A monitor that has fired and whose condition has stayed true says so, once,
  rather than looking healthy while it can no longer fire.
- A watch that goes a long time with nothing to report pauses itself, kept, and
  resumes with one click.
- A stopped schedule states WHY on its row: finished, paused, stalled, or needs
  attention — with the reason it stopped.
