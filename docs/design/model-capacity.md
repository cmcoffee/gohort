# Model capacity: requests at once, hand-off fan-out, Performance Tune

Status: proposed (2026-11-12). Nothing built yet; the measurement that
informs it shipped in 0.9.331.

## Why

Running work in parallel pays only as far as the model behind it can take
it. A vLLM server batches eight or more requests at once; llama.cpp takes as
many as it was started with (`--parallel N`); a hosted API takes many. Today
the settings that decide how much runs at once do not know about each other,
and the numbers that pace long work (timeouts, when a tool call moves to the
background) are guesses set by hand.

## What exists (verified in code)

| What | Where | Default | Governs |
|---|---|---|---|
| Parallel sibling dispatch lanes | tunable `tune_dispatch_fanout_lanes` (orchestrate) | 1 | how many `agents(run)` hand-offs from one model reply run at once; distinct targets only |
| Ollama max parallel | Models, Local Model Scheduler | 1 | one queue for every Ollama request |
| llama.cpp / vLLM max parallel | same | 1 (8 for vLLM) | one queue for every llama.cpp and vLLM request, peer-backed tiers included (`core/ollama` scheduler, `AcquireLlamacppSlot`) |
| A model on its own server | `LLMProviderConfig.SeparateServer` | off | takes no slot at all: no queue, no cap |
| A round's tool calls | `core/agent_loop.go`, the lanes | parallel | all at once, unless a call in the round writes or a tool declares no capabilities, which runs the round in order; since 0.9.331 each such round says so in the log |

So: hand-offs default to one at a time even where the model would take
eight; every local server shares one number whichever model a request is
for; and a separate server has no limit at all.

## Part 1: requests at once, per model

- Each model connection (worker, lead, Offload model, System One, and each
  tier lent by a peer) gets **Requests at once**.
- Defaults by backend: vLLM 8, llama.cpp 1, Ollama 1, a hosted API 8 (its
  rate limits are the provider's; gohort's number only bounds a burst).
- The queue is per server, keyed by endpoint: two models on one server share
  its queue, as they share its slots; models on separate servers no longer
  queue behind each other. A separate server gets a queue of its own instead
  of none.
- No migration. Until a model has a value of its own, the existing Local
  Model Scheduler number for its backend is its value, so nothing changes on
  upgrade. The scheduler section then shows each model's value with a link to
  where it is set.
- Background work still yields to people as it does now (Background work
  yields to people); that decision (2026-11-02) is not reopened here.

## Part 2: hand-off fan-out

- `tune_dispatch_fanout_lanes` defaults to 4 instead of 1: hand-offs to
  distinct agents from one reply run together, up to four. Each model's queue
  holds back what it cannot take, so a single-slot llama.cpp simply serves
  them in turn.
- Two hand-offs to the same agent stay one after another, as now: they share
  a thread and would overwrite each other's continuity.

## Part 3: Performance Tune

A button under Models that measures each model and suggests settings from
what it found. It suggests; the owner applies.

What it measures, per model:

- **Requests at once.** Short requests of a fixed size (a prompt of about 500
  tokens, a reply capped at 64) sent 1, 2, 4, 8 and 16 at a time. Total
  throughput rises with each step until the server is full, then stops while
  each request just takes longer. The suggestion is the largest step whose
  throughput still rose by at least 15% over the one before, with each
  request no slower than twice its time alone; capped by the server's own
  slot count where it reports one (llama.cpp `/props`).
- **Speed.** How fast it reads a long prompt (about 8,000 tokens) and how
  fast it writes.
- **Prompt cache.** The same long prompt sent twice: whether the second is
  markedly faster.
- **Context size.** Read from the server where it reports one (vLLM
  `/v1/models`, llama.cpp `/props`), else left as configured.

What it suggests from that:

| Setting | From |
|---|---|
| Requests at once (Part 1) | the throughput knee |
| Request timeout | the time to read the largest prompt the context allows, plus a long reply at the measured speed, with margin |
| Detach long tool calls after | left as is, shown with the measured speeds beside it (a tool's time is not the model's) |
| Long-thread trimming | informational: whether the cache reuses a long shared prefix |

How it runs:

- A maintenance pass beside the check tests on the Models page: a spinner,
  how far it has got ("model 2 of 3, 8 at once"), Stop, and a page arriving
  mid-run rejoins it. The result is kept, with a Copy report like the check
  tests'.
- **Quiet first.** Each step waits until nobody is using the model; a step a
  person's request landed in is run again once, and marked if it was
  disturbed again.
- **Cost.** Local and peer models run freely. A hosted API shows the tokens
  the run would use and its price first, and runs only on an explicit OK.
- **Peers.** A peer's model is measured as this machine reaches it, and the
  report says so: its server's own settings are its owner's.
- **Results as suggestions.** A table of setting, current value, suggested
  value and the measurement behind it, with Apply for a row and Apply all.
  Nothing is changed without that.
- **Stale.** A model whose endpoint or model id changed since its last tune
  is marked as needing one; nothing re-tunes on its own.

## Building it, in order

1. Per-model Requests at once and per-endpoint queues (`core/ollama`
   scheduler keyed by endpoint; the value on each model's config, defaulting
   to the backend's scheduler field).
2. The hand-off fan-out default.
3. Performance Tune: the probe runner in core (a subpackage, `core/modeltune`,
   so core does not grow), the maintenance pass and report in `apps/admin`.

## Tests

- Two models on one endpoint share a queue of the smaller value; two on
  separate endpoints do not wait on each other; a model with no value of its
  own takes its backend's.
- Hand-offs to distinct agents run together up to the ceiling; to one agent,
  in turn.
- The tune finds a knee from recorded timings (a fake server that slows past
  N at once), caps it by the server's reported slots, skips a hosted API
  without consent, re-runs a disturbed step, and applies nothing until Apply.

## Open

- Hosted APIs: a default of 8, or no gohort limit at all.
- Whether Apply all should leave out the request timeout, which has the
  widest effect.
- Whether the round measurement (0.9.331) shows ordered rounds costing more
  than the model limits do; if so, staging a round's reads around its writes
  comes before Part 1.
