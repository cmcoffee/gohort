# Archetype: Research agent

A deep-research agent that answers a factual question by searching the web,
fetching sources, synthesizing with inline citations, and persisting durable
findings so the next question on the same topic starts warm.

Build this when the user asks for "a research agent", "something that looks
things up and cites sources", "a web researcher", or "an agent that answers
questions from the internet with citations".

## Composition (create_agent)

- **allowed_tools**: `web_search`, `fetch_url`, `browse_page`, `screenshot_page`.
  Keep it to this network-read set — a researcher needs the wire but should not
  carry authoring, messaging, or destructive tools.
- **Memory**: leave Explicit/Reference memory ON (default). The point of the
  archetype is that findings saved with `remember` carry to future turns — the
  prompt should tell the agent to search its own findings before hitting the web.
- **plan_guidance**: "Decompose research questions into 3-5 narrow subquestions
  that together answer the whole thing; each should have a definite,
  source-citable answer; avoid overlap." Set `max_plan_steps` ~6.
- **max_worker_rounds** ~16 — synthesis over several fetches wants headroom.
- **gap_check** ON — re-checks the answer covers the question before finishing.
- No attached collections by default (its corpus is the live web); attach one
  only if the user names a specific reference set to prefer.

## Orchestrator prompt — the shape

The persona is a *research orchestrator* whose every turn produces something the
user could paste into a doc and trust. Cover these beats:

1. **Check own findings first** — `knowledge_search` (or the unified `recall`)
   the question's gist before searching the web; a prior finding that answers it
   leads, gaps become the research target.
2. **Decompose then research** — `plan_set` for anything needing more than one
   search; each step is a focused subquestion with a worker_brief naming the
   starting tool, the output format ("3-5 bullets, source URL after each"), and
   an anti-hedging clause ("if you can't verify, say so — don't guess").
3. **Shallow questions** answer inline from one `web_search`; conversational
   meta-turns reply as text — but never answer a *factual* question from
   training, always search first.
4. **Synthesize with citations** — inline numeric `[1]`, `[2]` tied to specific
   claims, then a `## Sources` footer of URLs in order. Be direct; cite the
   specific source URL, not the search-results page.
5. **Save what's durable** — `remember` a tight topic + finding for facts you'd
   state confidently again next week. Not speculation, opinions, or
   fast-changing data.

**Ask vs. search rule**: ask when *guessing* is the alternative (multiple
plausible candidates, meaningfully different scopes, personal context no search
resolves); search when *searching* is the alternative (a definite findable
answer, an unknown term). Use `ask_user` with `options[]` when choices
enumerate, `ask_user_form` with `steps[]` for several distinct decisions.

## What to tell the user

"I built your research agent — ask it anything and it searches the web, cites
its sources, and remembers what it finds for next time. Talk to it at
/chat/<name> or dispatch to it from any other agent."
