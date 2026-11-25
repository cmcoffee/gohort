# Querying an app's records

Status: steps 1 to 5 built (0.9.699, 2026-11-25); step 6 (indexes) waits on a measurement.

## Why

An app's data is its records, and today every reader gets all of them:

- The page calls `app.records.list()` and gets every record this person
  has, oldest first. A page showing the last 20 log entries downloads all
  of them, every time anything changes (`app.onChange`).
- A script gets every record on every run, as the `records` environment
  variable, or as a file once they pass 100 KB (fixed in 0.9.500; before
  that a large app's scripts read none). A data source answering "the
  entries for this week" parses the whole set to keep a few.
- Records are stored one table per app (`recTable(slug)`, in the person's
  store or the app's own `private_db` file) and read whole
  (`appscript.ReadRecords`).

That is fine for a habit tracker and wrong for what people now port: an app
that had a SQLite database, a log, an inventory, thousands of rows. Porting
one means turning its tables into kinds of record (the folder guide says
so), and then every query it made becomes a full read.

## What it would be

One query shape, the same for the page and for scripts, read by the host:

```
{"where": {"kind": "entry", "status": "open", "day": {">=": "2026-11-10"}},
 "order": "-created",
 "limit": 50,
 "after": "<the last key of the page before>"}
```

- `where`: field equals value, or one comparison per field (`=`, `!=`,
  `<`, `<=`, `>`, `>=`, `in`, `contains` for text). All must hold. No `or`,
  no joins: a join is two queries, or a data source.
- `order`: one field, `-` for newest or largest first. Default: oldest
  first, as now.
- `limit`: at most 500 (default: no limit, as now). `after`: the key to
  continue from, so a long list pages without offsets shifting under it.

Where it is used:

- **Page:** `app.records.list(query)`, and `app.records.count(where)`.
  `app.records.list()` with no query stays exactly as it is.
- **Scripts:** a data source or action declares what it reads in app.json,
  `"reads": {"where": {"kind": "entry"}, "order": "-created", "limit": 200}`,
  and gets those records in place of all of them (the same `records()`).
  A script that declares nothing gets everything, as now. For a query that
  depends on the request (this week, this person), `from gohort import query;
  rows = query({...})` asks the host at run time over the hook socket the
  script already has; the declared form is for the common fixed case and
  costs no round trip.
- **Shared collections:** the same shape on `app.shared(name, query)`.

## How it runs

The host reads the table and filters before anything is sent. kvlite has
no secondary indexes, so the first version still reads the whole table on
the server: what it saves is everything after that (the JSON sent to the
page, the environment or file handed to the script, the script's parse),
which is where the time goes today. If a large app is still slow, an index
per declared `where` field is the second step, kept beside the table and
written on save.

Nothing stored changes shape and nothing is migrated: a record is still a
JSON object in the app's table, and an app that never queries behaves as
it does today.

## Questions to settle first

1. **Declared reads or the hook query, or both?** Both is proposed above.
   Declared alone cannot express "this week"; the hook alone costs a round
   trip on every run and puts the query in the script, where checks cannot
   see it.
2. **A `kind` convention.** Ported apps will have several kinds of record in
   one table. Should `kind` be a recognised field (the guide says to add it,
   the editor groups by it), or is it just another field for `where`?
3. **Limits.** 500 results per page is a guess. A script's records as a
   file are capped at 64 MB today (`maxFileArgBytes`); with queries, that cap
   should rarely be reached, and should be measured against the sandbox.
4. **Checks.** A script check's `records` stand in for the store; with a
   declared `reads`, the check should run the same query over them, so a
   check fails when the query is wrong. Proposed: yes.

## Steps

1. The query shape and its filter, with tests (`core/appscript`).
2. `app.records.list(query)` and `count` in the page.
3. Declared `reads` on data sources and actions, and checks honouring it.
4. `query()` over the hook.
5. Builder's help and the folder guide.
6. Measure a large app; add indexes only if step 1's full read is the
   remaining cost.

## Settled (2026-11-25), as the questions above proposed

1. **Both**: declared `reads` for the fixed case, and `query()` over the hook
   for one that depends on the request.
2. **`kind` is just a field** for `where`, with no meaning of its own.
3. **500** results per page at most.
4. **Checks run the declared reads** over their records.

## As built (0.9.699)

- **The shape and its filter** are `core/recquery`, a leaf package (core's
  exported surface does not grow): `Parse`, `Check`, `Run` (the page, and
  the key to continue after when a limit left more), `Count`. Numbers
  compare by value and anything else as text, so an ISO date orders as a
  date; a missing field fails every comparison except `!=`; `contains` is
  any-case text, or an element of a list; records without the order field
  go last; an `after` that is no longer among the matches is an error, so a
  page that lost its place starts again.
- **The page**: `records?q=<query>` answers `{records, next}`,
  `records?count=<where>` answers `{count}`, and `shared/<name>?q=` the
  same; a list with no query answers as before. `window.app.records.list(
  query)` resolves to the records with `.next` on the list,
  `app.records.count(where)` to a number, `app.shared(name, query)` as
  list.
- **Scripts**: `reads` on a data source or action (`AppDataSource.Reads`,
  `AppAction.Reads`, and in `app.json`), applied by the runner to the
  `records` input before the script starts, so `records()` holds those
  records only. `from gohort import query` asks while it runs, through the
  hook (`query`, granted to every app data source and action), over the
  records of the person the run is for.
- **Checks and verify** run a script's declared reads over their records.
- **Builder's help, the folder guide and the page API reference** say all of
  it. A malformed `reads` is noted on the save and the script is handed
  every record until it is fixed.
- **Still a full read on the server**: kvlite has no secondary indexes, so
  the host reads the table and filters. What is saved is everything after:
  the JSON sent, the input handed to a script, the script's parse.
