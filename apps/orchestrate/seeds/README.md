# Seed agents

One file per seed agent. Each is a persona document with a settings header,
embedded into the binary and loaded at startup by `seeds_file.go`.

## Format

JSON frontmatter between `---` fences, then the agent's orchestrator prompt as
the markdown body:

```
---
{
  "id": "seed-kb",
  "name": "Knowledge Base",
  "allowed_tools": ["ask_user"],
  "max_worker_rounds": 6
}
---
You are a knowledge-base assistant. ...
```

The frontmatter keys are `AgentRecord`'s own JSON keys (`apps/orchestrate/types.go`),
so the field list in that struct is the field list here. JSON rather than YAML
because those tags already exist, and because a YAML parser would be a new
external dependency in a tree that also builds in GOPATH mode.

Two keys behave differently from the rest:

- **`owner`** is stamped by the loader, not read. A seed belongs to the
  framework.
- **`notes`** is an object of free text that the loader reads and discards. JSON
  has no comments, and the reason a setting is the way it is belongs next to the
  setting.

The prompt goes in the body, not in `orchestrator_prompt`. A file that puts it
in the frontmatter is rejected rather than quietly accepted, so there is only
ever one place to look for it.

## Rules the loader enforces

- Unknown frontmatter keys are an error. A misspelled key would otherwise drop a
  setting the file plainly asks for, without a word.
- A missing id, a missing name, or an empty body is an error.
- Two files claiming the same id is an error.
- Any of those errors stops startup. A seed that fails to parse must be loud:
  the alternative is a deployment that is missing an agent presented as a
  deployment that never had one.

## Adding one

Drop a new `.md` file here. `README.md` is the one reserved name; everything
else ending in `.md` is read as a seed, in filename order.

Keep ids stable. `seed-<something>` ids are compared by name in dozens of places
(`isSeedID`, the wizard templates, the scope pill, clone gating), so renaming an
id is a code change, not a file edit.

## Runtime snippets

Two of these prompts are not fixed text, so a body can splice in a fragment the
framework resolves at load time:

| placeholder | expands to |
|---|---|
| `{{memory_save_call}}` | the memory-save tool's name on the live surface, which the collapsed remember/recall envelope renames |
| `{{sandbox_python_note}}` | a Python compatibility block when the sandbox interpreter predates 3.7, and nothing otherwise |

Expansion runs on every load rather than once at parse, so a prompt tracks the
live flag and the live probe with no restart. A placeholder naming a snippet
that does not exist is an error, because the alternative is shipping
`{{sandbox_pyton_note}}` to the model as prose.

An optional snippet carries its own separator, so write it flush against the
text it follows (`…might be true.{{sandbox_python_note}}`) rather than after a
blank line. An empty expansion then adds nothing at all.

This is a substitution table for facts the framework knows about itself, not a
template language. A seed that wants to compute something is a seed that
belongs in Go.
