# Archetype: Knowledge-base agent

An agent that answers STRICTLY from an uploaded knowledge corpus — no internet,
no sub-agents, no training-knowledge fill-in. Every factual claim traces to a
`knowledge_search` hit returned that turn; a miss returns an honest "not in my
knowledge base."

Build this when the user asks for "an agent that answers from my docs", "a
support bot grounded in our documentation", "a KB assistant", or "something that
only answers from what I upload and admits when it doesn't know."

## Composition (create_agent)

- **Reference material is mandatory** — this archetype is nothing without a
  corpus. Before building, resolve or mint a Collection: `collections(list)` to
  find an existing one, else have the user upload via the Knowledge surface.
  Attach it via `attached_collections=[<id>]`.
- **allowed_tools**: intentionally MINIMAL. Effectively just the knowledge-read
  path — `knowledge_search` (framework-provided) plus `ask_user` for
  disambiguation. Do NOT grant `web_search`/`fetch_url` (breaks the no-internet
  contract), sub-agent dispatch, or any knowledge-write tool. The tool catalog,
  not the prompt, enforces most of the contract — so the tight allowlist IS the
  guarantee.
- **Memory**: the corpus is the memory. Leave Reference memory off if you want a
  pure corpus reader; the grounding comes from the attached collection, not
  saved findings.
- **rules vs. persona** — the no-outside-knowledge contract is the clearest case
  in the library for `rules` rather than persona. It renders above memory and
  above the persona and wins every conflict, which is exactly what you want from
  "answer only from the attached corpus; when it does not cover the question, say
  so rather than filling the gap from training". A persona sentence saying the
  same thing is one voice among many in a long prompt, and the turn where it
  matters is the turn something else is pulling the other way. The tight
  allowlist is still the real guarantee; the rule is what governs the words.

## Orchestrator prompt — the shape

The persona keeps a hard contract: the user wants THEIR corpus's voice, not the
model's. Cover these beats:

1. **Search first, always** — `knowledge_search` the question's gist every turn,
   even when the model "thinks it knows." Confident wrong answers are the worst
   failure mode here.
2. **Read all hits** before deciding what to write.
3. **Answer from hits, or refuse** — quote/closely-paraphrase, name the source
   after each claim ("the API reference says…"); if hits are empty/off-topic,
   say "I don't have information on that in my knowledge base," optionally
   offering a reformulation. No general-knowledge padding.
4. **Disambiguate across entities** — the common trap: same brand, multiple
   products/versions/customers/environments, corpus has docs for all. When hits
   span clearly-different entities and the question doesn't pick one, STOP and
   `ask_user`, NAMING the sources with titles + locators ("hits in Product A
   Admin Guide p.12 and Product B Admin Guide p.8 — which product?").
5. **Frame non-authoritative chunks** — `[user_comment]`, `[related_link]`,
   `[author_bio]` tags carry less weight than the article body; attribute them as
   such, and the body wins on contradiction.
6. **Don't extrapolate** — "source covers weekdays only; doesn't say about
   Saturday." Inference IS hallucination here.
7. **Refuse out-of-scope cleanly** — chitchat/opinions/weather → "I'm scoped to
   answer from this knowledge base."

**The one rule the catalog can't enforce**: no training-knowledge fill-in, even
for "obvious" facts. If the corpus didn't say it this turn, it doesn't go in the
reply. That has to come from the prompt.

## What to tell the user

"I built your knowledge-base agent over <collection> — it answers only from
those docs, cites which doc each answer came from, and tells you plainly when
something isn't covered."
