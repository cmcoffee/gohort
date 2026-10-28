# README images

Images referenced from the top-level `README.md`:

| file | what it shows | where it appears |
|---|---|---|
| `wordmark-dark.svg`, `wordmark-light.svg` | the logo: the favicon's three squares and GOHORT in the dashboard's block letters, one per GitHub theme (a `<picture>` picks) | the very top, with the same logo as text in a comment below it for raw readers |
| `dashboard-2026-10-18.png` | the dashboard with its apps and one agent card: the "this is a platform, not a bot" shot. Taken from a public-only build (`git archive` export, no private apps) on a fresh instance, a placeholder `admin` account, and a worker model set so the setup notice is absent | under the badges, above **Three ways to think about it** |
| `agent-turn-2026-10-28.png` | one agent turn with its tool trace open: the reply *and* what produced it, in the centred conversation column with the composer below it (mic, read-aloud, Private and Fresh at the left, Attach and Send at the right) | under **What it looks like in practice**, making the status-page story concrete |
| `admin-2026-10-18.png` | the administrator console's System Status: its tabs, and the sandbox it reports | near the end, beside the administrator section |

The logo copies the app's, it is not a separate design: the letter grid is the
dashboard masthead's (`core/dashboard_page.go`), one rect per run of blocks, and
the squares are the favicon's geometry and colours (`core/webui` IconSVG). If
either changes, redraw both SVGs to match, so the README does not drift from the
app.

## Take them from a fresh deployment

Not from a live instance. A running dashboard carries private app names, real
contacts and real conversation content, and a README image lives in git history
- removing one later means rewriting history, not deleting a file.

A fresh `--setup` with a little demo data is the right source, and has a useful
side effect: it exercises the first-run experience a release actually ships.

The 2026-10-18 set: an assistant named Moneypenny made from the Assistant shape,
one question to it ("What time is it right now in Tokyo and in London, and how
many days are left until 25 December?"), its card shown in the dashboard's
Agents section, 1440 px wide at 2x in the dark scheme. A headless browser needs
a colour emoji font (Noto Color Emoji) or the icons draw as empty boxes.

The agent turn was retaken 2026-10-28, the same assistant and question at
1440 x 640, after the composer and the conversation column changed. Two things
that shot needs: the worker pointed at a model server that calls tools (not
another instance's Ollama proxy, which drops tool definitions), and
transcription and speech turned on, or the mic and the read-aloud switch are
absent from the composer. The emoji font came from the distribution's
google-noto-emoji-color-fonts package, unpacked into a private font directory
the headless browser was pointed at (FONTCONFIG_FILE), nothing installed.

## Keep them light and slow-moving

PNG, a few hundred KB each; they land in every clone and in the release archive.
Prefer surfaces whose *shape* is stable (the dashboard grid, a chat thread)
over panels being actively redesigned, so the images age with the product rather
than with the week.

Referenced by relative path so they render on GitHub, in forks, and from the
source tarball.

When an image changes, give it a new filename. GitHub serves README images
through a proxy that caches by URL, so a replaced file under the same name
keeps showing the old picture for a long time.
