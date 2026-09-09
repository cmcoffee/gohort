# Agent shapes

One file per agent SHAPE. Each is a build recipe Builder reads, and, when the
shape ships an agent, the record and the persona that agent wears. One document
per shape, whichever way a user reaches it: cloning it from the wizard, asking
Builder for one, or being handed it as a framework default.

## Format

JSON frontmatter between `---` fences, then the recipe, then the persona:

```
---
{
  "summary": "A deep-research agent that answers a factual question by searching the web.",
  "aliases": ["researcher"],
  "template": { "label": "Research Assistant: cited multi-step research", "order": 1 },
  "rules_required": true,
  "record": { "id": "seed-research", "name": "Research", "allowed_tools": ["web_search"] }
}
---
# Archetype: Research agent

Build this when the user asks for...

## Persona

You are a research orchestrator. ...
```

The slug is the filename stem, so a doc cannot disagree with its own name.

| key | meaning |
|---|---|
| `summary` | required. The one line Builder reads when choosing between shapes. Write a sentence. |
| `aliases` | the words a model actually types for this shape (`kb`, `probe`, `watcher`). Slug resolution also matches on a contained word, so near-misses still land. |
| `record` | the agent this shape ships, in `AgentRecord`'s own json keys. Present means the shape can be instantiated. |
| `template` | `{label, order}`. Offers the shape on the wizard's "Start from a template" row. Needs a `record` to clone. |
| `rules_required` | this shape's contract belongs in `rules` rather than in the persona. A record shipping without them is refused. |
| `notes` | free text for whoever reads the file. JSON has no comments, and the reason a setting is the way it is belongs beside the setting. The loader discards it. |

## The two halves of the body

Everything above `## Persona` is the RECIPE, written to Builder about
construction: which tools, which budgets, what belongs in rules, which traps
this shape falls into. Everything below is the PROMPT, written to the model in
second person.

They were two files once, one in `seeds/` and one here, and they said the same
thing twice: the five numbered beats of the research recipe were the five
numbered beats of the research persona. They drifted exactly where it mattered,
with the recipe insisting the citation contract belongs in `rules` while the
record carried none, so the wizard produced the agent the recipe warns about.

Builder is handed the recipe alone. It composes agents, and a persona it can
copy verbatim is one it will copy instead of composing.

## Shapes that ship nothing

`investigator` and `scheduled_watcher` have no `record` and no persona, because
a watcher's prompt has to name the thing it watches. A shape is instantiable
exactly when its prompt can be written without knowing the subject; otherwise
Builder composes one from the recipe.

## Rules the loader enforces

- Unknown frontmatter keys, a missing summary, an empty recipe and a duplicate
  slug are errors, and any of them stops startup. This used to swallow read
  errors instead, so a broken recipe presented as a shape Builder had never
  been given and nobody learned why the agent came out different.
- A record with no id or no name, a record with no persona, a persona with no
  record, a persona placed in `orchestrator_prompt` instead of its section, a
  template with no record, and `rules_required` with no rules are all errors.

## Adding one

Drop a new `.md` file here. `README.md` is the one reserved name. That is the
whole change: the shape appears in `archetype(list)`, ships an agent if it
declares a record, joins the wizard row if it declares a template, and every
agent built from it follows it and can be detached.

Keep ids stable. Live agents record the shape they follow, `seed-<something>`
ids are compared by name in dozens of places, and other apps dispatch some of
them by literal id, so renaming one is a code change rather than a file edit.
