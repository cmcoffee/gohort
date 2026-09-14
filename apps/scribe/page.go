// The Scribe workbench page: document list (left) | rendered HTML document
// (center: a guide with its table of contents, or an article with its header
// image) | Guide Author chat (right). Built from the core/ui WorkbenchPanel
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
		ListEmpty: "Nothing yet — create a guide or an article.",
		DeleteURL: "guide?id={id}",
		NewButton: ui.ModalButton{
			Label: "New",
			Title: "New document",
			Body: ui.FormPanel{
				PostURL:     "new",
				SubmitLabel: "Create",
				Fields: []ui.FormField{
					{Field: "kind", Label: "Kind", Type: "select", Options: []ui.SelectOption{
						{Value: "guide", Label: "Guide", Help: "Many sections with a table of contents. The Guide Author adds and edits sections; you edit any section in place."},
						{Value: "article", Label: "Article", Help: "One body under a title, with an optional header image. Type into it directly, or have the Guide Author write it. Starts private."},
					}},
					{Field: "title", Label: "Title", Type: "text", Placeholder: "e.g. Getting Started with Kubernetes"},
					{Field: "subtitle", Label: "Subtitle", Type: "text", Placeholder: "Optional one-line description"},
					{Field: "template", Label: "Start from", Type: "select", Options: templateOptions(),
						Help: "Articles only: a starting skeleton for a document people write repeatedly."},
				},
				Invalidate: []string{"guides"},
			},
		},
		// Record-scoped and library-scoped actions in the list header, left of
		// New: the open document's settings, the house-style rules the Guide
		// Author writes under, and importing a page exported earlier.
		ListActions: []ui.WorkbenchAction{
			{Label: "Settings", Kind: "client", URL: "guides_settings"},
			{Label: "Rules", Kind: "client", URL: "scribe_rules"},
			{Label: "Import", Kind: "client", URL: "scribe_import"},
		},
		// Center — the rendered document (server HTML: title + ToC + sections,
		// or title + image + body).
		RecordURL:  "guide?id={id}",
		BodyField:  "html",
		BodyIsHTML: true,
		// Direct editing: an article's whole body in a textarea. The record
		// carries "markdown" only for an article the user may edit, which is what
		// enables the toggle; guides keep their per-section controls.
		EditURL:    "body?id={id}",
		EditField:  "markdown",
		EmptyIcon:  "📖",
		EmptyTitle: "Nothing selected",
		EmptyHint:  "Pick a document on the left, or create one. Then ask the assistant to draft it, or write it yourself.",
		// Per-document toolbar: preview/export, revision history, freshness audit.
		ViewerActions: []ui.WorkbenchAction{
			{Label: "Export", Kind: "menu", Children: []ui.WorkbenchAction{
				{Label: "Preview (HTML)", Kind: "download", URL: "export?id={id}&format=html"},
				{Label: "HTML file", Kind: "download", URL: "export?id={id}&format=html&download=1"},
				{Label: "PDF", Kind: "download", URL: "export?id={id}&format=pdf"},
				{Label: "Markdown", Kind: "download", URL: "export?id={id}&format=md"},
			}},
			// Second, not eighth. Reaching for an earlier version is a recovery
			// move — a section is gone and you want it back — so it should not be
			// hunted for at the far end of the toolbar.
			{Label: "History", Kind: "history", URL: "revisions?id={id}",
				PreviewURL: "revision?id={id}&rev={rev}", RestoreURL: "restore?id={id}&rev={rev}"},
			{Label: "Publish", Kind: "client", URL: "guides_publish"},
			{Label: "Image", Kind: "client", URL: "scribe_image"},
			{Label: "Sources", Kind: "client", URL: "guides_sources"},
			{Label: "Curator", Kind: "client", URL: "guides_curator"},
			{Label: "Knowledge", Kind: "client", URL: "guides_knowledge"},
			{Label: "Audit", Kind: "report", URL: "audit?id={id}", Spinner: "Auditing…", Invalidate: []string{"guides"}},
			{Label: "Reorganize", Kind: "report", URL: "reorganize?id={id}", Spinner: "Reorganizing…", Invalidate: []string{"guides"}},
			{Label: "Update from sources", Kind: "report", URL: "update-sources?id={id}", Spinner: "Updating…", Invalidate: []string{"guides"}},
		},
		// The agent writes sections via its tools; re-render the open guide when a
		// chat round finishes.
		RefreshOn: []string{"guides"},
		ActiveURL: "chat/active",
		// Right — the Guide Author chat (endpoints; WorkbenchPanel builds the panel).
		Chat: ui.AgentLoopPanel{
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
			ListURL:      "chat/sessions",
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
			SendURL:    "chat/send",
			CancelURL:  "chat/cancel",
			// Where a mid-flight message goes. A guide is written over a long
			// turn — the author reads a section as it lands and thinks of one
			// more thing — so this is the panel most likely to be typed into
			// while it is busy, and without it that second thought cancelled
			// the first.
			InjectURL:    "chat/inject",
			Markdown:     true,
			LockActivity: true,
			EmptyText:    "Ask me to draft or revise — e.g. \"Add an introduction\", \"Expand the setup section\", or \"Rewrite this as a runbook.\"",
			Placeholder:  "Ask the Guide Author…",
		},
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
			CSS(scribeArticleCSS).
			JS(guideModalElJS).
			JS(guideSectionCode).
			ClientAction("guides_knowledge", guideKnowledgeAction).
			ClientAction("guides_sources", guideSourcesAction).
			ClientAction("guides_settings", guideSettingsAction).
			ClientAction("guides_curator", guideCuratorAction).
			ClientAction("guides_publish", guidePublishAction).
			ClientAction("scribe_rules", scribeRulesAction).
			ClientAction("scribe_import", scribeImportAction).
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

// scribeImportAction brings an exported HTML page back in as a new article:
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
            window.uiAlert('Imported "' + ((d && d.title) || 'article') + '". It is in the list on the left.');
          })
          .catch(function(err){ window.uiAlert('Import failed: ' + (err && err.message || err)); });
      });
      input.click();
}`

// scribeImageAction manages an article's header image in a modal: show the
// current one, generate one from the title, paste a URL, or remove it.
const scribeImageAction = `function(ctx){
      var gid = ctx.recordId;
      if (!gid || !window.uiOpenSimpleModal) return;
      var qp = 'id=' + encodeURIComponent(gid);
      fetch('image?' + qp, {credentials:'same-origin'}).then(function(r){ return r.json(); }).then(function(d){
        window.uiOpenSimpleModal({title:'Header image', width:'560px', mount: function(body, dlg){
          if (!d || !d.can_edit){
            body.appendChild(el('p', {class:'guide-kn-intro', text:'Only an article you can edit has a header image.'}));
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
          var row = el('div', {class:'guide-edit-actions scribe-img-actions'});
          var gen = el('button', {class:'ui-row-btn primary', text:'Generate from title'});
          if (!d.can_generate) { gen.disabled = true; gen.title = 'Image generation is not configured on this deployment'; }
          var urlIn = el('input', {type:'text', placeholder:'…or paste an image URL'});
          var use = el('button', {class:'ui-row-btn', text:'Use URL'});
          var rm = el('button', {class:'ui-row-btn', text:'Remove'});
          function post(payload, label){
            status.textContent = label;
            gen.disabled = true; use.disabled = true; rm.disabled = true;
            return fetch('image?' + qp, {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'}, body: JSON.stringify(payload)})
              .then(function(r){ return r.text().then(function(t){ if (!r.ok) throw new Error(t || ('HTTP ' + r.status)); return JSON.parse(t); }); })
              .then(function(res){ status.textContent = ''; showPreview(res.image_url); ctx.refresh(); })
              .catch(function(err){ status.textContent = ''; window.uiAlert('Could not set the image: ' + (err && err.message || err)); })
              .then(function(){ gen.disabled = !d.can_generate; use.disabled = false; rm.disabled = false; });
          }
          gen.addEventListener('click', function(){ post({generate: true}, 'Generating…'); });
          use.addEventListener('click', function(){ if (urlIn.value.trim()) post({url: urlIn.value.trim()}, 'Saving…'); });
          rm.addEventListener('click', function(){
            fetch('image?' + qp, {method:'DELETE', credentials:'same-origin'}).then(function(){ showPreview(''); ctx.refresh(); });
          });
          row.appendChild(gen); row.appendChild(rm);
          body.appendChild(row);
          body.appendChild(el('div', {class:'scribe-img-url'}, [urlIn, use]));
          body.appendChild(status);
        }});
      });
}`

// scribeArticleCSS styles the article view (header image, body measure) and the
// image modal. Scoped under .guide-doc / .scribe-img-* so nothing leaks.
const scribeArticleCSS = `.guide-doc-image { display: block; width: 100%; max-height: 320px; object-fit: cover; border-radius: 10px; margin: 0 0 1.4rem; }
.guide-article-body > :first-child { margin-top: 0; }
.guide-article-body h2 { font-size: 1.4rem; color: var(--text-hi); border-bottom: 1px solid var(--border); padding-bottom: 0.3rem; margin: 1.6rem 0 0.8rem; }
.guide-article-body h3 { font-size: 1.12rem; color: var(--text-hi); margin: 1.3rem 0 0.5rem; }
.scribe-img-preview { min-height: 4rem; display: flex; align-items: center; justify-content: center; background: var(--bg-2); border: 1px solid var(--border); border-radius: 8px; margin-bottom: 0.8rem; overflow: hidden; }
.scribe-img-preview img { width: 100%; max-height: 220px; object-fit: cover; display: block; }
.scribe-img-actions { display: flex; gap: 0.5rem; margin-bottom: 0.6rem; }
.scribe-img-url { display: flex; gap: 0.5rem; }
.scribe-img-url input { flex: 1; padding: 0.45rem 0.6rem; background: var(--bg-0); color: var(--text); border: 1px solid var(--border); border-radius: 6px; }`

// guideDocCSS styles the rendered guide so it reads like a formatted document:
// a centered measure, a contents block, numbered section headings. Scoped under
// .guide-doc so it never leaks into other surfaces. Uses theme tokens for color.
const guideDocCSS = `<style>
.guide-doc { max-width: 760px; margin: 0 auto; padding: 0.5rem 0 3rem; }
.guide-doc-head h1 { font-size: 1.9rem; line-height: 1.2; margin: 0 0 0.3rem; color: var(--text-hi); }
.guide-doc-sub { font-size: 1.02rem; color: var(--text-mute); margin: 0 0 1.4rem; }
.guide-doc-empty { color: var(--text-mute); font-style: italic; padding: 1rem 0; }
.guide-toc {
  background: var(--bg-2); border: 1px solid var(--border); border-radius: 10px;
  padding: 0.9rem 1.1rem; margin: 0 0 2rem;
}
.guide-toc-title { font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.06em; color: var(--text-mute); margin-bottom: 0.5rem; }
.guide-toc ol { margin: 0; padding-left: 1.3rem; display: flex; flex-direction: column; gap: 0.25rem; }
.guide-toc a { color: var(--accent); text-decoration: none; }
.guide-toc a:hover { text-decoration: underline; }
.guide-section { margin: 0 0 2.2rem; scroll-margin-top: 1rem; }
.guide-section > h2 {
  font-size: 1.35rem; color: var(--text-hi);
  border-bottom: 1px solid var(--border); padding-bottom: 0.3rem; margin: 0 0 0.9rem;
}
.guide-section-num { color: var(--text-mute); font-weight: 600; margin-right: 0.3rem; }
.guide-section-body { font-size: 0.95rem; line-height: 1.65; color: var(--text); }
.guide-section-body h3 { font-size: 1.08rem; color: var(--text-hi); margin: 1.3rem 0 0.5rem; }
.guide-section-body h4 { font-size: 0.98rem; color: var(--text-hi); margin: 1.1rem 0 0.4rem; }
.guide-section-body h5 { font-size: 0.9rem; color: var(--text-hi); margin: 1rem 0 0.35rem; }
.guide-section-body h6 { font-size: 0.85rem; color: var(--text-mute); text-transform: uppercase; letter-spacing: 0.04em; margin: 0.9rem 0 0.3rem; }
.guide-section-body pre {
  background: var(--bg-0); border: 1px solid var(--border); border-radius: 8px;
  padding: 0.8rem 1rem; overflow-x: auto; font-size: 0.86rem;
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
  .guide-doc-sub { font-size: 0.95rem; }
  .guide-toc { padding: 0.7rem 0.85rem; margin-bottom: 1.4rem; }
  .guide-section { margin-bottom: 1.6rem; }
  .guide-section > h2 { font-size: 1.18rem; }
  .guide-section-body { font-size: 0.92rem; }
  .guide-section-body pre { font-size: 0.8rem; padding: 0.7rem 0.8rem; }
  /* Let wide tables scroll instead of forcing the page wider than the viewport. */
  .guide-section-body table { display: block; overflow-x: auto; max-width: 100%; }
}

/* Reading an earlier version (History -> View). The whole point of the screen is
   the section that is GONE, so it gets the accent and everything else recedes. */
.guide-rev-banner { background: var(--surface-2, rgba(99,102,241,0.06)); border: 1px solid var(--border); border-left: 3px solid #6366f1; border-radius: 6px; padding: 0.7rem 0.9rem; margin-bottom: 1.4rem; }
.guide-rev-head { font-size: 0.9rem; font-weight: 600; }
.guide-rev-sub { font-size: 0.8rem; color: var(--text-mute); margin-top: 0.35rem; }
.guide-rev-list { margin: 0.45rem 0 0; padding-left: 1.1rem; font-size: 0.85rem; }
.guide-rev-list a { color: #6366f1; }
.guide-rev .guide-section { opacity: 0.62; }
.guide-rev .guide-section-gone { opacity: 1; border-left: 3px solid #6366f1; padding-left: 0.9rem; }
.guide-rev-tag { display: inline-block; font-size: 0.68rem; letter-spacing: 0.04em; text-transform: uppercase; color: #6366f1; font-weight: 600; margin-bottom: 0.3rem; }
</style>`

// guideSectionCtrlCSS styles the inline per-section controls (hover-revealed),
// the "+ Add section" button, and the empty-state add link.
const guideSectionCtrlCSS = `<style>
.guide-section { position: relative; }
.guide-sec-ctrls {
  position: absolute; top: 0.1rem; right: 0; display: flex; gap: 0.25rem;
  opacity: 0; transition: opacity 0.12s;
}
.guide-section:hover .guide-sec-ctrls, .guide-sec-ctrls:focus-within { opacity: 1; }
.guide-sec-btn {
  cursor: pointer; background: var(--bg-2); color: var(--text-mute);
  border: 1px solid var(--border); border-radius: 6px; padding: 0.12rem 0.45rem;
  font-size: 0.74rem; font-weight: 600; line-height: 1.4;
}
.guide-sec-btn:hover { color: var(--accent); border-color: var(--accent); }
.guide-sec-del:hover { color: var(--danger); border-color: var(--danger); }
.guide-add-row { margin-top: 1.5rem; }
.guide-add-btn {
  cursor: pointer; background: transparent; color: var(--text-mute);
  border: 1px dashed var(--border); border-radius: 8px; padding: 0.5rem 1rem;
  font-size: 0.85rem; font-weight: 600; width: 100%;
}
.guide-add-btn:hover { color: var(--accent); border-color: var(--accent); }
.guide-add-link { background: none; border: 0; color: var(--accent); cursor: pointer; font: inherit; padding: 0; text-decoration: underline; }
.guide-edit-field { display: flex; flex-direction: column; gap: 0.3rem; margin-bottom: 0.8rem; }
.guide-edit-field label { font-size: 0.78rem; font-weight: 600; color: var(--text-mute); }
.guide-edit-field input, .guide-edit-field textarea {
  background: var(--bg-0); color: var(--text); border: 1px solid var(--border);
  border-radius: 6px; padding: 0.45rem 0.6rem; font: inherit; font-size: 0.9rem;
}
.guide-edit-field textarea { min-height: 16rem; resize: vertical; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.85rem; }
/* The section editor is where the writing actually happens, so its body field
   takes the whole modal rather than a fixed 16rem box with the rest of the
   dialog empty beneath it. The modal card is already a flex column capped at
   88vh, and its body flexes, so growing means opting in here — min-height is
   kept as the floor for a short viewport, where the body scrolls instead. */
.guide-edit-field.guide-edit-grow { flex: 1 1 auto; min-height: 0; margin-bottom: 0; }
.guide-edit-field.guide-edit-grow textarea { flex: 1 1 auto; min-height: min(24rem, 40vh); }
.guide-edit-actions { display: flex; justify-content: flex-end; gap: 0.5rem; margin-top: 0.4rem; }
/* Touch devices have no hover, so the hover-revealed section controls would be
   unreachable — keep them visible there, and drop them out of the heading overlap
   onto their own right-aligned row on narrow screens. */
@media (hover: none) {
  .guide-sec-ctrls { opacity: 1; }
}
@media (max-width: 700px) {
  .guide-sec-ctrls { position: static; opacity: 1; justify-content: flex-end; margin-bottom: 0.5rem; }
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
  function openEditor(title, t0, m0, onSave){
    if (!window.uiOpenSimpleModal) return;
    window.uiOpenSimpleModal({title: title, width:'min(1100px, 94vw)', mount: function(body, dlg){
      var tf = fieldText('Section title', t0);
      var mf = fieldArea('Body (markdown)', m0, true);
      body.appendChild(tf.wrap); body.appendChild(mf.wrap);
      var save = el('button', {class:'ui-row-btn primary', text:'Save'});
      var actions = el('div', {class:'guide-edit-actions'}, [save]);
      body.appendChild(actions);
      save.addEventListener('click', function(){
        save.disabled = true; save.textContent = 'Saving…';
        onSave(tf.input.value, mf.input.value).then(function(){
          try { dlg.close(); dlg.remove(); } catch(e){}
          refresh();
        }).catch(function(err){ save.disabled = false; save.textContent = 'Save'; alert('Save failed: ' + (err && err.message || err)); });
      });
    }});
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
      openEditor('Add section', '', '', function(t, m){ return jpost('section/add?' + gp, {title:t, markdown:m}); });
    } else if (act === 'edit'){
      fetch('section?' + gp + sp, {credentials:'same-origin'}).then(function(r){ return r.json(); }).then(function(s){
        openEditor('Edit section', s.title || '', s.markdown || '', function(t, m){ return jpost('section?' + gp + sp, {title:t, markdown:m}); });
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
const guideKnowledgeCSS = `.guide-kn-intro { color: var(--text-mute); font-size: 0.88rem; margin: 0 0 0.9rem; }`

// guideSourcesAction is the 'guides_sources' client action behind the Sources
// toolbar button: a modal that attaches/detaches cross-app reference sources
// (servitor Systems, connected document sources like Confluence) to the open
// guide. The Guide Author's list_reference_sources flags the attached set so it
// builds from them. App-specific, injected via ExtraHeadHTML to keep it out of
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
          intro:'Attach knowledge other gohort services have gathered: your Systems (servitor) and connected document sources (e.g. Confluence). The Guide Author builds the guide from the sources you pick here.',
          empty_text:'No reference sources available yet. Systems appear once you have appliances in the servitor app; document sources appear once connected.'
        }, body);
      }});
}`

// guideSettingsAction is the 'guides_settings' client action behind the Edit
// toolbar button: ONE modal for the guide's name/subtitle, the Private
// (no-internet) flag, AND its sharing (off / view / edit). Owner/admin only
// (server-enforced). App-specific, injected via ExtraHeadHTML to keep it out of
// core/ui.
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
        window.uiOpenSimpleModal({title:'Edit ' + ((d && d.kind === 'article') ? 'article' : 'guide'), width:'520px', mount: function(body, dlg){
          if (!canManage){
            body.appendChild(el('p', {class:'guide-kn-intro', text:'Only the owner can change these settings.'}));
            return;
          }
          var tf = fieldText('Title', (d && d.title) || '');
          var sf = fieldText('Subtitle', (d && d.subtitle) || '');
          body.appendChild(tf.wrap); body.appendChild(sf.wrap);
          // Private (no internet).
          var pcb = el('input', {type:'checkbox'}); if (d && d.private) pcb.checked = true;
          body.appendChild(el('label', {class:'guide-share-row'}, [pcb,
            el('span', {text: "Private — no internet access. The assistant answers and edits only from this document's attached knowledge; web search and research are disabled, and every model call stays on the local worker."})]));
          // Sharing.
          body.appendChild(el('div', {class:'guide-set-head', text:'Sharing'}));
          var scb = el('input', {type:'checkbox'}); if (d && d.shared) scb.checked = true;
          body.appendChild(el('label', {class:'guide-share-row'}, [scb, el('span', {text:'Share with everyone signed in'})]));
          var rView = el('input', {type:'radio', name:'guide-share-mode', value:'view'});
          var rEdit = el('input', {type:'radio', name:'guide-share-mode', value:'edit'});
          if ((d && d.mode) === 'edit') rEdit.checked = true; else rView.checked = true;
          var modeWrap = el('div', {class:'guide-share-modes'}, [
            el('label', {class:'guide-share-mode'}, [rView, el('span', {text:'View only — read & export'})]),
            el('label', {class:'guide-share-mode'}, [rEdit, el('span', {text:'Can edit — edit sections & co-author'})]),
          ]);
          body.appendChild(modeWrap);
          function syncModes(){ modeWrap.style.display = scb.checked ? 'flex' : 'none'; }
          scb.addEventListener('change', syncModes); syncModes();
          var save = el('button', {class:'ui-row-btn primary', text:'Save'});
          body.appendChild(el('div', {class:'guide-edit-actions'}, [save]));
          save.addEventListener('click', function(){
            save.disabled = true; save.textContent = 'Saving…';
            fetch('settings?' + qp, {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'},
              body: JSON.stringify({title: tf.input.value, subtitle: sf.input.value, private: pcb.checked, shared: scb.checked, mode: (rEdit.checked ? 'edit' : 'view')})})
              .then(function(r){ if (!r.ok) throw new Error('HTTP ' + r.status); })
              .then(function(){ try { dlg.close(); dlg.remove(); } catch(e){} if (window.uiInvalidate) window.uiInvalidate('guides'); })
              .catch(function(err){ save.disabled = false; save.textContent = 'Save'; alert('Save failed: ' + (err && err.message || err)); });
          });
        }});
      });
}`

// guideSettingsCSS styles the settings/sharing modal rows.
const guideSettingsCSS = `.guide-share-row { display: flex; align-items: center; gap: 0.55rem; cursor: pointer; padding: 0.5rem 0; font-size: 0.92rem; color: var(--text-hi); }
.guide-share-modes { display: flex; flex-direction: column; gap: 0.4rem; margin: 0.2rem 0 0.3rem 1.6rem; }
.guide-share-mode { display: flex; align-items: center; gap: 0.5rem; cursor: pointer; font-size: 0.88rem; color: var(--text); }
.guide-set-head { font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.06em; color: var(--text-mute); font-weight: 700; margin: 0.9rem 0 0.2rem; border-top: 1px solid var(--border); padding-top: 0.7rem; }`

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
        window.uiOpenSimpleModal({title:'Publish guide', width:'760px', mount: function(body){
          if (!d || !d.configured){
            body.appendChild(el('p', {class:'guide-kn-intro', text:'No publish destinations are configured on this deployment yet. An admin sets them up in Admin > Publishing — a Confluence site, or any endpoint that accepts a posted document.'}));
            return;
          }
          if (!d.can_publish){
            body.appendChild(el('p', {class:'guide-kn-intro', text:'You need edit access to this guide to publish it.'}));
            return;
          }
          // Where it already lives, with a one-click update of that same page.
          (d.published || []).forEach(function(p){
            var row = el('div', {class:'guide-pub-row'});
            var where = p.target_title || p.kind;
            var label = el('div', {class:'guide-pub-where'}, [el('strong', {text: p.title || 'Published'}),
              el('span', {class:'guide-pub-mute', text: ' in ' + where + (p.version ? ' (v' + p.version + ')' : '')})]);
            row.appendChild(label);
            if (p.url){
              var a = el('a', {class:'guide-pub-link', href: p.url, target:'_blank', rel:'noopener', text:'Open'});
              row.appendChild(a);
            }
            var again = el('button', {class:'ui-row-btn', text:'Update'});
            again.addEventListener('click', function(){
              again.disabled = true; again.textContent = 'Updating…';
              fetch('publish/again?' + qp + '&kind=' + encodeURIComponent(p.kind), {method:'POST', credentials:'same-origin'})
                .then(function(r){ return r.text().then(function(t){ if (!r.ok) throw new Error(t || ('HTTP ' + r.status)); return t; }); })
                .then(function(){ again.textContent = 'Updated'; if (window.uiInvalidate) window.uiInvalidate('guides'); })
                .catch(function(err){ again.disabled = false; again.textContent = 'Update';
                  window.uiAlert('Could not update it: ' + (err && err.message || err)); });
            });
            row.appendChild(again);
            body.appendChild(row);
          });
          // The conversation. It opens itself: the agent's first move is to
          // look at what is configured and ask where this should go.
          var host = el('div', {class:'guide-pub-chat'});
          body.appendChild(host);
          window.uiMountComponent({
            type: 'agent_loop_panel',
            send_url: 'publish/chat/send',
            cancel_url: 'chat/cancel',
            markdown: true,
            lock_activity: true,
            auto_send: 'Publish this guide.',
            empty_text: 'Working out where this can go…',
            placeholder: 'Answer the Publisher…',
            submit_label: 'Send'
          }, host);
        }});
      });
}`

// guidePublishCSS styles the "already published here" rows and gives the
// Publisher chat a fixed height inside the modal, so the modal doesn't grow as
// the conversation does.
const guidePublishCSS = `.guide-pub-row { display: flex; align-items: center; gap: 0.6rem; padding: 0.5rem 0.7rem; margin-bottom: 0.5rem; background: var(--bg-2); border: 1px solid var(--border); border-radius: 8px; }
.guide-pub-where { flex: 1; font-size: 0.9rem; color: var(--text-hi); }
.guide-pub-mute { color: var(--text-mute); font-weight: 400; }
.guide-pub-link { color: var(--accent); font-size: 0.85rem; text-decoration: none; }
.guide-pub-link:hover { text-decoration: underline; }
.guide-pub-chat { height: 52vh; min-height: 20rem; display: flex; flex-direction: column; margin-top: 0.4rem; }
.guide-pub-chat > * { flex: 1; min-height: 0; }
@media (max-width: 700px) { .guide-pub-chat { height: 60vh; } }`
