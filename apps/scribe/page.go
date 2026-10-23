// The Scribe workbench page: document list (left) | rendered HTML document
// (center: the guide, with its header image and, once it is long enough, a
// table of contents) | Guide Author chat (right). Built from the core/ui WorkbenchPanel
// primitive; the document styling rides in via the page head so a document
// reads like a formatted page.
package scribe

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func (T *Scribe) servePage(w http.ResponseWriter, r *http.Request) {
	wb := ui.WorkbenchPanel{
		// Left — guide list + New.
		ListURL:   "guides",
		ItemKey:   "id",
		ItemLabel: "title",
		ListTitle: "Documents",
		ListEmpty: "Nothing yet: create a guide.",
		DeleteURL: "guide?id={id}",
		NewButton: ui.ModalButton{
			Label: "New",
			Title: "New document",
			Body: ui.FormPanel{
				PostURL:     "new",
				SubmitLabel: "Create",
				Fields: []ui.FormField{
					{Field: "title", Label: "Title", Type: "text", Placeholder: "e.g. Getting Started with Kubernetes"},
					{Field: "subtitle", Label: "Subtitle", Type: "text", Placeholder: "Optional one-line description"},
					{Field: "template", Label: "Start from", Type: "select", Options: templateOptions(),
						Help: "A starting skeleton for a document people write repeatedly."},
				},
				Invalidate: []string{"guides"},
			},
		},
		// Import brings an exported page back in as a new document — it CREATES
		// one, which is why it sits beside the other control that creates one
		// rather than in the bar that acts on whatever is open.
		//
		// Library-scoped, so it does not grey out with nothing selected. That
		// gating made it unclickable on an empty library, which is precisely the
		// library you would be importing into.
		ListActions: []ui.WorkbenchAction{
			// Both create a new document; neither touches the open one, so one
			// library menu holds them.
			{Label: "Import", Kind: "menu", Scope: "library", Children: []ui.WorkbenchAction{
				{Label: "HTML page", Kind: "client", URL: "scribe_import"},
				// A page already published somewhere (Confluence, one of your
				// publishing targets), taken in so the guide keeps that page
				// rather than publishing a second one (page_import.go).
				{Label: "A published page", Kind: "client", URL: "scribe_import_page"},
				// The lossless door: a bundle (from Export > Bundle, or a whole
				// account export) through the shared preview-then-import flow.
				{Label: "Bundle", Kind: "client", URL: "scribe_import_bundle"},
			}},
			// The house style the Guide Author writes under. It is about every
			// document, not the open one, so it sits with the library controls
			// rather than among the per-document buttons, where it read as one of
			// them.
			{Label: "Rules", Kind: "client", URL: "scribe_rules", Scope: "library"},
		},
		// Center — the rendered document (server HTML: title, header image,
		// contents once there are three or more sections, sections).
		RecordURL:  "guide?id={id}",
		BodyField:  "html",
		BodyIsHTML: true,
		// The whole guide as one markdown page, each ## heading a section: the
		// record carries "markdown" only for a guide the user may edit, which is
		// what enables the toggle. "Edit all" because each section also has its
		// own Edit.
		EditURL:    "body?id={id}",
		EditField:  "markdown",
		EditLabel:  "Edit all",
		// Pictures pasted, dropped or picked in the editor: stored with the
		// guide and inserted as markdown (images.go).
		ImageUploadURL: "images?id={id}",
		EmptyIcon:  "📖",
		EmptyTitle: "Nothing selected",
		EmptyHint:  "Pick a document on the left, or create one. Then ask the assistant to draft it, or write it yourself.",
		// Per-document toolbar: preview/export, revision history, freshness audit.
		ViewerActions: []ui.WorkbenchAction{
			// Thirteen buttons were eight. Each menu holds one kind of thing:
			// what the document is and what it is built from, the passes that
			// rewrite it, and the ones that check it.
			//
			// The open document's own name, privacy, writing brief and how
			// findings reach it, with its header image and the material the
			// Author builds from: first, ahead of Edit all, because it is what
			// the document IS, which the rest of the bar acts on.
			{Label: "Document", Kind: "menu", Children: []ui.WorkbenchAction{
				{Label: "Settings", Kind: "client", URL: "guides_settings"},
				{Label: "Header image", Kind: "client", URL: "scribe_image"},
				{Label: "Sources", Kind: "client", URL: "guides_sources"},
				{Label: "Knowledge", Kind: "client", URL: "guides_knowledge"},
			}},
			{Kind: "edit"},
			// The passes that rewrite the document through the Guide Author.
			{Label: "✨ Revise", Kind: "menu", Children: []ui.WorkbenchAction{
				// A macro, and now shaped like one. This was a "report" action: it
				// POSTed a fixed system prompt to a one-shot JSON-mode call, spun,
				// and reordered the sections — a prompt the user could not read,
				// could not adjust, and could not learn from. The Guide Author has
				// list_sections and move_section, so it can do the same work in the
				// conversation, where the instruction is visible and amendable.
				//
				// The first option's text is the old system prompt's rules said as a
				// request, so one click is the behavior this button always had. The
				// second is the behavior it never had ("move troubleshooting up
				// front"), and it costs one more click rather than taxing every use.
				//
				// It SENDS. Seeding the composer and stopping was tried first and got
				// the common case backwards: it made the reader confirm the default
				// every single time to buy an edit they rarely want.
				//
				// No {id}: the chat already carries the open document (SendURL's
				// guide={scope}), so "this guide" resolves server-side.
				{Label: "Reorganize", Kind: "compose", ComposeTitle: "Reorganize this document", ComposeOptions: []ui.ComposeOption{
					{Label: "Into the clearest reading order", Help: "Overview and prerequisites first, steps in sequence, reference and troubleshooting last.",
						Text: "Reorganize this guide into the clearest reading order for someone new to the topic: " +
							"overview and prerequisites first, then setup and steps in sequence, then advanced and reference material, " +
							"with troubleshooting or FAQ last. Only change the order; don't rewrite any section's content."},
					{Label: "Something else", Input: true, Placeholder: "e.g. move troubleshooting up front: that's what people open this for",
						Help: "Say how you want it ordered.",
						// Seeded with the rule the default enforces, so the common
						// edit ("…but keep X first") is a change to one clause rather
						// than a blank page.
						Text: "Reorganize this guide. Only change the order; don't rewrite any section's content. "},
				}},
				// Same conversion, and a safer one than it looks. This already
				// dispatched the Guide Author with the co-author kit — the agent the
				// chat column runs — just in an isolated guide-update session the
				// author never saw. updateSourcesEvery (follow.go) IS that
				// dispatch's prompt.
				//
				// Its Private guard does not come with it, and does not need to.
				// The old runner blocked the network and dropped "research";
				// handleChatSend sets ForcePrivate, strips web_search and fetch_url,
				// drops research, AND filters attached source tools down to
				// read/write caps. A Private guide is MORE contained through the
				// conversation than it was through the button.
				//
				// What genuinely changes is the session: the run now carries the
				// thread's context instead of starting fresh. Usually an improvement
				// (the author has just been discussing the guide); start a new
				// session when it isn't.
				//
				// The second option is the one this button could never do at all: the
				// old endpoint swept every section or nothing, and "only the Install
				// section" meant not using it.
				// Both halves of keeping a guide current from what it is linked
				// to, in one button: guides set to Distill are read in full and
				// sent to the Curator, and the Author checks the sections against
				// the rest (guideUpdateSourcesAction, follow.go).
				{Label: "Update from sources", Kind: "client", URL: "guides_update_sources"},
				// The brief says how the guide is written, and the Author follows it
				// on every edit, but a changed brief reached only the sections
				// written after it. This puts the ones already there through it:
				// presentation only, since what a section says is Update from
				// sources' job. The turn's chat prompt carries the brief.
				{Label: "Edit to the brief", Kind: "compose", ComposeTitle: "Edit to the writing brief", ComposeOptions: []ui.ComposeOption{
					{Label: "Every section", Help: "Revise each section to read the way the brief asks, keeping what it says.",
						Text: "Edit this guide to its WRITING BRIEF as it stands now (it is in your instructions). Change how each section is presented, not what it says.\n\n" +
							"1. Call list_sections to see the guide.\n" +
							"2. For each section in order, read_section it and judge whether it already reads the way the brief asks: its structure, voice, length and formatting.\n" +
							"3. Where it does not, call edit_section with the whole revised body. Keep every fact, step, command, value and citation, and remove only what the brief says to leave out. Add nothing from elsewhere, and do no research.\n" +
							"4. Leave a section that already fits the brief unchanged: don't rewrite for the sake of it.\n\n" +
							"When done, reply with a short bulleted summary of which sections you changed and what about them. If the guide has no writing brief, say so and change nothing."},
					{Label: "Something else", Input: true, Placeholder: "e.g. only Troubleshooting, and make it a checklist",
						Help: "Say which sections, or what to change about how they read.",
						Text: "Edit this guide to its writing brief. Change how it is presented, not what it says: keep every fact, step and citation. "},
				}},
			}},
			// The ones that check it: is it still true, and what the Curator
			// filed into it.
			{Label: "Review", Kind: "menu", Children: []ui.WorkbenchAction{
				{Label: "Audit", Kind: "report", URL: "audit?id={id}", Spinner: "Auditing…", Invalidate: []string{"guides"}},
				{Label: "Curator", Kind: "client", URL: "guides_curator"},
			}},
			// Ahead of the outbound buttons. Reaching for an earlier version is a
			// recovery move — a section is gone and you want it back — so it
			// should not be hunted for at the far end of the toolbar.
			{Label: "History", Kind: "history", URL: "revisions?id={id}",
				PreviewURL: "revision?id={id}&rev={rev}", RestoreURL: "restore?id={id}&rev={rev}"},
			// Who it reaches, then where it goes.
			{Label: "Share", Kind: "client", URL: "guides_share"},
			{Label: "Export", Kind: "menu", Children: exportMenu()},
			{Label: "Publish", Kind: "client", URL: "guides_publish"},
		},
		// The agent writes sections via its tools; re-render the open guide when a
		// chat round finishes.
		RefreshOn: []string{"guides"},
		ActiveURL: "chat/active",
		// Right — the Guide Author chat (endpoints; WorkbenchPanel builds the panel).
		// scribeChat fills in what this literal leaves out: edit and retry on
		// a message, rename, rejoining a running turn after a reload, tool
		// confirmations, question cards and the guard notices, the same as
		// orchestrate's own chat.
		Chat: scribeChat.Panel(ui.AgentLoopPanel{
			// Past conversations. The endpoints behind these three have existed
			// since the app shipped — every conversation with the Guide Author was
			// already being written to the same store orchestrate uses — but the
			// panel never declared them, so the work was unreachable the moment
			// the page was left.
			//
			// As a BUTTON, not a rail: the chat is the third of three columns
			// here and has nothing to give a list. Sessions opens the list,
			// picking one cuts straight to it, and the column is back to being a
			// conversation.
			// {scope} is the document the workbench has open, read at request
			// time. It used to be scoped by a server-side "current" the client
			// POSTed on selection, which is one slot per user: a second tab
			// overwrote it, and the POST is fire-and-forget, so a list fetched
			// just after a switch answered about the PREVIOUS document — which
			// is a Past sessions that is sometimes right and sometimes empty.
			ListURL:      "chat/sessions?guide={scope}",
			LoadURL:      "chat/sessions/{id}",
			DeleteURL:    "chat/sessions/{id}",
			ListTitle:    "Past sessions",
			NewLabel:     "New session",
			ListPosition: "modal",
			// The "⋯" in the list header is built only for a panel that opts
			// into something to put in it, so without this Scribe had no
			// overflow at all where every other chat column has one — and
			// clearing out a year of drafting conversations meant deleting them
			// one at a time. Bulk delete rides DeleteURL, which is declared
			// directly above, so this is the whole wiring.
			//
			// No MarkAllReadURL: these sessions carry no unread state, and the
			// affordance only shows when something is actually unread.
			BulkSelect: true,
			// Carries the open document too: the turn's tools edit THAT guide,
			// and the session is filed under it for Past sessions to find.
			SendURL:   "chat/send?guide={scope}",
			CancelURL: "chat/cancel",
			// Where a mid-flight message goes. A guide is written over a long
			// turn — the author reads a section as it lands and thinks of one
			// more thing — so this is the panel most likely to be typed into
			// while it is busy, and without it that second thought cancelled
			// the first.
			InjectURL:    "chat/inject",
			Markdown:     true,
			LockActivity: true,
			EmptyText:    "Ask me to draft or revise: e.g. \"Add an introduction\", \"Expand the setup section\", or \"Rewrite this as a runbook.\"",
			Placeholder:  "Ask the Guide Author…",
		}),
	}

	page := ui.Page{
		Title:     "Scribe",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "100%",
		Sections:  []ui.Section{{NoChrome: true, Body: wb}},
		// Migrated off a raw ExtraHeadHTML concatenation onto the typed
		// ui.Head builder: the doc CSS rides in as pre-wrapped <style> blobs
		// (.HTML), the section-controls listener as raw JS (.JS), the shared
		// el() helper once (.JS), and each modal as a typed client action
		// (.ClientAction) — the framework assembles the <script> + register
		// calls + readiness guard.
		Head: ui.NewHead().
			HTML(guideDocCSS).
			HTML(guideSectionCtrlCSS).
			CSS(guideKnowledgeCSS).
			CSS(guideSettingsCSS).
			CSS(guideCuratorCSS).
			CSS(guidePublishCSS).
			CSS(scribeImageCSS).
			JS(guideModalElJS).
			JS(guideSectionCode).
			ClientAction("guides_knowledge", guideKnowledgeAction).
			ClientAction("guides_sources", guideSourcesAction).
			ClientAction("guides_settings", guideSettingsAction).
			ClientAction("guides_share", guideShareAction).
			ClientAction("guides_curator", guideCuratorAction).
			ClientAction("guides_publish", guidePublishAction).
			ClientAction("scribe_rules", scribeRulesAction).
			ClientAction("scribe_import", scribeImportAction).
			ClientAction("scribe_import_page", scribeImportPageAction).
			ClientAction("guides_update_sources", guideUpdateSourcesAction).
			JS(ArtifactClientJS).
			ClientAction("scribe_export_bundle", scribeExportBundleAction).
			ClientAction("scribe_import_bundle", scribeImportBundleAction).
			ClientAction("scribe_image", scribeImageAction),
	}
	page.ServeHTTP(w, r)
}

// templateOptions lists the shared document skeletons for the New modal's
// "Start from" select, with a blank first so the default is an empty body.
func templateOptions() []ui.SelectOption {
	out := []ui.SelectOption{{Value: "", Label: "Blank"}}
	for _, t := range MarkdownDocTemplates {
		out = append(out, ui.SelectOption{Value: t.Name, Label: t.Name, Help: t.Description})
	}
	return out
}

// scribeRulesAction opens the shared core/ui Rules editor over this app's
// per-user rules endpoint. The rules are appended to every Guide Author turn.
const scribeRulesAction = `function(ctx){
      if (!window.uiOpenRulesPanel) return;
      window.uiOpenRulesPanel({url: 'rules', noun: 'document the Guide Author writes'});
}`

// scribeImportAction brings an exported HTML page back in as a new guide:
// pick a file, POST it, refresh the list.
const scribeImportAction = `function(ctx){
      var input = document.createElement('input');
      input.type = 'file';
      input.accept = '.html,.htm,text/html';
      input.addEventListener('change', function(){
        var f = input.files && input.files[0];
        if (!f) return;
        var fd = new FormData();
        fd.append('file', f);
        fetch('import', {method:'POST', credentials:'same-origin', body: fd})
          .then(function(r){ if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); }); return r.json(); })
          .then(function(d){
            if (window.uiInvalidate) window.uiInvalidate('guides');
            window.uiAlert('Imported "' + ((d && d.title) || 'guide') + '". It is in the list on the left.');
          })
          .catch(function(err){ window.uiAlert('Import failed: ' + (err && err.message || err)); });
      });
      input.click();
}`

// scribeImportPageAction takes in a page already published somewhere: pick
// where it is, paste its address, and the guide made from it keeps that page
// as its published copy (page_import.go). The read runs as a job, followed
// here with a spinner, its steps and a Stop beside Close.
const scribeImportPageAction = `function(ctx){
      if (!window.uiOpenSimpleModal) return;
      fetch('import/page', {credentials:'same-origin'}).then(function(r){ return r.ok ? r.json() : {places: []}; }).then(function(d){
        var places = (d && d.places) || [];
        window.uiOpenSimpleModal({title:'Import a published page', width:'560px', mount: function(body, dlg){
          var intro = el('p', {class:'guide-kn-intro', text:'Start a guide from a page that is already published, and keep it there: publishing the guide updates that page instead of making a new one, and edits made on the page come back to the guide.'});
          body.appendChild(intro);
          if (!places.length) {
            body.appendChild(el('p', {class:'guide-kn-intro', text:'Nothing you can read a page from is set up yet: connect Confluence, or make a publishing target in Extensions.'}));
            return;
          }
          var pick = el('select', {class:'ui-input'});
          places.forEach(function(p, i){ var o = el('option', {text: p.title + (p.group ? ' (' + p.group + ')' : '')}); o.value = String(i); pick.appendChild(o); });
          var link = el('input', {class:'ui-input', type:'text', placeholder:'https://your-site.atlassian.net/wiki/spaces/OPS/pages/123456/Runbook'});
          var keepRow = el('label', {style:'display:flex;align-items:center;gap:0.5rem;margin-top:0.6rem'});
          var keep = el('input', {type:'checkbox'});
          keepRow.appendChild(keep); keepRow.appendChild(document.createTextNode('Keep it published: update the page whenever the guide changes'));
          var briefRow = el('label', {style:'display:flex;align-items:center;gap:0.5rem;margin-top:0.4rem'});
          var brief = el('input', {type:'checkbox'});
          brief.checked = true;
          briefRow.appendChild(brief); briefRow.appendChild(document.createTextNode('Write its brief from it: what it is for and how it is written, for whoever writes in it next'));
          body.appendChild(el('label', {text:'Where it is'})); body.appendChild(pick);
          body.appendChild(el('label', {text:'Its address', style:'margin-top:0.6rem;display:block'})); body.appendChild(link);
          body.appendChild(keepRow);
          body.appendChild(briefRow);
          var status = el('div', {style:'margin-top:0.8rem;font-size:var(--fs-sm, 0.8rem);line-height:1.5'});
          body.appendChild(status);
          var go = el('button', {type:'button', class:'ui-row-btn primary', text:'Import'});
          var stop = el('button', {type:'button', class:'ui-row-btn', text:'Stop'});
          dlg.addAction(go);
          var frames = ['\u280b','\u2819','\u2839','\u2838','\u283c','\u2834','\u2826','\u2827','\u2807','\u280f'], f = 0, job = null, timer = null;
          function show(j){
            status.innerHTML = '';
            var line = j.done ? (j.ok ? 'Done. ' : 'It did not work. ') + (j.message || '') : frames[f++ % frames.length] + ' ' + (j.now || 'Reading the page') + (j.elapsed > 2 ? ' \u00b7 ' + j.elapsed + 's' : '');
            status.appendChild(el('div', {text: line, style: j.done && !j.ok ? 'color:var(--danger, #c33)' : ''}));
            (j.steps || []).slice(-6).forEach(function(s){ status.appendChild(el('div', {text: s.text, style:'color:var(--text-mute)'})); });
          }
          function poll(){
            fetch('import/page/job?job=' + encodeURIComponent(job), {credentials:'same-origin'}).then(function(r){ return r.json(); }).then(function(j){
              show(j);
              if (j.done) {
                clearInterval(timer); stop.remove();
                if (j.ok) { if (window.uiInvalidate) window.uiInvalidate('guides'); status.appendChild(el('div', {text:'It is in the list on the left.'})); }
                else { go.disabled = false; }
              }
            }).catch(function(){});
          }
          go.addEventListener('click', function(){
            var p = places[parseInt(pick.value, 10)];
            if (!p || !link.value.trim()) { status.textContent = 'Pick where it is and paste its address.'; return; }
            go.disabled = true;
            status.textContent = frames[0] + ' Starting';
            fetch('import/page', {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'},
              body: JSON.stringify({kind: p.kind, target: p.target || '', link: link.value.trim(), keep_published: keep.checked, write_brief: brief.checked})})
              .then(function(r){ if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); }); return r.json(); })
              .then(function(d){
                job = d.job;
                dlg.addAction(stop);
                stop.onclick = function(){ fetch('import/page/stop?job=' + encodeURIComponent(job), {method:'POST', credentials:'same-origin'}); };
                timer = setInterval(poll, 1000); poll();
              })
              .catch(function(err){ go.disabled = false; status.textContent = 'Import failed: ' + (err && err.message || err); });
          });
        }});
      });
}`

// scribeExportBundleAction downloads the open document as a bundle through the
// shared export dialog (core ArtifactClientJS). Only the owner's own documents
// export this way; a shared one keeps the HTML, PDF and Markdown renderings.
const scribeExportBundleAction = `function(ctx){
      var id = ctx && ctx.recordId;
      if (!id) return;
      fetch('guide?id=' + encodeURIComponent(id), {credentials:'same-origin'})
        .then(function(r){ if (!r.ok) throw new Error('HTTP ' + r.status); return r.json(); })
        .then(function(g){
          if (!g.own) {
            window.uiAlert('Only the owner can export this as a bundle. The HTML, PDF and Markdown exports work for everyone it is shared with.');
            return;
          }
          window.gohortArtifacts.exportFlow({type: 'guide', name: id, label: g.title || id});
        })
        .catch(function(err){ window.uiAlert('Export failed: ' + (err && err.message || err)); });
}`

// scribeImportBundleAction brings a bundle into the library through the shared
// preview-then-import flow.
const scribeImportBundleAction = `function(ctx){
      window.gohortArtifacts.importFlow({
        previewURL: '/account/api/artifacts/preview',
        importURL: '/account/api/artifacts/import',
        invalidate: ['guides'],
        subtitle: 'A document lands in your library as your own, shared with nobody, with web research off until you turn it on in Settings. A title you already have is skipped.'
      });
}`

// scribeImageAction manages a guide's header image in a modal: show the
// current one, generate one from the title, paste a URL, or remove it.
const scribeImageAction = `function(ctx){
      var gid = ctx.recordId;
      if (!gid || !window.uiOpenSimpleModal) return;
      var qp = 'id=' + encodeURIComponent(gid);
      fetch('image?' + qp, {credentials:'same-origin'}).then(function(r){ return r.json(); }).then(function(d){
        window.uiOpenSimpleModal({title:'Header image', width:'560px', mount: function(body, dlg){
          if (!d || !d.can_edit){
            body.appendChild(el('p', {class:'guide-kn-intro', text:'Only someone who can edit this guide can change its header image.'}));
            return;
          }
          var preview = el('div', {class:'scribe-img-preview'});
          function showPreview(url){
            preview.innerHTML = '';
            if (url) { var img = el('img'); img.src = url; preview.appendChild(img); }
            else preview.appendChild(el('span', {class:'guide-pub-mute', text:'No header image yet.'}));
          }
          showPreview(d.image_url);
          body.appendChild(preview);
          var status = el('div', {class:'guide-kn-intro'});
          // Working: the shared spinner with seconds, since generating can run
          // a while and a static word cannot be told from a stall.
          function busy(label){ status.textContent = ''; status.appendChild(window.uiLoading ? window.uiLoading(label) : document.createTextNode(label + '…')); }
          var row = el('div', {class:'guide-edit-actions scribe-img-actions'});
          var gen = el('button', {class:'ui-row-btn primary', text:'Generate from title'});
          if (!d.can_generate) { gen.disabled = true; gen.title = 'Image generation is not configured on this deployment'; }
          var urlIn = el('input', {type:'text', placeholder:'…or paste an image URL, or a screenshot'});
          var use = el('button', {class:'ui-row-btn', text:'Use URL'});
          var rm = el('button', {class:'ui-row-btn', text:'Remove'});
          var up = el('button', {class:'ui-row-btn', text:'Upload'});
          function post(payload, label){
            busy(label);
            gen.disabled = true; use.disabled = true; rm.disabled = true; up.disabled = true;
            return fetch('image?' + qp, {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'}, body: JSON.stringify(payload)})
              .then(function(r){ return r.text().then(function(t){ if (!r.ok) throw new Error(t || ('HTTP ' + r.status)); return JSON.parse(t); }); })
              .then(function(res){ status.textContent = ''; showPreview(res.image_url); ctx.refresh(); })
              .catch(function(err){ status.textContent = ''; window.uiAlert('Could not set the image: ' + (err && err.message || err)); })
              .then(function(){ gen.disabled = !d.can_generate; use.disabled = false; rm.disabled = false; up.disabled = false; });
          }
          // A picture of your own: stored with the guide like one pasted into
          // its body, then made the header. From a file, or pasted straight
          // into this dialog (a screenshot on the clipboard).
          function uploadFile(f){
            if (!f || !/^image\//.test(f.type || '')) return;
            var fd = new FormData();
            fd.append('file', f, f.name || 'image.png');
            busy('Uploading');
            fetch('images?' + qp, {method:'POST', credentials:'same-origin', body: fd})
              .then(function(r){ return r.text().then(function(t){ if (!r.ok) throw new Error(t || ('HTTP ' + r.status)); return JSON.parse(t); }); })
              .then(function(res){ post({url: res.url}, 'Saving'); })
              .catch(function(err){ status.textContent = ''; window.uiAlert('Could not upload the image: ' + (err && err.message || err)); });
          }
          up.addEventListener('click', function(){
            var input = document.createElement('input');
            input.type = 'file';
            input.accept = 'image/png,image/jpeg,image/gif,image/webp';
            input.addEventListener('change', function(){ uploadFile(input.files && input.files[0]); });
            input.click();
          });
          body.addEventListener('paste', function(ev){
            var items = (ev.clipboardData && ev.clipboardData.items) || [];
            for (var i = 0; i < items.length; i++) {
              if (items[i].kind === 'file' && /^image\//.test(items[i].type || '')) {
                ev.preventDefault();
                uploadFile(items[i].getAsFile());
                return;
              }
            }
          });
          gen.addEventListener('click', function(){ post({generate: true}, 'Generating'); });
          use.addEventListener('click', function(){ if (urlIn.value.trim()) post({url: urlIn.value.trim()}, 'Saving'); });
          rm.addEventListener('click', function(){
            fetch('image?' + qp, {method:'DELETE', credentials:'same-origin'}).then(function(){ showPreview(''); ctx.refresh(); });
          });
          row.appendChild(gen); row.appendChild(up); row.appendChild(rm);
          body.appendChild(row);
          body.appendChild(el('div', {class:'scribe-img-url'}, [urlIn, use]));
          body.appendChild(status);
        }});
      });
}`

// scribeImageCSS styles a guide's header image and the image modal. Scoped
// under .guide-doc / .scribe-img-* so nothing leaks.
const scribeImageCSS = `.guide-doc-image { display: block; width: 100%; max-height: 320px; object-fit: cover; border-radius: 10px; margin: 0 0 1.4rem; }
.scribe-img-preview { min-height: 4rem; display: flex; align-items: center; justify-content: center; background: var(--bg-2); border: 1px solid var(--border); border-radius: 8px; margin-bottom: 0.8rem; overflow: hidden; }
.scribe-img-preview img { width: 100%; max-height: 220px; object-fit: cover; display: block; }
.scribe-img-actions { display: flex; flex-wrap: wrap; gap: 0.5rem; margin-bottom: 0.6rem; }
.scribe-img-url { display: flex; gap: 0.5rem; }
.scribe-img-url input { flex: 1; padding: 0.45rem 0.6rem; background: var(--bg-0); color: var(--text); border: 1px solid var(--border); border-radius: 6px; }`

// guideDocCSS styles the rendered guide so it reads like a formatted document:
// a centered measure, a contents block, numbered section headings. Scoped under
// .guide-doc so it never leaks into other surfaces. Uses theme tokens for color.
const guideDocCSS = `<style>
.guide-doc { max-width: 760px; margin: 0 auto; padding: 0.5rem 0 3rem; }
.guide-doc-head h1 { font-size: 1.9rem; line-height: 1.2; margin: 0 0 0.3rem; color: var(--text-hi); }
.guide-doc-sub { font-size: var(--fs-lg, 1.02rem); color: var(--text-mute); margin: 0 0 1.4rem; }
.guide-doc-empty { color: var(--text-mute); font-style: italic; padding: 1rem 0; }
.guide-toc {
  background: var(--bg-2); border: 1px solid var(--border); border-radius: 10px;
  padding: 0.9rem 1.1rem; margin: 0 0 2rem;
}
.guide-toc-title { font-size: var(--fs-2xs, 0.72rem); text-transform: uppercase; letter-spacing: 0.06em; color: var(--text-mute); margin-bottom: 0.5rem; }
.guide-toc ol { margin: 0; padding-left: 1.3rem; display: flex; flex-direction: column; gap: 0.25rem; }
.guide-toc a { color: var(--accent); text-decoration: none; }
.guide-toc a:hover { text-decoration: underline; }
.guide-section { margin: 0 0 2.2rem; scroll-margin-top: 1rem; }
.guide-section > h2 {
  font-size: 1.35rem; color: var(--text-hi);
  border-bottom: 1px solid var(--border); padding-bottom: 0.3rem; margin: 0 0 0.9rem;
}
.guide-section-num { color: var(--text-mute); font-weight: 600; margin-right: 0.3rem; }
.guide-section-body { font-size: var(--fs-lg, 0.95rem); line-height: 1.65; color: var(--text); }
.guide-section-body img { display: block; max-width: 100%; height: auto; margin: 0.9rem 0; border: 1px solid var(--border); border-radius: 8px; }
.guide-section-body h3 { font-size: var(--fs-xl, 1.08rem); color: var(--text-hi); margin: 1.3rem 0 0.5rem; }
.guide-section-body h4 { font-size: var(--fs-lg, 0.98rem); color: var(--text-hi); margin: 1.1rem 0 0.4rem; }
.guide-section-body h5 { font-size: var(--fs-md, 0.9rem); color: var(--text-hi); margin: 1rem 0 0.35rem; }
.guide-section-body h6 { font-size: var(--fs-sm, 0.85rem); color: var(--text-mute); text-transform: uppercase; letter-spacing: 0.04em; margin: 0.9rem 0 0.3rem; }
.guide-section-body pre {
  background: var(--bg-0); border: 1px solid var(--border); border-radius: 8px;
  padding: 0.8rem 1rem; overflow-x: auto; font-size: var(--fs-sm, 0.86rem);
}
.guide-section-body code { font-size: 0.88em; }
.guide-section-body :not(pre) > code { background: var(--bg-2); padding: 0.1rem 0.35rem; border-radius: 4px; }
.guide-section-body blockquote {
  border-left: 3px solid var(--border); margin: 0.8rem 0; padding: 0.2rem 0 0.2rem 1rem; color: var(--text-mute);
}
.guide-section-body table { border-collapse: collapse; margin: 0.8rem 0; }
.guide-section-body th, .guide-section-body td { border: 1px solid var(--border); padding: 0.4rem 0.7rem; text-align: left; }
@media (max-width: 700px) {
  .guide-doc-head h1 { font-size: 1.55rem; }
  .guide-doc-sub { font-size: var(--fs-lg, 0.95rem); }
  .guide-toc { padding: 0.7rem 0.85rem; margin-bottom: 1.4rem; }
  .guide-section { margin-bottom: 1.6rem; }
  .guide-section > h2 { font-size: 1.18rem; }
  .guide-section-body { font-size: var(--fs-md, 0.92rem); }
  .guide-section-body pre { font-size: var(--fs-sm, 0.8rem); padding: 0.7rem 0.8rem; }
  /* Let wide tables scroll instead of forcing the page wider than the viewport. */
  .guide-section-body table { display: block; overflow-x: auto; max-width: 100%; }
}

/* Reading an earlier version (History -> View). The whole point of the screen is
   the section that is GONE, so it gets the accent and everything else recedes. */
.guide-rev-banner { background: var(--surface-2, rgba(99,102,241,0.06)); border: 1px solid var(--border); border-left: 3px solid #6366f1; border-radius: 6px; padding: 0.7rem 0.9rem; margin-bottom: 1.4rem; }
.guide-rev-head { font-size: var(--fs-md, 0.9rem); font-weight: 600; }
.guide-rev-sub { font-size: var(--fs-sm, 0.8rem); color: var(--text-mute); margin-top: 0.35rem; }
.guide-rev-list { margin: 0.45rem 0 0; padding-left: 1.1rem; font-size: var(--fs-sm, 0.85rem); }
.guide-rev-list a { color: #6366f1; }
.guide-rev .guide-section { opacity: 0.62; }
.guide-rev .guide-section-gone { opacity: 1; border-left: 3px solid #6366f1; padding-left: 0.9rem; }
.guide-rev-tag { display: inline-block; font-size: var(--fs-2xs, 0.68rem); letter-spacing: 0.04em; text-transform: uppercase; color: #6366f1; font-weight: 600; margin-bottom: 0.3rem; }
</style>`

// guideSectionCtrlCSS styles the inline per-section controls (hover-revealed,
// except Discuss, which is always there), the "+ Add section" button, and the
// empty-state add link.
const guideSectionCtrlCSS = `<style>
.guide-section { position: relative; }
.guide-sec-ctrls {
  position: absolute; top: 0.1rem; right: 0; display: flex; gap: 0.25rem;
}
/* The editing controls appear on hover; Discuss is always there, the way
   into a section for somebody who does not yet know what to change. The
   heading keeps room for it so a long title does not run underneath. */
.guide-sec-ctrls .guide-sec-btn { opacity: 0; transition: opacity 0.12s; }
.guide-sec-ctrls .guide-sec-discuss { opacity: 1; color: var(--accent); border-color: var(--accent); }
.guide-section:hover .guide-sec-ctrls .guide-sec-btn, .guide-sec-ctrls:focus-within .guide-sec-btn { opacity: 1; }
.guide-sec-ctrls + h2 { padding-right: 5.5rem; }
.guide-sec-btn {
  cursor: pointer; background: var(--bg-2); color: var(--text-mute);
  border: 1px solid var(--border); border-radius: var(--btn-radius, 6px); padding: 0.12rem 0.45rem;
  font-size: var(--fs-xs, 0.74rem); font-weight: 600; line-height: 1.4;
}
/* A touch screen gets the framework's compact tap size. */
@media (pointer: coarse) { .guide-sec-btn { min-height: var(--btn-h-sm, 2.25rem); padding: 0.3rem 0.65rem; } }
.guide-sec-btn:hover { color: var(--accent); border-color: var(--accent); }
.guide-sec-del:hover { color: var(--danger); border-color: var(--danger); }
.guide-add-row { margin-top: 1.5rem; }
.guide-add-btn {
  cursor: pointer; background: transparent; color: var(--text-mute);
  border: 1px dashed var(--border); border-radius: var(--btn-radius, 6px); padding: 0.5rem 1rem;
  font-size: var(--fs-sm, 0.85rem); font-weight: 600; width: 100%;
}
.guide-add-btn:hover { color: var(--accent); border-color: var(--accent); }
.guide-add-link { background: none; border: 0; color: var(--accent); cursor: pointer; font: inherit; padding: 0; text-decoration: underline; }
/* A link in a sentence: on a touch screen its hit area grows, not the link. */
@media (pointer: coarse) { .guide-add-link { position: relative; } .guide-add-link::after { content: ""; position: absolute; inset: -0.55rem; } }
.guide-edit-field { display: flex; flex-direction: column; gap: 0.3rem; margin-bottom: 0.8rem; }
.guide-edit-field label { font-size: var(--fs-xs, 0.78rem); font-weight: 600; color: var(--text-mute); }
.guide-edit-field input, .guide-edit-field textarea {
  background: var(--bg-0); color: var(--text); border: 1px solid var(--border);
  border-radius: 6px; padding: 0.45rem 0.6rem; font: inherit; font-size: var(--fs-md, 0.9rem);
}
.guide-edit-field textarea { min-height: 16rem; resize: vertical; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: var(--fs-sm, 0.85rem); }
/* The section editor is where the writing actually happens, so its body field
   takes the whole modal rather than a fixed 16rem box with the rest of the
   dialog empty beneath it. The modal card is already a flex column capped at
   88vh, and its body flexes, so growing means opting in here: min-height is
   kept as the floor for a short viewport, where the body scrolls instead. */
.guide-edit-field.guide-edit-grow { flex: 1 1 auto; min-height: 0; margin-bottom: 0; }
.guide-edit-field.guide-edit-grow textarea { flex: 1 1 auto; min-height: min(24rem, 40vh); }
.guide-edit-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; align-items: center; gap: 0.5rem; margin-top: 0.4rem; }
.guide-edit-hint { margin-right: auto; font-size: var(--fs-xs, 0.78rem); color: var(--text-mute); }
/* Touch devices have no hover, so the hover-revealed section controls would be
   unreachable: keep them visible there, and drop them out of the heading overlap
   onto their own right-aligned row on narrow screens. */
@media (hover: none) {
  .guide-sec-ctrls .guide-sec-btn { opacity: 1; }
}
@media (max-width: 700px) {
  .guide-sec-ctrls { position: static; justify-content: flex-end; margin-bottom: 0.5rem; }
  .guide-sec-ctrls .guide-sec-btn { opacity: 1; }
  .guide-sec-ctrls + h2 { padding-right: 0; }
  .guide-add-btn { padding: 0.6rem 1rem; }
}
</style>`

// guideModalElJS is the shared DOM helper the guides_* client actions call. It
// is emitted once in the ui.Head init block; function declarations hoist, so
// the action handlers (registered before it in the block) resolve el() at
// click time.
const guideModalElJS = `function el(tag, attrs, kids){
  var n = document.createElement(tag);
  if (attrs) for (var k in attrs){ if (k === 'text') n.textContent = attrs[k]; else n.setAttribute(k, attrs[k]); }
  (kids||[]).forEach(function(c){ n.appendChild(typeof c === 'string' ? document.createTextNode(c) : c); });
  return n;
}`

// guideSectionCode wires the inline per-section controls (data-guide-act) via
// one delegated click listener. Injected as-is (its own IIFE + helpers) through
// ui.Head.JS — it registers no client action, so it needs no unwrapping.
const guideSectionCode = `(function(){
  function el(tag, attrs, kids){
    var n = document.createElement(tag);
    if (attrs) for (var k in attrs){ if (k === 'text') n.textContent = attrs[k]; else n.setAttribute(k, attrs[k]); }
    (kids||[]).forEach(function(c){ n.appendChild(typeof c === 'string' ? document.createTextNode(c) : c); });
    return n;
  }
  function refresh(){ if (window.uiInvalidate) window.uiInvalidate('guides'); }
  function jpost(url, body){
    return fetch(url, {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body||{})});
  }
  // Field builder for the edit/add modal.
  function fieldText(label, value){
    var inp = el('input', {type:'text', value: value||''});
    return {wrap: el('div', {class:'guide-edit-field'}, [el('label', {text: label}), inp]), input: inp};
  }
  function fieldArea(label, value, grow){
    var ta = el('textarea'); ta.value = value || '';
    var cls = 'guide-edit-field' + (grow ? ' guide-edit-grow' : '');
    return {wrap: el('div', {class:cls}, [el('label', {text: label}), ta]), input: ta};
  }
  function openEditor(title, t0, m0, onSave, gid){
    if (!window.uiOpenSimpleModal) return;
    window.uiOpenSimpleModal({title: title, width:'min(1100px, 94vw)', mount: function(body, dlg){
      var tf = fieldText('Section title', t0);
      var mf = fieldArea('Body (markdown)', m0, true);
      body.appendChild(tf.wrap); body.appendChild(mf.wrap);
      // Save beside Close in the footer; the picture controls stay under
      // the body they add to.
      var save = dlg.addAction(el('button', {class:'ui-row-btn primary', text:'Save'}));
      // Pictures: paste a screenshot or drop a file into the body, or pick one.
      if (gid && window.uiImagePaste){
        var pics = window.uiImagePaste(mf.input, 'images?id=' + encodeURIComponent(gid));
        var addImg = el('button', {class:'ui-row-btn', type:'button', text:'Add image'});
        addImg.addEventListener('click', function(){ pics.pick(); });
        body.appendChild(el('div', {class:'guide-edit-actions'}, [
          el('span', {class:'guide-edit-hint', text:'Paste or drop a screenshot to add it.'}), addImg]));
      }
      save.addEventListener('click', function(){
        save.disabled = true; save.textContent = 'Saving…';
        onSave(tf.input.value, mf.input.value).then(function(){
          try { dlg.close(); dlg.remove(); } catch(e){}
          refresh();
        }).catch(function(err){ save.disabled = false; save.textContent = 'Save'; window.uiAlert('Save failed: ' + (err && err.message || err)); });
      });
    }});
  }
  // Discuss: the shared assist window in its discuss mode, talking to the
  // guide's Author (section/discuss). The text is kept only when the person
  // uses the draft, through the section's own save.
  function discuss(gp, sp, title, text, save, opening){
    if (!window.uiOpenAssist) return;
    var session = Math.random().toString(36).slice(2) + Date.now().toString(36);
    window.uiOpenAssist({
      mode: 'discuss',
      title: 'Discuss: ' + (title || 'new section'),
      subtitle: 'The guide\u2019s assistant asks first, then writes. Nothing changes in the guide until you use the draft.',
      initial: text,
      ask: opening,
      send: function(req, done){
        jpost('section/discuss?' + gp + sp, {title: title, message: req.message, draft: req.draft, history: req.history, session: session})
          .then(function(r){ return r.text().then(function(t){ if (!r.ok) throw new Error(t || ('HTTP ' + r.status)); return JSON.parse(t); }); })
          .then(function(res){ done(res); })
          .catch(function(err){ done(null, (err && err.message) || String(err)); });
      },
      onAccept: function(m){
        if (!String(m || '').trim()) { window.uiAlert('There is no text to save yet.'); return; }
        save(m).then(function(r){ if (r && !r.ok) return r.text().then(function(t){ throw new Error(t); }); refresh(); })
          .catch(function(err){ window.uiAlert('Save failed: ' + (err && err.message || err)); });
      }
    });
  }
  document.addEventListener('click', function(e){
    var btn = e.target.closest && e.target.closest('[data-guide-act]');
    if (!btn) return;
    var act = btn.getAttribute('data-guide-act');
    var doc = btn.closest('.guide-doc');
    var gid = doc && doc.getAttribute('data-guide-id');
    if (!gid) return;
    var sec = btn.closest('.guide-section');
    var sid = sec && sec.getAttribute('data-section-id');
    var gp = 'guide=' + encodeURIComponent(gid);
    var sp = sid ? '&section=' + encodeURIComponent(sid) : '';
    if (act === 'add'){
      openEditor('Add section', '', '', function(t, m){ return jpost('section/add?' + gp, {title:t, markdown:m}); }, gid);
    } else if (act === 'edit'){
      fetch('section?' + gp + sp, {credentials:'same-origin'}).then(function(r){ return r.json(); }).then(function(s){
        openEditor('Edit section', s.title || '', s.markdown || '', function(t, m){ return jpost('section?' + gp + sp, {title:t, markdown:m}); }, gid);
      });
    } else if (act === 'discuss'){
      fetch('section?' + gp + sp, {credentials:'same-origin'}).then(function(r){ return r.json(); }).then(function(s){
        discuss(gp, sp, s.title || '', s.markdown || '', function(m){ return jpost('section?' + gp + sp, {title: s.title || '', markdown: m}); },
          'Let us work on this section. Ask me what you need to know first.');
      });
    } else if (act === 'discuss-new'){
      window.uiPrompt('What is the new section about? A working title is enough.', '').then(function(t){
        t = (t || '').trim();
        if (!t) return;
        discuss(gp, '', t, '', function(m){ return jpost('section/add?' + gp, {title: t, markdown: m}); },
          'I want to add a section, working title "' + t + '". Ask me what you need to know first.');
      });
    } else if (act === 'delete'){
      window.uiConfirm('Delete this section? You can restore it from History.').then(function(ok){
        if (!ok) return;
        fetch('section?' + gp + sp, {method:'DELETE', credentials:'same-origin'}).then(refresh);
      });
    } else if (act === 'up' || act === 'down'){
      jpost('section/move?' + gp + sp + '&dir=' + act).then(refresh);
    }
  });
})();`

// guideKnowledgeAction is the 'guides_knowledge' client action behind the
// Knowledge toolbar button: a modal that attaches/detaches knowledge collections
// to the open guide (the set the Guide Author's search_knowledge tool consults).
// App-specific, injected via ExtraHeadHTML so the picker stays out of core/ui.
const guideKnowledgeAction = `function(ctx){
      var gid = ctx.recordId;
      if (!gid || !window.uiOpenSimpleModal) return;
      var qp = 'guide=' + encodeURIComponent(gid);
      window.uiOpenSimpleModal({title:'Attach knowledge', width:'560px', mount: function(body){
        window.uiMountComponent({
          type:'chip_picker', mode:'attach',
          options_source:'collections?' + qp,
          attached_field:'attached',
          save_key:'collections',
          post_to:'collections?' + qp,
          name_field:'id', label_field:'name', desc_field:'description',
          noun:'collection',
          intro:'Pick the knowledge collections the Guide Author can search while drafting this guide. It searches them with the search_knowledge tool to ground sections in your own material.',
          empty_text:'You have no knowledge collections yet. Create one in the Knowledge app, then attach it here.'
        }, body);
      }});
}`

// guideKnowledgeCSS carries the one intro-paragraph style still shared by the
// Settings modal. The knowledge + sources pickers now render via the shared
// core/ui chip_picker (attach mode), which owns its own styling. Injected via
// ui.Head.CSS.
const guideKnowledgeCSS = `.guide-kn-intro { color: var(--text-mute); font-size: var(--fs-md, 0.88rem); margin: 0 0 0.9rem; }`

// guideSourcesAction is the 'guides_sources' client action behind the Sources
// toolbar button: a modal that attaches/detaches cross-app reference sources
// (servitor Systems, connected document sources like Confluence) to the open
// guide. They are all the Guide Author's list_reference_sources and
// pull_reference can reach. App-specific, injected via ExtraHeadHTML to keep it out of
// core/ui. Reuses the guide-kn-* picker styles plus a group header.
const guideSourcesAction = `function(ctx){
      var gid = ctx.recordId;
      if (!gid || !window.uiOpenSimpleModal) return;
      var qp = 'guide=' + encodeURIComponent(gid);
      window.uiOpenSimpleModal({title:'Guide sources', width:'560px', mount: function(body){
        window.uiMountComponent({
          type:'chip_picker', mode:'attach',
          options_source:'references?' + qp,
          attached_field:'attached',
          save_key:'references',
          post_to:'references?' + qp,
          name_field:'id', label_field:'name', desc_field:'desc',
          group_by_field:'group',
          noun:'source',
          flag_field:'distill', flag_when_field:'can_distill',
          flag_label:'Distill', flag_off_label:'Consult',
          flag_help:'Distill keeps this guide up to date from that one: each time its Curator changes it, what this guide\u2019s brief needs from the change is distilled in. Consult leaves it for the Author to look things up in when it needs to.',
          intro:'Attach knowledge other gohort services have gathered: your Systems (servitor), connected document sources (e.g. Confluence), your agents and your other guides. The Guide Author looks things up in them when it needs to (Consult). A guide you attach can be set to Distill instead, to keep this one up to date from it.',
          empty_text:'No reference sources available yet. Systems appear once you have systems in the servitor app; document sources appear once connected.'
        }, body);
      }});
}`

// guideShareAction is the 'guides_share' client action behind the Share
// toolbar button: the shared people-and-teams picker, with Everyone as one
// more choice, and Can edit / Can view on each. It saves as it changes.
// Owner/admin only (server-enforced).
const guideShareAction = `function(ctx){
      var gid = ctx.recordId;
      if (!gid || !window.uiOpenSimpleModal) return;
      var qp = 'id=' + encodeURIComponent(gid);
      fetch('share?' + qp, {credentials:'same-origin'}).then(function(r){
        window.uiOpenSimpleModal({title:'Share', width:'560px', mount: function(body){
          if (!r.ok){
            body.appendChild(el('p', {class:'guide-kn-intro', text:'Only the owner can change who this guide is shared with.'}));
            return;
          }
          window.uiMountComponent({
            type:'chip_picker', mode:'attach',
            options_source:'share/candidates?'+qp,
            record_source:'share?' + qp, field:'allowed_users',
            post_to:'share?' + qp, method:'PATCH',
            name_field:'value', label_field:'label', desc_field:'desc',
            noun:'person or team',
            intro:'Who can open this guide: people, a team you are on, or everyone signed in. Each starts able to read and export it. Everyone needs an administrator to approve it.',
            empty_text:'There is no one else on this deployment to share with yet.',
            flag_field:'editors', flag_label:'Can edit', flag_off_label:'Can view',
            flag_help:'Can edit: change sections and write with the assistant. Only you can change sharing, delete the guide, or manage its knowledge and sources.',
            invalidate:['guides']
          }, body);
        }});
      });
}`

// guideSettingsAction is the 'guides_settings' client action behind the
// Settings toolbar button: ONE modal for the guide's name/subtitle, the
// Private (no-internet) flag, the writing brief and how findings reach it.
// Sharing is the Share button's. Owner/admin only (server-enforced).
// App-specific, injected via ExtraHeadHTML to keep it out of core/ui.
const guideSettingsAction = `function(ctx){
      function fieldText(label, value){
        var inp = el('input', {type:'text', value: value || ''});
        inp.style.width = '100%'; inp.style.padding = '0.45rem 0.6rem'; inp.style.background = 'var(--bg-0)';
        inp.style.color = 'var(--text)'; inp.style.border = '1px solid var(--border)'; inp.style.borderRadius = '6px';
        var wrap = el('div', {class:'guide-edit-field'}, [el('label', {text: label}), inp]);
        return {wrap: wrap, input: inp};
      }
      var gid = ctx.recordId;
      if (!gid || !window.uiOpenSimpleModal) return;
      var qp = 'id=' + encodeURIComponent(gid);
      fetch('settings?' + qp, {credentials:'same-origin'}).then(function(r){ return r.json(); }).then(function(d){
        var canManage = !!(d && d.can_manage);
        window.uiOpenSimpleModal({title:'Edit guide', width:'520px', mount: function(body, dlg){
          if (!canManage){
            body.appendChild(el('p', {class:'guide-kn-intro', text:'Only the owner can change these settings.'}));
            return;
          }
          var tf = fieldText('Title', (d && d.title) || '');
          var sf = fieldText('Subtitle', (d && d.subtitle) || '');
          body.appendChild(tf.wrap); body.appendChild(sf.wrap);
          // Writing brief: what this guide is for, followed by everything that
          // writes it (the Author, the Curator and its placements).
          var bta = el('textarea', {rows:'5', placeholder:'e.g. This guide teaches other models how to read diagnostic dumps. Write every section as a general lesson: the symptom, where to look in the dump, what it means, how to confirm it, what to do. Leave out incident specifics.'});
          bta.value = (d && d.brief) || '';
          bta.style.width = '100%'; bta.style.padding = '0.45rem 0.6rem'; bta.style.background = 'var(--bg-0)';
          bta.style.color = 'var(--text)'; bta.style.border = '1px solid var(--border)'; bta.style.borderRadius = '6px';
          bta.style.resize = 'vertical'; bta.style.fontFamily = 'inherit';
          // Discuss: work the brief out with the guide's Author, which asks
          // before it writes. Its draft fills the box; Save keeps it.
          var bdisc = el('button', {class:'ui-row-btn', type:'button', text:'✨ Discuss', title:'Work the brief out with the assistant: it asks what the guide is for, then writes it'});
          bdisc.addEventListener('click', function(){
            if (!window.uiOpenAssist) return;
            var session = Math.random().toString(36).slice(2) + Date.now().toString(36);
            window.uiOpenAssist({
              mode: 'discuss',
              title: 'Discuss: writing brief',
              subtitle: 'The guide\u2019s assistant asks what the guide is for, then writes the brief. Using the draft puts it in the box; Save keeps it.',
              initial: bta.value,
              ask: bta.value.trim() ? 'Help me improve this guide\u2019s writing brief. Ask me what you need to know first.' : 'Help me write this guide\u2019s writing brief. Ask me what you need to know first.',
              send: function(req, done){
                fetch('brief/discuss?' + qp, {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'},
                  body: JSON.stringify({message: req.message, draft: req.draft, history: req.history, session: session})})
                  .then(function(r){ return r.text().then(function(t){ if (!r.ok) throw new Error(t || ('HTTP ' + r.status)); return JSON.parse(t); }); })
                  .then(function(res){ done(res); })
                  .catch(function(err){ done(null, (err && err.message) || String(err)); });
              },
              onAccept: function(t){ bta.value = t; bta.focus(); }
            });
          });
          body.appendChild(el('div', {class:'guide-edit-field'}, [el('label', {text:'Writing brief'}), bta,
            el('div', {style:'display:flex;align-items:flex-start;gap:0.6rem;margin-top:0.3rem'}, [
              el('div', {class:'guide-pub-mute', style:'font-size:0.8rem;flex:1',
                text:'What this guide is for and how it is written. The Guide Author follows it when you edit, and the Curator decides by it what to keep, what to fold into an existing section, and what to drop.'}),
              bdisc])]));
          // Private (no internet).
          var pcb = el('input', {type:'checkbox'}); if (d && d.private) pcb.checked = true;
          body.appendChild(el('label', {class:'guide-share-row'}, [pcb,
            el('span', {text: "Private, no internet access. The assistant answers and edits only from this document's attached knowledge; web search and research are disabled, and every model call stays on the local worker."})]));
          // Findings: what agents attached to this guide report into it.
          body.appendChild(el('div', {class:'guide-set-head', text:'Findings'}));
          var ccb = el('input', {type:'checkbox'}); if (d && d.curate_on_report) ccb.checked = true;
          body.appendChild(el('label', {class:'guide-share-row'}, [ccb,
            el('span', {text:'Curate findings as they arrive. When an agent reports something for this guide, the Guide Curator files it within about a minute, with no review first. Every change is in the Curator digest, with Undo.'})]));
          var save = dlg.addAction(el('button', {class:'ui-row-btn primary', text:'Save'}));
          save.addEventListener('click', function(){
            save.disabled = true; save.textContent = 'Saving…';
            fetch('settings?' + qp, {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'},
              body: JSON.stringify({title: tf.input.value, subtitle: sf.input.value, private: pcb.checked, curate_on_report: ccb.checked, brief: bta.value})})
              .then(function(r){ if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); }); })
              .then(function(){ try { dlg.close(); dlg.remove(); } catch(e){} if (window.uiInvalidate) window.uiInvalidate('guides'); })
              .catch(function(err){ save.disabled = false; save.textContent = 'Save'; window.uiAlert('Save failed: ' + (err && err.message || err)); });
          });
        }});
      });
}`

// guideSettingsCSS styles the settings/sharing modal rows.
const guideSettingsCSS = `.guide-share-row { display: flex; align-items: center; gap: 0.55rem; cursor: pointer; padding: 0.5rem 0; font-size: var(--fs-md, 0.92rem); color: var(--text-hi); }
.guide-set-head { font-size: var(--fs-2xs, 0.72rem); text-transform: uppercase; letter-spacing: 0.06em; color: var(--text-mute); font-weight: 700; margin: 0.9rem 0 0.2rem; border-top: 1px solid var(--border); padding-top: 0.7rem; }`

// guidePublishAction is the 'guides_publish' client action behind the Publish
// toolbar button. It opens the Publisher agent in a modal rather than a form:
// which space a page belongs in, and whether this is the page published last
// time, are questions worth asking rather than guessing.
//
// The panel is a plain core/ui agent_loop_panel pointed at this app's publish
// chat endpoint — ask_user cards render in it for free — so nothing about
// publishing leaks into core/ui. Where the guide has already been published,
// each destination gets a Republish link that updates that exact page with no
// conversation at all.
const guidePublishAction = `function(ctx){
      var gid = ctx.recordId;
      if (!gid || !window.uiOpenSimpleModal) return;
      var qp = 'id=' + encodeURIComponent(gid);
      fetch('publish/state?' + qp, {credentials:'same-origin'}).then(function(r){ return r.json(); }).then(function(d){
        window.uiOpenSimpleModal({title:'Publish guide', width:'760px', mount: function(body, dlg){
          var targets = (d && d.targets) || [];
          // Where publishing targets are made, opened in a new tab so this
          // guide stays put; reopening Publish picks up the new target.
          function setupLink(label){
            if (!d || !d.targets_url) return null;
            return el('a', {class:'guide-pub-link', href: d.targets_url, target:'_blank', rel:'noopener', text: label});
          }
          if (!d || (!d.configured && !targets.length)){
            body.appendChild(el('p', {class:'guide-kn-intro', text:'Nowhere to publish yet. Make a publishing target of your own from one of your API or MCP integrations, or one of your agents.'}));
            var first = setupLink('Set up a publishing target in Extensions');
            if (first) body.appendChild(first);
            return;
          }
          if (!d.can_publish){
            body.appendChild(el('p', {class:'guide-kn-intro', text:'You need edit access to this guide to publish it.'}));
            return;
          }
          var view = el('div', {});
          body.appendChild(view);

          // A publish in flight: alive (spinner + seconds) until it reports
          // its own ending, polled every 2s. Rejoined when the modal opens
          // mid-run.
          function followJob(){
            view.innerHTML = '';
            var line = el('div', {class:'guide-pub-status'});
            // Stop beside Close in the footer while it runs. Closing the
            // window leaves the publish running (it outlives the page, and
            // reopening rejoins it); Stop is how it is ended.
            var stop = el('button', {class:'ui-row-btn', text:'Stop'});
            stop.addEventListener('click', function(){
              stop.disabled = true; stop.textContent = 'Stopping';
              fetch('publish/stop?' + qp, {method:'POST', credentials:'same-origin'})
                .then(function(r){ return r.text().then(function(t){ if (!r.ok) throw new Error(t || ('HTTP ' + r.status)); return t; }); })
                .catch(function(err){ stop.disabled = false; stop.textContent = 'Stop';
                  window.uiAlert('Could not stop it: ' + (err && err.message || err)); });
            });
            view.appendChild(el('div', {class:'guide-pub-running'}, [line]));
            dlg.addAction(stop);
            function dropStop(){ if (stop.parentNode) stop.parentNode.removeChild(stop); }
            // What the publish is doing, step by step, under the spinner;
            // kept when it ends, so an outcome can be checked against what
            // was actually done.
            var steps = el('ol', {class:'guide-pub-steps'});
            view.appendChild(steps);
            function showSteps(list){
              steps.innerHTML = '';
              (list || []).forEach(function(st){
                steps.appendChild(el('li', {}, [el('span', {class:'guide-pub-mute', text: st.at + 's '}), document.createTextNode(st.text)]));
              });
              steps.style.display = (list && list.length) ? '' : 'none';
            }
            var frames = ['\u280b','\u2819','\u2839','\u2838','\u283c','\u2834','\u2826','\u2827','\u2807','\u280f'], fi = 0, last = null;
            var spin = setInterval(function(){
              fi = (fi + 1) % frames.length;
              // What it is doing now (its round, its thinking, the step it is
              // on) rides beside the target, so a long model round reads as
              // working rather than stuck on the last step listed.
              if (last && !last.done) line.textContent = frames[fi] + (last.linked ? ' Linking to a page in ' : ' Publishing to ') + last.target + (last.now ? ' - ' + last.now : '') + (last.elapsed >= 3 ? ' - ' + last.elapsed + 's' : '');
            }, 120);
            function poll(){
              fetch('publish/job?' + qp, {credentials:'same-origin', cache:'no-store'}).then(function(r){ return r.json(); }).then(function(j){
                if (j.none){ clearInterval(spin); dropStop(); showList(); return; }
                last = j;
                showSteps(j.steps);
                if (!j.done){ setTimeout(poll, 2000); return; }
                clearInterval(spin);
                dropStop();
                line.textContent = (j.linked ? (j.ok ? 'Linked to a page in ' : j.stopped ? 'Stopped linking to ' : 'Could not link to a page in ')
                  : (j.ok ? 'Published to ' : j.stopped ? 'Stopped publishing to ' : 'Could not publish to ')) + j.target + ' (' + j.took + ').';
                line.className = 'guide-pub-status ' + (j.ok ? 'guide-pub-ok' : 'guide-pub-fail');
                if (j.message) view.appendChild(el('p', {class:'guide-kn-intro', text: j.message}));
                if (j.url) view.appendChild(el('a', {class:'guide-pub-link', href: j.url, target:'_blank', rel:'noopener', text:'Open it'}));
                var back = el('button', {class:'ui-row-btn', text:'Back'});
                back.addEventListener('click', function(){ reload(); });
                var btns = [back];
                if (!j.ok && !j.stopped && !j.linked){
                  // A quick update that failed usually needs an answer: hand
                  // it to the Publisher with what went wrong.
                  var talk = el('button', {class:'ui-row-btn primary', text:'Work it out with the Publisher'});
                  talk.addEventListener('click', function(){
                    openChat('Update this guide in my publishing target "' + j.target + '". The last try failed: ' + (j.message || 'no reason given'));
                  });
                  btns.unshift(talk);
                }
                view.appendChild(el('div', {class:'guide-pub-actions', style:'margin-top:0.7rem'}, btns));
                if (window.uiInvalidate) window.uiInvalidate('guides');
              }).catch(function(){ setTimeout(poll, 3000); });
            }
            poll();
          }
          function startPublish(req){
            return fetch('publish/to?' + qp, {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'}, body: JSON.stringify(req)})
              .then(function(r){ return r.text().then(function(t){ if (!r.ok) throw new Error(t || ('HTTP ' + r.status)); return t; }); })
              .then(function(){ followJob(); })
              .catch(function(err){ window.uiAlert('Could not publish: ' + (err && err.message || err)); });
          }
          function reload(){
            fetch('publish/state?' + qp, {credentials:'same-origin'}).then(function(r){ return r.json(); }).then(function(nd){ d = nd; targets = nd.targets || []; showList(); });
          }

          // The Publisher, in this window: it publishes, and asks here
          // whatever it actually needs (a target's questions it cannot
          // settle from the guide, a value the place refused). Back returns
          // to the list, which then shows where the guide landed.
          function openChat(ask){
            view.innerHTML = '';
            var back = el('button', {class:'ui-row-btn', text:'Back'});
            back.addEventListener('click', function(){ reload(); });
            view.appendChild(el('div', {class:'guide-pub-actions'}, [back]));
            var host = el('div', {class:'guide-pub-chat'});
            view.appendChild(host);
            window.uiMountComponent({
              type: 'agent_loop_panel',
              send_url: 'publish/chat/send?guide=' + encodeURIComponent(gid),
              cancel_url: 'chat/cancel',
              markdown: true,
              lock_activity: true,
              auto_send: ask,
              empty_text: 'Getting it ready to publish...',
              placeholder: 'Answer the Publisher...',
              submit_label: 'Send'
            }, host);
          }

          function showList(){
            view.innerHTML = '';
            // Where it already lives, with an update of that same place.
            (d.published || []).forEach(function(p){
              var row = el('div', {class:'guide-pub-row'});
              var where = p.target_title || p.kind;
              row.appendChild(el('div', {class:'guide-pub-where'}, [el('strong', {text: p.title || 'Published'}),
                el('span', {class:'guide-pub-mute', text: ' in ' + where + (p.version ? ' (v' + p.version + ')' : '')})]));
              if (p.url) row.appendChild(el('a', {class:'guide-pub-link', href: p.url, target:'_blank', rel:'noopener', text:'Open'}));
              var again = el('button', {class:'ui-row-btn', text:'Update'});
              again.addEventListener('click', function(){
                if (String(p.kind).indexOf('target:') === 0){
                  again.disabled = true;
                  startPublish({kind: p.kind, target: p.target, title: p.title, answers: p.answers || {}});
                  return;
                }
                again.disabled = true;
                fetch('publish/again?' + qp + '&kind=' + encodeURIComponent(p.kind), {method:'POST', credentials:'same-origin'})
                  .then(function(r){ return r.text().then(function(t){ if (!r.ok) throw new Error(t || ('HTTP ' + r.status)); return t; }); })
                  .then(function(){ followJob(); })
                  .catch(function(err){ again.disabled = false;
                    window.uiAlert('Could not update it: ' + (err && err.message || err)); });
              });
              row.appendChild(again);
              view.appendChild(row);
            });
            // Keep published: the copies above follow the guide, updated
            // each time the Guide Curator changes it. Only once it is
            // published somewhere, since it only ever updates those places.
            if ((d.published || []).length){
              var keepBox = el('input', {type:'checkbox'});
              keepBox.checked = !!d.keep_published;
              keepBox.addEventListener('change', function(){
                var want = keepBox.checked;
                keepBox.disabled = true;
                fetch('publish/keep?' + qp, {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'}, body: JSON.stringify({on: want})})
                  .then(function(r){ return r.text().then(function(t){ if (!r.ok) throw new Error(t || ('HTTP ' + r.status)); return JSON.parse(t); }); })
                  .then(function(res){ d.keep_published = !!res.keep_published; keepBox.checked = d.keep_published; keepBox.disabled = false; })
                  .catch(function(err){ keepBox.checked = !want; keepBox.disabled = false;
                    window.uiAlert('Could not change it: ' + (err && err.message || err)); });
              });
              view.appendChild(el('label', {class:'guide-share-row'}, [keepBox,
                el('span', {text:'Keep published: update these copies each time the Guide Curator changes this guide, and bring edits made on them back through the Curator.'})]));
            }
            // The person's own targets, each opening the Publisher on it.
            if (targets.length){
              view.appendChild(el('div', {class:'guide-set-head', text:'Publish to'}));
              targets.forEach(function(t){
                var b = el('button', {class:'guide-pub-target'}, [el('strong', {text: t.target.title}),
                  el('span', {class:'guide-pub-mute', text: t.target.desc ? ' - ' + t.target.desc : ''})]);
                b.addEventListener('click', function(){ openChat('Publish this guide to my publishing target "' + t.target.title + '" (destination id: ' + t.kind + ').'); });
                view.appendChild(b);
              });
            } else {
              view.appendChild(el('div', {class:'guide-set-head', text:'Publish to'}));
              view.appendChild(el('p', {class:'guide-kn-intro', text:'You have no publishing targets of your own yet: a place you publish to often, set up once with how to publish there.'}));
              var setup = setupLink('Set one up in Extensions');
              if (setup) view.appendChild(setup);
            }
            // Everything else (an admin's Confluence, a webhook, an agent
            // destination) goes through the Publisher, which asks where.
            if (d.other_count > 0){
              var other = el('button', {class:'ui-row-btn', text: targets.length ? 'Other destinations...' : 'Choose where to publish'});
              other.addEventListener('click', function(){ openChat('Publish this guide.'); });
              view.appendChild(el('div', {style:'margin-top:0.8rem'}, [other]));
            }
            // A page this guide should keep, that it did not make: link it,
            // so the next update goes there instead of to a new page.
            view.appendChild(el('div', {class:'guide-set-head', text:'Already on a page somewhere?'}));
            view.appendChild(el('p', {class:'guide-kn-intro', text:'Link this guide to that page and updates go there, in place of a new page. The next update replaces what the page says with this guide.'}));
            var linkBtn = el('button', {class:'ui-row-btn', text:'Link to an existing page'});
            var linkForm = el('div', {style:'margin-top:0.5rem'});
            linkBtn.addEventListener('click', function(){
              linkBtn.disabled = true;
              fetch('import/page', {credentials:'same-origin'}).then(function(r){ return r.ok ? r.json() : {places: []}; }).then(function(pd){
                var places = (pd && pd.places) || [];
                linkForm.innerHTML = '';
                if (!places.length){
                  linkForm.appendChild(el('p', {class:'guide-kn-intro', text:'Nothing you can read a page from is set up yet: connect Confluence, or make a publishing target in Extensions.'}));
                  return;
                }
                var pick = el('select', {class:'ui-input'});
                places.forEach(function(pl, i){ var o = el('option', {text: pl.title + (pl.group ? ' (' + pl.group + ')' : '')}); o.value = String(i); pick.appendChild(o); });
                var addr = el('input', {class:'ui-input', type:'text', placeholder:'The page address, from your browser'});
                var go = el('button', {class:'ui-row-btn primary', text:'Link'});
                go.addEventListener('click', function(){
                  var pl = places[parseInt(pick.value, 10)];
                  if (!pl || !addr.value.trim()) { window.uiAlert('Pick where the page is and paste its address.'); return; }
                  go.disabled = true;
                  fetch('publish/link?' + qp, {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'},
                    body: JSON.stringify({kind: pl.kind, target: pl.target || '', link: addr.value.trim()})})
                    .then(function(r){ return r.text().then(function(t){ if (!r.ok) throw new Error(t || ('HTTP ' + r.status)); return t; }); })
                    .then(function(){ followJob(); })
                    .catch(function(err){ go.disabled = false; window.uiAlert('Could not link it: ' + (err && err.message || err)); });
                });
                linkForm.appendChild(el('label', {text:'Where it is'})); linkForm.appendChild(pick);
                linkForm.appendChild(el('label', {text:'Its address', style:'margin-top:0.5rem;display:block'})); linkForm.appendChild(addr);
                linkForm.appendChild(el('div', {style:'margin-top:0.5rem'}, [go]));
              }).catch(function(){ linkBtn.disabled = false; });
            });
            view.appendChild(linkBtn);
            view.appendChild(linkForm);
          }
          if (d.running) followJob(); else showList();
        }});
      });
}`

// guidePublishCSS styles the "already published here" rows and gives the
// Publisher chat a fixed height inside the modal, so the modal doesn't grow as
// the conversation does.
const guidePublishCSS = `.guide-pub-row { display: flex; align-items: center; gap: 0.6rem; padding: 0.5rem 0.7rem; margin-bottom: 0.5rem; background: var(--bg-2); border: 1px solid var(--border); border-radius: 8px; }
.guide-pub-where { flex: 1; font-size: var(--fs-md, 0.9rem); color: var(--text-hi); }
.guide-pub-mute { color: var(--text-mute); font-weight: 400; }
.guide-pub-link { color: var(--accent); font-size: var(--fs-sm, 0.85rem); text-decoration: none; }
.guide-pub-link:hover { text-decoration: underline; }
.guide-pub-target { display: flex; gap: 0.3rem; align-items: baseline; width: 100%; text-align: left; padding: 0.6rem 0.8rem; margin-bottom: 0.45rem; background: var(--bg-2); border: 1px solid var(--border); border-radius: var(--btn-radius, 6px); color: var(--text-hi); cursor: pointer; font: inherit; }
@media (pointer: coarse) { .guide-pub-target { min-height: var(--tap, 44px); align-items: center; } }
.guide-pub-target:hover { border-color: var(--accent); }
.guide-pub-actions { display: flex; gap: 0.5rem; margin-top: 0.8rem; }
.guide-pub-status { font-size: var(--fs-lg, 0.95rem); color: var(--text-hi); padding: 0.6rem 0; }
.guide-pub-running { display: flex; align-items: center; gap: 0.6rem; }
.guide-pub-running .guide-pub-status { flex: 1; min-width: 0; }
.guide-pub-steps { margin: 0 0 0.6rem; padding-left: 1.4rem; max-height: 16rem; overflow-y: auto; font-size: var(--fs-sm, 0.8rem); line-height: 1.45; color: var(--text); font-family: var(--mono, ui-monospace, monospace); word-break: break-word; }
.guide-pub-steps li { margin: 0.1rem 0; }
.guide-pub-ok { color: var(--ok, #3fb950); }
.guide-pub-fail { color: var(--danger, #f85149); }
.guide-pub-chat { height: 52vh; min-height: 20rem; display: flex; flex-direction: column; margin-top: 0.4rem; }
.guide-pub-chat > * { flex: 1; min-height: 0; }
@media (max-width: 700px) { .guide-pub-chat { height: 60vh; } }`
