# Co-author: select, ask, review, apply

Status: planned 2026-10-29. Phases 1 to 3 built and checked in headless Chromium (desktop and
390px), including a Builder-built editable workbench. Phase 4's missing primitives (files a
record keeps, document actions) built; the song app built by Builder against a live model is
next, on the live server. Phase 5 done. Not yet: FormPanel text fields (phase 2).

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

- **The highlight is what the chat is about.** A selection the person makes in the document
  pins it: a chip above the chat's input ("Highlighted: “Chorus...”, 4 lines") with the app's
  quick actions beside it (`opts.actions`, each a label and the words it sends at once). Moving
  to the chat to type keeps it; clicking back into the document with nothing selected lets it
  go. (It began as a bar beside the selection with Ask: on a phone that bar sat where the
  system's Cut / Copy / Paste goes, and Ask was a step the highlight already said.)
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

1. **Core primitives.** `uiCoauthor` (highlight, pin, chip, send context), `text_proposal`
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

   Walking the app through Builder's section kinds found two things it could not say:

   - **A take had nowhere to live.** `generate_music` leaves an mp3 in the agent's working
     folder and the chat plays it, but a custom app's record holds JSON only, so the take
     stayed in a conversation that is not even per song. Now a workbench names a
     `files_field` (`takes`): each record keeps files in a folder of its own
     (`core/appassets/record_files.go`, under `data/app_record_files`, 64 MiB a file, 40 a
     record, audio/video/pictures/PDF only, checked by their first bytes), listed on the
     record as `[{name, title, type, size, added}]`. The agent gets `keep_file(file, title)`
     to keep what it made on the open record. The viewer lists them under the document, the
     editor open or not, and plays them in place. The list is the server's: a form's
     whole-record save and the editor's merge keep it as stored, so neither drops it nor
     points it at another record's files. Deleting the record, or the app, deletes its
     files. Fields on the record, not a takes table: a take cannot exist without its song.
   - **No Render button.** `WorkbenchPanel.ViewerActions` of kind `compose` already put a
     prompt in the chat; Builder could not declare one. Now `document_actions`
     [{label, prompt}].

   Left to the agent, not the app: the scansion check (the lyric agent's own tool, or its
   own counting) and which music tool renders. The chat is still one conversation across
   songs; every send carries the open song, so the agent works on the right one.

   Not covered: a record's files do not travel with a records export or import.

   Later the same night: the person preferred Builder's hand-built song page to the workbench it
   was rebuilt as, so the workbench is an option again, not the rule. What the page lacked was a
   place for takes, and any app's records can now keep files: a page uses app.files, and an action
   script keeps what it made by printing files:[{record, file, title}] (the list is the record's
   _files when no workbench names one). A rendered document its agent edits its own way (Scribe)
   takes highlighting too: WorkbenchPanel.HighlightAsk / uiHighlightContext pin the highlighted
   text into the chat with no proposals, and agenthost.HighlightContext tells the turn.

   And a take the app's agent renders reaches the script: run_agent returns what the run's
   tools made or attached as reply.files, copied into agent_files/<run>/ in the script's folder,
   and an action's files reply moves them onto the record. Unclaimed runs clear after a day.
5. **Fold the copies in.** CodeWriter's `editor_proposal` becomes a `text_proposal`; the agent
   page's change list, casefile's proposal card and the dead ArticleEditor chat move onto the
   review or are deleted.

   Done:

   - **CodeWriter** has one review. `propose_code` (the whole file) now emits a `text_proposal`
     with an empty `find`, reviewed under the editor like a passage, with Apply, Reject and Undo.
     Its own diff pane (`uiProposeEdit` over `editorShowDiff`) is gone, and so is
     `editorShowDiff` itself, which nothing else called. An `editor_proposal` stored in a
     conversation from before draws as the `text_proposal` card.
   - **The agent page's ✨ dialog** keeps its ticked list, because a proposal there spans several
     fields applied in one PATCH, but each change now shows as the same line diff, the field as it
     is now against the new value, instead of the new value alone.
   - **ArticleEditor** is deleted, with its chat (`buildDocChat`) and the styles only it used. No
     app had built one since the prompt-block editor went.
   - **Casefile's proposal card** stays. It proposes routing tags and a classification for an
     intake item, edited as a form: nothing in it is a text change to diff.
