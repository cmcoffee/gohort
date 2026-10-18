# Built-in agents

Framework agents that are not shapes. One file each: JSON frontmatter carrying
an `AgentRecord`, then the persona as the markdown body.

Two live here. Builder, whose authoring catalog comes from an identity check
rather than from any record (`builderInternalTools` appends tools that no
`allowed_tools` list can name, and `agentCanAuthor` answers true for it ahead
of the flag), which a shape cannot express. And Research (`seed-research`), a
shape until 0.8.381 and an engine now: hidden, and run by id by Collections
(research and audit) and Scribe (audit and its research tool). It is not an
app agent because the app-agent spec carries no rules, plan guidance, gap
check or round budgets, and Research depends on all four.

A new framework agent is none of these. Builder's shaper composes a specialist
from the request; a shape that ships an agent goes under `../archetypes/`.

## Format

```
---
{ "id": "seed-builder", "name": "Builder", "allowed_tools": ["ask_user"] }
---
You are Builder. ...
```

The frontmatter keys are `AgentRecord`'s own JSON keys
(`apps/orchestrate/types.go`), so the field list in that struct is the field
list here. Two keys behave differently: `owner` is stamped by the loader rather
than read, because a built-in belongs to the framework, and `notes` is free
text the loader discards, because JSON has no comments and the reason a setting
is the way it is belongs beside the setting.

The prompt goes in the body, not in `orchestrator_prompt`. A file that puts it
in the frontmatter is rejected, so there is only ever one place to look.

## Runtime snippets

A body can splice in a fragment the framework resolves at load:

| placeholder | expands to |
|---|---|
| `{{memory_save_call}}` | the memory-save tool's name on the live surface, which the collapsed remember/recall envelope renames |
| `{{sandbox_python_note}}` | a Python compatibility block when the sandbox interpreter predates 3.7, and nothing otherwise |

Expansion runs on every load rather than once at parse, so a prompt tracks the
live flag and the live probe with no restart. A placeholder naming a snippet
that does not exist is an error, because the alternative is shipping
`{{sandbox_pyton_note}}` to the model as prose. An optional snippet carries its
own separator, so write it flush against the text it follows.

## Rules the loader enforces

Unknown frontmatter keys, a missing id or name, an empty body, a prompt placed
in the frontmatter, and two files claiming one id are all errors that stop
startup. A built-in that fails to parse must be loud: the alternative is a
deployment missing an agent, presented as a deployment that never had one.
