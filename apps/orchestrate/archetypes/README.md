# Agent archetypes

One file per agent SHAPE. These are build recipes, not agents: Builder reads
one and composes a user-owned agent from it, so a shape can be versioned,
diffed and customized without a framework persona living in everybody's fleet.

Seeds are the neighbouring library (`../seeds/`) and answer a different
question. A seed IS an agent, written in second person to the model. A recipe
is written to Builder about how to build one. Where a shape exists as both, the
recipe names its seed and a test holds the two together.

## Format

JSON frontmatter between `---` fences, then the recipe as markdown:

```
---
{
  "summary": "A deep-research agent that answers a factual question by searching the web.",
  "aliases": ["researcher"],
  "seed": "seed-research",
  "settings": { "allowed_tools": ["web_search"], "max_worker_rounds": 16 }
}
---
# Archetype: Research agent
...
```

The slug is the filename stem, so a doc cannot disagree with its own name.
Builder is handed the body only; the header is for the framework.

| key | meaning |
|---|---|
| `summary` | required. The one line Builder reads when choosing between shapes. Write a sentence. |
| `aliases` | the words a model actually types for this shape (`kb`, `probe`, `watcher`). Slug resolution also matches on a contained word, so near-misses still land. |
| `seed` | the seed agent that ships this shape, when one does. |
| `template` | this shape's label in the New Agent wizard's "Start from a template" row. Present means the wizard offers it, and picking it clones `seed`, so a `template` without a `seed` is refused. |
| `settings` | the parts of the recipe a test can check. Optional. |

`settings` holds `allowed_tools`, `max_plan_steps`, `max_worker_rounds`,
`gap_check` and `rules_required`. Everything is optional and unset means the
recipe does not say, which is different from saying zero: an empty
`allowed_tools` prescribes the default pool, while omitting it leaves the
allowlist to the subject at hand.

Keep `settings` narrow. What an agent may reach and how far it may go are worth
pinning; the rest of a recipe is judgement, and prose is the right form for it.

## Rules the loader enforces

- Unknown frontmatter keys, a missing summary, an empty body and a duplicate
  slug are all errors, and any of them stops startup. This used to swallow read
  errors instead, so a broken recipe presented as a shape Builder had never been
  given and nobody learned why the agent came out different.
- A recipe naming a `seed` must agree with that seed on every setting it
  declares. A user who clones the wizard template and a user who asks Builder
  for the same thing should not end up with agents of different reach.
- A `template` label with no `seed` is an error: the wizard would offer a
  starting point with nothing behind it.

## The wizard row

The "Start from a template" options are read from these headers, ordered by
label. Adding a shape to that row is adding a `template` line to its recipe;
there is no list of templates anywhere else. The create endpoint guards on the
same derivation, so a forged POST cannot clone a seed the row does not offer.

## Adding one

Drop a new `.md` file here and the `archetype` tool lists it. `README.md` is the
one reserved name.
