// The Guides workbench page: guide list (left) | rendered HTML document with a
// table of contents (center) | Guide Author chat (right). Built from the core/ui
// WorkbenchPanel primitive; the document styling rides in via ExtraHeadHTML so a
// guide reads like a formatted document.
package guides

import (
	"net/http"

	"github.com/cmcoffee/gohort/core/ui"
)

func (T *Guides) servePage(w http.ResponseWriter, r *http.Request) {
	wb := ui.WorkbenchPanel{
		// Left — guide list + New.
		ListURL:   "guides",
		ItemKey:   "id",
		ItemLabel: "title",
		ListTitle: "Guides",
		ListEmpty: "No guides yet — create one.",
		DeleteURL: "guide?id={id}",
		NewButton: ui.ModalButton{
			Label: "New",
			Title: "New guide",
			Body: ui.FormPanel{
				PostURL:     "new",
				SubmitLabel: "Create guide",
				Fields: []ui.FormField{
					{Field: "title", Label: "Title", Type: "text", Placeholder: "e.g. Getting Started with Kubernetes"},
					{Field: "subtitle", Label: "Subtitle", Type: "text", Placeholder: "Optional one-line description"},
				},
				Invalidate: []string{"guides"},
			},
		},
		// Edit the selected guide's settings — sits in the list header, left of New.
		ListActions: []ui.WorkbenchAction{
			{Label: "Edit", Kind: "client", URL: "guides_settings"},
		},
		// Center — the rendered document (server HTML: title + ToC + sections).
		RecordURL:  "guide?id={id}",
		BodyField:  "html",
		BodyIsHTML: true,
		EmptyIcon:  "📖",
		EmptyTitle: "No guide selected",
		EmptyHint:  "Pick a guide on the left, or create one. Then ask the assistant to draft sections.",
		// Per-document toolbar: preview/export, revision history, freshness audit.
		ViewerActions: []ui.WorkbenchAction{
			{Label: "Export", Kind: "menu", Children: []ui.WorkbenchAction{
				{Label: "Preview (HTML)", Kind: "download", URL: "export?id={id}&format=html"},
				{Label: "PDF", Kind: "download", URL: "export?id={id}&format=pdf"},
				{Label: "Markdown", Kind: "download", URL: "export?id={id}&format=md"},
			}},
			{Label: "Sources", Kind: "client", URL: "guides_sources"},
			{Label: "Knowledge", Kind: "client", URL: "guides_knowledge"},
			{Label: "History", Kind: "history", URL: "revisions?id={id}", RestoreURL: "restore?id={id}&rev={rev}"},
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
			SendURL:      "chat/send",
			CancelURL:    "chat/cancel",
			Markdown:     true,
			LockActivity: true,
			EmptyText:    "Ask me to draft or revise a section — e.g. \"Add an introduction\" or \"Expand the setup section.\"",
			Placeholder:  "Ask the Guide Author…",
		},
	}

	page := ui.Page{
		Title:         "Guides",
		ShowTitle:     true,
		BackURL:       "/",
		MaxWidth:      "100%",
		Sections:      []ui.Section{{NoChrome: true, Body: wb}},
		ExtraHeadHTML: guideDocCSS + guideSectionCtrlCSS + guideSectionJS + guideKnowledgeJS + guideSourcesJS + guideSettingsJS,
	}
	page.ServeHTTP(w, r)
}

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

// guideSectionJS wires the inline section controls (rendered server-side in
// renderGuideHTML with data-guide-act attributes) to the section endpoints, then
// refreshes the WorkbenchPanel viewer via uiInvalidate. App-specific behavior,
// injected through ExtraHeadHTML so it stays out of the domain-agnostic core/ui.
// One delegated listener handles edit / move / delete / add for the live viewer
// (which re-renders, so per-element handlers would not survive).
const guideSectionJS = `<script>
(function(){
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
  function fieldArea(label, value){
    var ta = el('textarea'); ta.value = value || '';
    return {wrap: el('div', {class:'guide-edit-field'}, [el('label', {text: label}), ta]), input: ta};
  }
  function openEditor(title, t0, m0, onSave){
    if (!window.uiOpenSimpleModal) return;
    window.uiOpenSimpleModal({title: title, width:'680px', mount: function(body, dlg){
      var tf = fieldText('Section title', t0);
      var mf = fieldArea('Body (markdown)', m0);
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
})();
</script>`

// guideKnowledgeJS registers the 'guides_knowledge' client action behind the
// Knowledge toolbar button: a modal that attaches/detaches knowledge collections
// to the open guide (the set the Guide Author's search_knowledge tool consults).
// App-specific, injected via ExtraHeadHTML so the picker stays out of core/ui.
const guideKnowledgeJS = `<script>
(function(){
  function el(tag, attrs, kids){
    var n = document.createElement(tag);
    if (attrs) for (var k in attrs){ if (k === 'text') n.textContent = attrs[k]; else n.setAttribute(k, attrs[k]); }
    (kids||[]).forEach(function(c){ n.appendChild(typeof c === 'string' ? document.createTextNode(c) : c); });
    return n;
  }
  function register(){
    if (!window.uiRegisterClientAction) return;
    window.uiRegisterClientAction('guides_knowledge', function(ctx){
      var gid = ctx.recordId;
      if (!gid || !window.uiOpenSimpleModal) return;
      var qp = 'guide=' + encodeURIComponent(gid);
      fetch('collections?' + qp, {credentials:'same-origin'}).then(function(r){ return r.json(); }).then(function(d){
        var available = (d && d.available) || [];
        var attached = {};
        ((d && d.attached) || []).forEach(function(id){ attached[id] = true; });
        window.uiOpenSimpleModal({title:'Attach knowledge', width:'560px', mount: function(body, dlg){
          body.appendChild(el('p', {class:'guide-kn-intro', text:'Pick the knowledge collections the Guide Author can search while drafting this guide. It searches them with the search_knowledge tool to ground sections in your own material.'}));
          if (!available.length){
            body.appendChild(el('div', {class:'guide-kn-empty', text:'You have no knowledge collections yet. Create one in the Knowledge app, then attach it here.'}));
            return;
          }
          var boxes = [];
          var listWrap = el('div', {class:'guide-kn-list'});
          available.forEach(function(c){
            var cb = el('input', {type:'checkbox'}); cb.value = c.id; if (attached[c.id]) cb.checked = true;
            boxes.push(cb);
            var label = el('label', {class:'guide-kn-item'}, [cb,
              el('span', {class:'guide-kn-name', text: c.name || c.id})]);
            if (c.description) label.appendChild(el('span', {class:'guide-kn-desc', text: c.description}));
            listWrap.appendChild(label);
          });
          body.appendChild(listWrap);
          var save = el('button', {class:'ui-row-btn primary', text:'Save'});
          body.appendChild(el('div', {class:'guide-edit-actions'}, [save]));
          save.addEventListener('click', function(){
            var ids = boxes.filter(function(b){ return b.checked; }).map(function(b){ return b.value; });
            save.disabled = true; save.textContent = 'Saving…';
            fetch('collections?' + qp, {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'}, body: JSON.stringify({collections: ids})})
              .then(function(r){ if (!r.ok) throw new Error('HTTP ' + r.status); })
              .then(function(){ try { dlg.close(); dlg.remove(); } catch(e){} })
              .catch(function(err){ save.disabled = false; save.textContent = 'Save'; alert('Save failed: ' + (err && err.message || err)); });
          });
        }});
      });
    });
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', register); else register();
})();
</script>
<style>
.guide-kn-intro { color: var(--text-mute); font-size: 0.88rem; margin: 0 0 0.9rem; }
.guide-kn-empty { color: var(--text-mute); font-style: italic; padding: 0.5rem 0; }
.guide-kn-list { display: flex; flex-direction: column; gap: 0.5rem; max-height: 22rem; overflow-y: auto; }
.guide-kn-item { display: grid; grid-template-columns: auto 1fr; gap: 0.1rem 0.55rem; align-items: center; cursor: pointer; padding: 0.4rem 0.5rem; border: 1px solid var(--border); border-radius: 8px; }
.guide-kn-item:hover { border-color: var(--accent); }
.guide-kn-name { font-weight: 600; color: var(--text-hi); font-size: 0.92rem; }
.guide-kn-desc { grid-column: 2; color: var(--text-mute); font-size: 0.8rem; }
</style>`

// guideSourcesJS registers the 'guides_sources' client action behind the Sources
// toolbar button: a modal that attaches/detaches cross-app reference sources
// (servitor Systems, connected document sources like Confluence) to the open
// guide. The Guide Author's list_reference_sources flags the attached set so it
// builds from them. App-specific, injected via ExtraHeadHTML to keep it out of
// core/ui. Reuses the guide-kn-* picker styles plus a group header.
const guideSourcesJS = `<script>
(function(){
  function el(tag, attrs, kids){
    var n = document.createElement(tag);
    if (attrs) for (var k in attrs){ if (k === 'text') n.textContent = attrs[k]; else n.setAttribute(k, attrs[k]); }
    (kids||[]).forEach(function(c){ n.appendChild(typeof c === 'string' ? document.createTextNode(c) : c); });
    return n;
  }
  function register(){
    if (!window.uiRegisterClientAction) return;
    window.uiRegisterClientAction('guides_sources', function(ctx){
      var gid = ctx.recordId;
      if (!gid || !window.uiOpenSimpleModal) return;
      var qp = 'guide=' + encodeURIComponent(gid);
      fetch('references?' + qp, {credentials:'same-origin'}).then(function(r){ return r.json(); }).then(function(d){
        var groups = (d && d.groups) || [];
        var attached = {};
        ((d && d.attached) || []).forEach(function(s){ attached[s.kind + '::' + s.item_id] = true; });
        window.uiOpenSimpleModal({title:'Guide sources', width:'560px', mount: function(body, dlg){
          body.appendChild(el('p', {class:'guide-kn-intro', text:'Attach knowledge other gohort services have gathered: your Systems (servitor) and connected document sources (e.g. Confluence). The Guide Author builds the guide from the sources you pick here.'}));
          if (!groups.length){
            body.appendChild(el('div', {class:'guide-kn-empty', text:'No reference sources available yet. Systems appear once you have appliances in the servitor app; document sources appear once connected.'}));
            return;
          }
          var boxes = [];
          var listWrap = el('div', {class:'guide-kn-list'});
          groups.forEach(function(g){
            listWrap.appendChild(el('div', {class:'guide-src-group', text: g.label}));
            (g.items||[]).forEach(function(it){
              var key = g.kind + '::' + it.id;
              var cb = el('input', {type:'checkbox'}); cb.value = key; cb._kind = g.kind; cb._item = it.id;
              if (attached[key]) cb.checked = true;
              boxes.push(cb);
              var label = el('label', {class:'guide-kn-item'}, [cb,
                el('span', {class:'guide-kn-name', text: it.name || it.id})]);
              if (it.desc) label.appendChild(el('span', {class:'guide-kn-desc', text: it.desc}));
              listWrap.appendChild(label);
            });
          });
          body.appendChild(listWrap);
          var save = el('button', {class:'ui-row-btn primary', text:'Save'});
          body.appendChild(el('div', {class:'guide-edit-actions'}, [save]));
          save.addEventListener('click', function(){
            var refs = boxes.filter(function(b){ return b.checked; }).map(function(b){ return {kind: b._kind, item_id: b._item}; });
            save.disabled = true; save.textContent = 'Saving…';
            fetch('references?' + qp, {method:'POST', credentials:'same-origin', headers:{'Content-Type':'application/json'}, body: JSON.stringify({references: refs})})
              .then(function(r){ if (!r.ok) throw new Error('HTTP ' + r.status); })
              .then(function(){ try { dlg.close(); dlg.remove(); } catch(e){} })
              .catch(function(err){ save.disabled = false; save.textContent = 'Save'; alert('Save failed: ' + (err && err.message || err)); });
          });
        }});
      });
    });
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', register); else register();
})();
</script>
<style>
.guide-src-group { font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.05em; color: var(--text-mute); font-weight: 700; margin: 0.5rem 0 0.1rem; }
.guide-src-group:first-child { margin-top: 0; }
</style>`

// guideSettingsJS registers the 'guides_settings' client action behind the Edit
// toolbar button: ONE modal for the guide's name/subtitle, the Private
// (no-internet) flag, AND its sharing (off / view / edit). Owner/admin only
// (server-enforced). App-specific, injected via ExtraHeadHTML to keep it out of
// core/ui.
const guideSettingsJS = `<script>
(function(){
  function el(tag, attrs, kids){
    var n = document.createElement(tag);
    if (attrs) for (var k in attrs){ if (k === 'text') n.textContent = attrs[k]; else n.setAttribute(k, attrs[k]); }
    (kids||[]).forEach(function(c){ n.appendChild(typeof c === 'string' ? document.createTextNode(c) : c); });
    return n;
  }
  function fieldText(label, value){
    var inp = el('input', {type:'text', value: value || ''});
    inp.style.width = '100%'; inp.style.padding = '0.45rem 0.6rem'; inp.style.background = 'var(--bg-0)';
    inp.style.color = 'var(--text)'; inp.style.border = '1px solid var(--border)'; inp.style.borderRadius = '6px';
    var wrap = el('div', {class:'guide-edit-field'}, [el('label', {text: label}), inp]);
    return {wrap: wrap, input: inp};
  }
  function register(){
    if (!window.uiRegisterClientAction) return;
    window.uiRegisterClientAction('guides_settings', function(ctx){
      var gid = ctx.recordId;
      if (!gid || !window.uiOpenSimpleModal) return;
      var qp = 'id=' + encodeURIComponent(gid);
      fetch('settings?' + qp, {credentials:'same-origin'}).then(function(r){ return r.json(); }).then(function(d){
        var canManage = !!(d && d.can_manage);
        window.uiOpenSimpleModal({title:'Edit guide', width:'520px', mount: function(body, dlg){
          if (!canManage){
            body.appendChild(el('p', {class:'guide-kn-intro', text:'Only the guide owner can change these settings.'}));
            return;
          }
          var tf = fieldText('Title', (d && d.title) || '');
          var sf = fieldText('Subtitle', (d && d.subtitle) || '');
          body.appendChild(tf.wrap); body.appendChild(sf.wrap);
          // Private (no internet).
          var pcb = el('input', {type:'checkbox'}); if (d && d.private) pcb.checked = true;
          body.appendChild(el('label', {class:'guide-share-row'}, [pcb,
            el('span', {text: "Private — no internet access. The assistant answers and edits only from this guide's attached knowledge; web search and research are disabled."})]));
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
    });
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', register); else register();
})();
</script>
<style>
.guide-share-row { display: flex; align-items: center; gap: 0.55rem; cursor: pointer; padding: 0.5rem 0; font-size: 0.92rem; color: var(--text-hi); }
.guide-share-modes { display: flex; flex-direction: column; gap: 0.4rem; margin: 0.2rem 0 0.3rem 1.6rem; }
.guide-share-mode { display: flex; align-items: center; gap: 0.5rem; cursor: pointer; font-size: 0.88rem; color: var(--text); }
.guide-set-head { font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.06em; color: var(--text-mute); font-weight: 700; margin: 0.9rem 0 0.2rem; border-top: 1px solid var(--border); padding-top: 0.7rem; }
</style>`
