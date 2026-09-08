# README images

Two images, referenced from the top-level `README.md`:

| file | what it shows | where it appears |
|---|---|---|
| `dashboard.png` | the dashboard with its apps — the "this is a platform, not a bot" shot | under the badges, above **Three ways to think about it** |
| `agent-turn.png` | one agent turn with its tool trace visible — the reply *and* what produced it | under **What it looks like in practice**, making the status-page story concrete |

## Take them from a fresh deployment

Not from a live instance. A running dashboard carries private app names, real
contacts and real conversation content, and a README image lives in git history
— removing one later means rewriting history, not deleting a file.

A fresh `--setup` with a little demo data is the right source, and has a useful
side effect: it exercises the first-run experience a release actually ships.

## Keep them light and slow-moving

PNG, a few hundred KB each; they land in every clone and in the release archive.
Prefer surfaces whose *shape* is stable — the dashboard grid, a chat thread —
over panels being actively redesigned, so the images age with the product rather
than with the week.

Referenced by relative path so they render on GitHub, in forks, and from the
source tarball.
