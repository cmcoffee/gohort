# Co-author: select, ask, review, apply

Status: planned 2026-10-29. Phases 1 to 3 built and checked in headless Chromium (desktop and
390px), including a Builder-built editable workbench. Not yet: FormPanel text fields (phase 2),
the song app built by Builder against a live model (phase 4), folding the copies in (phase 5).

## Why it exists

Talking to an agent about a document and then applying what it proposes works in one place:
CodeWriter. Its agent calls `propose_code` with the whole new file, an `editor_proposal` block
opens a diff against the editor, and Apply writes the textarea. Everywhere else the same idea was
rebuilt or is missing:

- The diff (`core/editor/static/diff.js`) is not in the runtime. An app injects it by hand
  through `ExtraHeadHTML`, and the block renderer only works inside `.ui-cw`.
- ✨ Assist (`core/assist`) is the generic proposal protocol on the server, but its review shows
  versions to step through (draft) or fills fields with Undo (form). Neither shows old against new.
- The agent page's "apply selected changes" list, casefile's proposal card and the orphaned
  ArticleEditor chat are each their own copy.
- Nothing lets a person point at part of a document. The whole file goes with every turn and the
  proposal is the whole file back, so "tighten the second verse" is answered by rewriting
  everything and diffing it.
- Builder knows none of it. No `app_def` section kind can mount an editor with a co-author, so a
  "chat, review the diff, apply" app cannot be built without hand-written JS.

The app that prompted this is a song builder: lyrics in an editor, a chat with the lyric agent
beside it, select the chorus with the mouse and ask for it to scan better, see the diff of just
those lines, apply it, render the song.

## What it is

Four primitives, none of which knows what a song is.

### 1. The editable

A text area (or the code editor) becomes a co-authored document with `uiCoauthor(textarea, opts)`.
It reads the browser's own selection: a mouse drag, a double-click, shift-click, or the long-press
handles on a phone. There is no select mode.

- **Selection bar.** A selection inside the document shows a small bar beside it, the way a
  copy/cut menu appears: **Ask**, plus the app's quick actions (`opts.actions`, each a label and
  the words it sends). Nothing appears for an empty selection.
- **Ask** pins the selection: a chip above the chat's input ("Chorus, 4 lines") and the input
  focused. A quick action pins it and sends its words at once.
- **The pin is what the turn is about.** It rides the send through the host's existing
  `uiSendContext` as `selection: {text, start, end}`. Clearing the chip, or applying a proposal,
  unpins it; the next turn is about the whole document again.

### 2. The proposal

One block, `text_proposal`, and one tool that emits it, `agenthost.ProposeEditTool`:

- `find`: the exact passage being replaced, copied from the document. Empty means the whole
  document.
- `replace`: what goes there.
- `summary`: one line.

A passage rather than offsets because a model copies text reliably and counts characters badly,
and because the passage is also the staleness check: if the person edited those lines since,
`find` is no longer there and Apply refuses ("that passage changed since: ask again") instead of
writing over their edit. When `find` occurs more than once, the occurrence nearest the pinned
selection is the one meant.

`agenthost.SelectionContext` writes the pinned selection into the turn the way CodeWriter writes
the file, with the instruction to answer about that passage and propose a replacement for it, not
the whole document.

### 3. The review

The proposal opens where the passage is: the document scrolls to it and a review panel shows the
diff of the passage only, old against new, with **Apply** and **Reject**. Apply writes it and keeps
an **Undo** until the next edit. A proposal for the whole document shows the whole-document diff,
as CodeWriter's does now. One review is open at a time; a newer proposal replaces it, and its card
in the conversation can reopen it.

The diff moves into the runtime: `core/ui` ships `core/editor`'s diff with every page, so no app
injects it.

### 4. The chat's composer handle

The chat panel gives its host a handle on its own composer, rather than the page-global
`uiRegisterComposerAction`: `panel.uiComposer = {insertText, send, focus, setChip, clearChip}`. The
editable finds the chat beside it through the host element they share.

## What it is not

- Not a server-side apply. A proposal changes nothing until the person applies it in the browser;
  their own Save persists it, as CodeWriter's does today.
- Not song-aware. Section headings, syllable counts and rendering live in the song app and its
  agent's tools.
- Not a second assist endpoint. ✨ Assist's draft and fields replies keep their shapes; phase 2
  gives them the same review.

## Phases

1. **Core primitives.** `uiCoauthor` (selection bar, pin, chip, send context), `text_proposal`
   renderer and range review with Apply / Reject / Undo, the composer handle, the diff in the
   runtime, `agenthost.ProposeEditTool` and `SelectionContext`. Runtime JS harnesses and Go tests
   for each.
2. **First consumers.** CodeWriter adopts it: selection-aware turns, `propose_edit` beside
   `propose_code`, its diff injection removed. `FormPanel` textarea fields get the same selection
   bar through ✨ Assist, and Assist's draft review shows the diff.
3. **Builder reach.** The workbench section's `editable: true` and `selection_actions`: the
   open item is an editor saved by merging into the record (`POST record?id=`), every send
   carries the document and the pinned passage, and the agent gets `propose_edit` in place of
   `add_section` and the "Add to" button, which would write under unsaved edits. Revert asks
   before putting the saved text back, and Escape never discards. `AUTHORING.md` and the
   section help describe it.
4. **The song app, built by Builder.** Lyrics document with section headings, the lyric agent as
   co-author with its scansion tool, Render to `generate_music` keeping each take as a titled
   version. Whatever Builder cannot express is the next primitive to fix, not app-specific code.
5. **Fold the copies in.** CodeWriter's `editor_proposal` becomes a `text_proposal`; the agent
   page's change list, casefile's proposal card and the dead ArticleEditor chat move onto the
   review or are deleted.
