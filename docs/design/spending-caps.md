# Per-person spending caps

Agreed 2026-09-26 (paid API calls, monthly caps), revised and built
2026-11-19 on branch spend-caps once keys and MCP made unattended spending the
larger exposure.

## What counts

Everything priced, charged to the person it was done for:

- model use, at the administrator's tier rates (Cost tab, Prices); a model
  priced at 0 (a local worker) costs nothing and charges nothing;
- each call through a priced credential (CostPerCall) or connector, recorded
  where the per-source ledger already records it (RecordExternalCostFor).

Work no person stands behind is not charged and never capped.

## Who pays

Rides the context (costledger.WithSpender), set where work starts for
someone. The first person and key set stay; the last agent set is the one
doing the work.

- A web request: the signed-in person, else the owner of the key it carries
  (a personal API key also names the key; a desktop or bridge key only its
  owner).
- Orchestrate runs (CreateCancellable): the run's user and agent; a chat
  turn carries its request's payer across to its detached context, then the
  turn's person and agent; a background task its origin's.
- A core pipeline run: the person who started it.
- Servitor: the session's person; filing after a turn, the session's person.
- A credential call made with no payer on its context: its session's person.

## Caps

Monthly, a calendar month in the deployment's zone (time.Local). Zero is no
cap; none is set until the administrator sets one.

- Administrator: a default for everyone, an override per person (Cost tab,
  Spending by person). Administrators are capped like everyone else.
- The person: a lower cap of their own (Account, Spending); above the
  administrator's, the administrator's holds.
- A key: its owner's cap per API key (Account, Spending), which stops that
  key without stopping the person.
- The app Ask caps (per app per day) stay; whichever is lower stops first.
- Credentials keep their own per-day call cap; no separate monthly one.

## Enforcement

- A turn starting over a cap is refused, the model never called, and the
  reply says which cap and what was spent.
- A turn that crosses one mid-way finishes on the local worker, as the
  per-agent daily allowance does.
- A priced credential call that would cross one is not sent.
- Crossing 80% of a person's cap puts a notice on their bell, once a month.

## Stored

In core/costledger (core is at its export ceiling): table person_spend,
"<YYYY-MM>|<person>" holding the month's total and the same split by agent,
key and source; table spend_caps for the default, overrides, own caps and key
caps. Nothing earlier is backfilled; no migration.

## Not covered

- Search and image calls priced per call in the rates are counted for the
  deployment but not charged to a person: their counters carry no context.
- A background task started by a key-driven turn is charged to the person,
  not the key.
