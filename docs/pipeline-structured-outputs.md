# Pipeline structured outputs

**Status:** SHIPPED v0.5.549. Related: `core/pipeline_def.go`, `core/pipeline_interp.go`,
`core/pipeline_structured_test.go`, `project_pipeline_framework_future`,
`project_builder_as_pipeline`.

Today a stage's output is a string, and `outputs map[string]string` is the whole
data model (`pipeline_interp.go:80`). Stages thread text into later prompts via
`{stage:NAME}` string substitution. That is enough for "summarize, then rewrite"
and not enough for anything that has to *branch*, *iterate*, or *fan over one
field of a prior result*.

This spec adds one thing: **a stage may declare the shape of its output, and
later stages may reference a single field of it.** Nothing else. `loop`,
`branch`, a `tool` stage kind, and per-stage model tier are follow-on work that
all depend on this landing first — which is why it goes first. Retrofitting
typed values under an already-shipped `loop` means rewriting `loop`.

## Why this is the enabling change

| Wanted primitive | What it needs from this change |
|---|---|
| `loop` with a carry variable | The carry is a named typed value, not a blob of text |
| `branch` / early exit | A predicate has to read *a field* (`frame.reject`), not grep prose |
| `fan_over` a field | Today fanout can only consume a whole stage output that happens to be a bare JSON array |
| `tool` stage | Tool params come from prior fields, and results come back as fields |

## Data model

### `PipelineStage` gains one field

```go
// Output declares the stage's result shape. Empty (the default) = the
// stage returns free text and behaves exactly as it does today. Non-empty
// = the interpreter asks the model for JSON with these fields, validates
// the reply against them, and exposes each field to later stages as
// {stage:NAME.field}.
Output []PipelineField `json:"output,omitempty"`
```

```go
// PipelineField is one declared field of a stage's structured output.
type PipelineField struct {
    Name string          `json:"name"`           // lowercase [a-z0-9_]+; the key in {stage:NAME.name}
    Type PipelineFieldType `json:"type"`         // string | number | bool | list | object
    Desc string          `json:"desc,omitempty"` // rendered into the prompt; this is how the model knows what to put here
    // Fields describes the element shape for type=list and the member
    // shape for type=object. One level only — a nested field may not
    // itself declare Fields. Deeper structure is expressible, just not
    // addressable: it renders as JSON and the consuming prompt deals
    // with it.
    Fields []PipelineField `json:"fields,omitempty"`
    // Required fails the stage when the model omits the field. Default
    // false: an absent optional field resolves to "".
    Required bool `json:"required,omitempty"`
}
```

`Type` is a closed set — `string`, `number`, `bool`, `list`, `object`. A bare
`list` (no `Fields`) is a list of strings, which is exactly what `fan_over` and
`DecodeJSONList` already consume.

**Why a field list and not JSON Schema.** Builder authors these, and a user
edits them in a form. A field list renders to a prompt instruction, to a
validator, and to a UI table without any of them knowing JSON Schema. Full
schema is a bigger surface for less reach at this tier.

### Internal: `outputs` carries both

```go
// stageOutput is one completed stage's result. Text is always populated
// (it is what {stage:NAME} and {prev} render). Fields is populated only
// when the stage declared Output.
type stageOutput struct {
    Text   string
    Fields map[string]any
}
```

`outputs map[string]string` becomes `outputs map[string]stageOutput`. This type
is unexported and lives entirely inside `pipeline_interp.go`; no exported
signature changes. `{prev}` stays a string.

## Templating

`resolveStageTemplate` gains one form:

```
{input}            the pipeline's top-level input          (unchanged)
{prev}             the previous stage's Text               (unchanged)
{stage:NAME}       a named prior stage's Text              (unchanged)
{stage:NAME.field} a named prior stage's declared field    (new)
```

Field rendering:

| Declared type | Renders as |
|---|---|
| `string` | the value verbatim |
| `number`, `bool` | Go default formatting (`%v`) |
| `list`, `object` | compact JSON |

Rendering lists as JSON is deliberate: it keeps them consumable by
`DecodeJSONList` if a downstream stage wants to re-parse, and it round-trips
through a prompt without inventing a second list encoding.

**Implementation stays literal-replace.** `resolveStageTemplate` already does
`strings.ReplaceAll` per known stage name; it adds a second inner loop per
declared field. `{stage:plan}` does not match inside `{stage:plan.queries}`
(the closing brace is part of the literal), so there is no ordering hazard and
no scanner is needed.

**Unknown placeholders are still left untouched.** A typo'd field degrades to a
visible artifact in the prompt rather than a silently blanked one — the
existing rule, unchanged.

## `fan_over` gains field access

`FanOver` accepts `NAME` (today: the whole stage output, parsed as a list) or
`NAME.field` (new: that field, which must be declared `list`).

This is not optional politeness — it is required for structured stages to be
usable with fanout at all. Once a planning stage declares
`{queries: list, calcs: list}`, its `Text` is a JSON *object*, and today's
`DecodeJSONList(src, 0)` would fail on it and fall through to prose-scraping
the JSON. `fan_over: "plan.queries"` is the correct reference.

## Execution

In `executePipelineDef`, for a stage with non-empty `Output`:

1. **Prompt.** Append a rendered contract to the resolved prompt — the field
   list in *declared order* (not map order; the payload must stay
   byte-identical across runs for cache reuse), each with name, type, and
   `Desc`. Ends with an instruction to reply with that JSON object and nothing
   else.
2. **Call.** Add `WithJSONMode()` to the worker's `ChatOption`s.
   `core/llm.go:739` already has it and `llm_openai.go` already maps it to
   `response_format: {"type":"json_object"}`.
3. **Decode.** `DecodeJSON` (`core/search.go:73`) into `map[string]any`. It
   already strips code fences, finds the outermost object, and sanitizes
   trailing commas and stray comments — the three things local models actually
   get wrong.
4. **Validate.** Every `Required` field present and type-compatible. Coerce
   leniently in the obvious directions (a `number` returned as `"42"`, a `list`
   returned as a single string becomes a one-element list) — same posture as
   `parseParamsArg` in `tool_def` (`project_tool_def_param_coercion`), which
   coerces rather than hard-rejecting.
5. **Repair once.** On decode or validation failure, re-run the stage once with
   the failure appended to the prompt. Status line records the attempt.
6. **Fail the stage** if the repair also fails.

`Text` is set to the model's raw reply either way, so `{stage:NAME}` on a
structured stage renders the JSON.

### Why hard-fail rather than fail-open

The framework's default is fail-open with breadcrumbs
(`feedback_guards_leave_breadcrumbs`), and this is the exception. A stage that
declared a shape and did not produce it breaks every downstream
`{stage:X.field}` reference anyway; the degrade path would be a prompt
containing the literal text `{stage:plan.queries}` three stages later. Failing
at the source, after one repair attempt, is the debuggable behavior. The
breadcrumb rule is still honored — both the repair and the failure land on the
status line.

**Agent stages get the same treatment.** An agent stage that declares `Output`
gets the contract appended to its dispatch prompt and its reply decoded the
same way. It cannot get `WithJSONMode` (the dispatch hook owns the call), so it
relies on the prompt contract plus `DecodeJSON`'s leniency plus the repair
retry. Worth stating explicitly so the behavior is not a surprise.

## Validation (`PipelineDef.Validate`)

New checks:

- Stage names must not contain `.` — otherwise `{stage:a.b}` is ambiguous
  between a stage named `a.b` and field `b` of stage `a`.
- Field names: non-empty, unique within a stage, `[a-z0-9_]+`.
- A nested `PipelineField` may not itself declare `Fields` (one level).
- `Type` is one of the five known values.
- `fan_over: "NAME.field"` — `NAME` is an earlier stage, `field` is declared on
  it, and its type is `list`.
- **`{stage:NAME}` and `{stage:NAME.field}` references in prompts resolve** to
  an earlier stage, and the field is declared.

That last one is the quiet win: a typo'd reference becomes a save-time error
instead of a prompt that silently carries a literal brace expression into an
LLM call.

> Note a pre-existing gap this closes. `Validate`'s doc comment already claims
> it checks "`{stage:NAME}` references point at earlier stages (no forward refs
> or cycles)" — the implementation never did; it only checks `FanOver`. The
> comment is aspirational today. This change makes it true.

## Compatibility

- `Output` empty = today's code path, byte for byte. No `WithJSONMode`, no
  decode, no validation. **No existing pipeline changes behavior.**
- New `PipelineDef` field is `omitempty`; old defs deserialize unchanged and
  `ExportPipeline` / `ImportPipeline` need no changes.
- `outputs` type change is confined to `pipeline_interp.go`
  (`executePipelineDef`, `runFanoutStage`, `resolveStageTemplate` — all
  unexported).
- `apps/orchestrate` touches: the pipeline editor UI and `pipeline_def_tool.go`
  need to accept and round-trip `output`. Per
  `reference_temptool_update_roundtrip`, wire **both** the create and the update
  paths, and have the parser accept native types — dropping the field on update
  is the exact bug that shape has produced before.

## Out of scope

`loop`, `branch`, a `tool` stage kind, per-stage model tier, nested field
addressing (`{stage:plan.calcs.0.expr}`), and streaming a structured stage.
Each is a separate change on top of this one.

## Files

| File | Change |
|---|---|
| `core/pipeline_def.go` | `PipelineField`, `PipelineFieldType`, `PipelineStage.Output`, `SplitStageRef`, rewritten `Validate` |
| `core/pipeline_interp.go` | `stageOutput`, `outputs` type, contract rendering, decode + coerce + repair, `{stage:NAME.field}`, `fanoutItems` |
| `apps/orchestrate/pipeline_def_tool.go` | parse `output` on create **and** update; tool description + help text |
| `core/pipeline_structured_test.go` | validator, decoder, templating, fan_over, and an all-agent end-to-end run |

`apps/orchestrate/pipelines_http.go` needed no change: it decodes the request
body straight into a `PipelineDef`, so the new field rides along. There is no
field-by-field stage editor in the web UI to update — pipelines are authored
through the `pipeline` tool or as raw JSON.

## Bugs this closed on the way past

Two silent-drop bugs, both found by touching the code rather than by anything
reporting them:

- `parsePipelineStages` never read `tools` or `think`, though the `pipeline`
  tool has documented both for as long as they've existed. Builder could pass
  them, they parsed clean, and they vanished. Exactly the shape
  `reference_temptool_update_roundtrip` warns about, on a different tool.
- `Validate` registered a stage before checking its `fan_over`, so
  `fan_over: <self>` passed validation and failed at run time instead. Its doc
  comment also claimed a `{stage:NAME}` forward-reference check that was never
  implemented.
