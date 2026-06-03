// App-specific toolbar handlers registered as core/ui client
// actions. These flows are techwriter-specific (they know the chat
// endpoint's body shape, the suggest-title response, the preview
// pipeline, etc.) and intentionally live in this package — the
// shared runtime does not embed knowledge of them. The page's
// ExtraHeadHTML loads this <script> on every techwriter page mount;
// the registered names match the URL fields on the ToolbarAction
// entries in page.go.

package techwriter

// twWebAssets is loaded into the techwriter page's <head>. It
// registers techwriter's named toolbar callbacks against the
// core/ui client-action registry. The runtime injects an editor
// handle (ed) carrying read/write access to body/title/image plus
// helpers (save, toast, busy/restore, confirm, appendAssistant).
//
// Anything techwriter-specific — endpoint URLs, request body
// shape, prompt strings, success copy — lives below. Keep it
// here so that "what reprocess does" reads as one coherent block,
// not a method scattered across the framework.
const twWebAssets = `<script>
(function() {
  function register() {
    if (!window.uiRegisterClientAction) return;
    var prefix = (window.location.pathname.match(/^(\/[^/]+)\//) || [,'/techwriter'])[1];
    function urlFor(suffix) { return prefix + '/' + suffix.replace(/^\//, ''); }

    // In-app preview overlay — used when window.open isn't available (the
    // gohort-desktop WKWebView returns null for popups, so the old "blocked
    // by browser" path meant Preview just didn't work there). Renders the
    // preview HTML in a sandboxed iframe so its styles/scripts stay isolated.
    function showPreviewOverlay(html) {
      var prev = document.getElementById('tw-preview-overlay');
      if (prev) prev.remove();
      var overlay = document.createElement('div');
      overlay.id = 'tw-preview-overlay';
      overlay.style.cssText = 'position:fixed;inset:0;z-index:2147483646;background:rgba(0,0,0,0.6);display:flex;flex-direction:column';
      var bar = document.createElement('div');
      bar.style.cssText = 'display:flex;align-items:center;gap:8px;padding:8px 12px;background:#0d1117;border-bottom:1px solid #30363d;font:13px sans-serif';
      var label = document.createElement('div');
      label.textContent = 'Preview';
      label.style.cssText = 'flex:1;color:#c9d1d9;font-weight:600';
      var closeBtn = document.createElement('button');
      closeBtn.textContent = 'Close';
      closeBtn.style.cssText = 'padding:6px 14px;border-radius:6px;cursor:pointer;border:1px solid #3a6ea5;background:#3a6ea5;color:#fff';
      bar.appendChild(label);
      bar.appendChild(closeBtn);
      var frame = document.createElement('iframe');
      frame.style.cssText = 'flex:1;width:100%;border:0;background:#fff';
      frame.setAttribute('sandbox', 'allow-scripts allow-same-origin');
      frame.srcdoc = html;
      overlay.appendChild(bar);
      overlay.appendChild(frame);
      function teardown() { overlay.remove(); document.removeEventListener('keydown', onKey, true); }
      function onKey(e) { if (e.key === 'Escape') { e.preventDefault(); teardown(); } }
      document.addEventListener('keydown', onKey, true);
      closeBtn.onclick = teardown;
      (document.body || document.documentElement).appendChild(overlay);
    }

    window.uiRegisterClientAction('techwriter_reprocess', function(ctx) {
      var ed = ctx.editor;
      if (!ed.getBody().trim()) { ed.toast('Write or paste something first'); return; }
      if (!ed.confirm('Reprocess? Your current draft is saved first, then replaced with a polished version — use ◀ to revert to the original.')) return;
      var fixedMsg =
        'Process this article: fill in missing context, expand bare commands, ' +
        'ensure it follows the markdown renderer rules from the system prompt, and polish for ' +
        'clarity. Keep all facts, commands, and citations intact.';
      // Save the ORIGINAL first so reprocess is a SECOND draft, not a
      // destructive overwrite. If the user never titled it, auto-title the
      // original content (so the saved draft + its revision are
      // identifiable). The save fires before the multi-second LLM rewrite,
      // so the original is persisted (and its id assigned) well before the
      // reprocessed result lands as the next revision.
      ed.busy(ctx.button, 'Saving draft…');
      var titled = ed.getTitle().trim()
        ? Promise.resolve()
        : fetch(urlFor('api/suggest-title'), {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({body: ed.getBody()}),
          }).then(function(r) { return r.json(); })
            .then(function(d) { if (d && d.subject) ed.setTitle(d.subject); })
            .catch(function() {});
      titled.then(function() {
        ed.save(); // persist the original — becomes the first revision
        ed.busy(ctx.button, 'Reprocessing…');
        return fetch(urlFor('api/chat'), {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({
            subject: ed.getTitle(),
            body:    ed.getBody(),
            message: fixedMsg,
            mode:    'edit',
            history: [],
          }),
        });
      }).then(function(r) { return r.json(); }).then(function(d) {
        ed.restore(ctx.button);
        if (!d) { ed.toast('Empty response'); return; }
        if (d.error) { ed.toast('Error: ' + d.error); return; }
        if (d.type === 'article' && d.content) {
          ed.setBody(d.content);
          if (d.title) ed.setTitle(d.title);
          ed.save(); // the reprocessed result — saved as the next revision
          ed.toast('Reprocessed — your original is saved; use ◀ to revert');
        } else {
          ed.appendAssistant('assistant', d.content || '(no rewrite produced)');
        }
      }).catch(function(err) {
        ed.restore(ctx.button);
        ed.toast('Reprocess failed: ' + (err && err.message || err));
      });
    });

    window.uiRegisterClientAction('techwriter_suggest_title', function(ctx) {
      var ed = ctx.editor;
      if (!ed.getBody().trim()) { ed.toast('Write something first'); return; }
      ed.busy(ctx.button, 'Suggesting…');
      fetch(urlFor('api/suggest-title'), {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({body: ed.getBody()}),
      }).then(function(r) { return r.json(); }).then(function(d) {
        ed.restore(ctx.button);
        if (d && d.subject) {
          ed.setTitle(d.subject);
          ed.toast('Title suggested — Save to keep');
        }
      }).catch(function(err) {
        ed.restore(ctx.button);
        ed.toast('Failed: ' + (err && err.message || err));
      });
    });

    window.uiRegisterClientAction('techwriter_generate_image', function(ctx) {
      var ed = ctx.editor;
      var title = ed.getTitle().trim();
      if (!title) { ed.toast('Add a title first — the image is generated from it'); return; }
      // Banner-style without rendered text — image models still
      // misspell/drop typography even for short titles, so the
      // title becomes a subject descriptor only. The article's
      // title still appears separately above the image in
      // export/preview.
      var prompt =
        'A wide banner-style header image evoking the topic: "' + title + '". ' +
        'Visual style: clean, professional, editorial / magazine quality, ' +
        'suitable as a top-of-article banner. Wide aspect ratio (around 3:1). ' +
        'Do NOT render any text, words, letters, captions, or typography in ' +
        'the image — purely visual.';
      ed.busy(ctx.button, 'Generating…');
      fetch(urlFor('api/generate-image'), {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({prompt: prompt}),
      }).then(function(r) { return r.json(); }).then(function(d) {
        ed.restore(ctx.button);
        var url = d && (d.url || d.image_url || d.data);
        if (!url) { ed.toast('No image returned'); return; }
        ed.setImage(url);
        // Auto-save so the image sticks (and shows up in
        // export/preview without a manual Save click).
        ed.save();
      }).catch(function(err) {
        ed.restore(ctx.button);
        ed.toast('Failed: ' + (err && err.message || err));
      });
    });

    window.uiRegisterClientAction('techwriter_preview', function(ctx) {
      var ed = ctx.editor;
      var subject = ed.getTitle().trim();
      var body    = ed.getBody();
      if (!body.trim()) { ed.toast('Write something first'); return; }
      // Real browsers: open a popup synchronously inside the click handler
      // (so it isn't blocked) and document.write into it — avoids the
      // blob:URL anchor-scroll bug (Chrome/Firefox treat blob:#frag as a
      // navigation, not a same-page scroll, so TOC links don't jump). The
      // gohort-desktop WKWebView returns null for window.open, so fall back
      // to an in-app iframe overlay there.
      var payload = JSON.stringify({subject: subject, body: body, image_url: ed.getImage() || ''});
      var win = window.open('', '_blank');
      if (win) {
        win.document.write('<!DOCTYPE html><title>Loading preview…</title>' +
          '<body style="font-family:sans-serif;padding:2rem;color:#666">Generating preview…</body>');
        fetch(urlFor('api/preview'), {method: 'POST', headers: {'Content-Type': 'application/json'}, body: payload})
          .then(function(r) { return r.text(); })
          .then(function(html) { win.document.open(); win.document.write(html); win.document.close(); })
          .catch(function(err) {
            try { win.document.open(); win.document.write('<body style="font-family:sans-serif;padding:2rem;color:#c00">Preview failed: ' + (err && err.message || err) + '</body>'); win.document.close(); } catch (_) {}
            ed.toast('Preview failed: ' + (err && err.message || err));
          });
        return;
      }
      // No popup available (gohort-desktop WKWebView) — render in-app.
      ed.toast('Opening preview…');
      fetch(urlFor('api/preview'), {method: 'POST', headers: {'Content-Type': 'application/json'}, body: payload})
        .then(function(r) { return r.text(); })
        .then(function(html) { showPreviewOverlay(html); })
        .catch(function(err) { ed.toast('Preview failed: ' + (err && err.message || err)); });
    });

    window.uiRegisterClientAction('techwriter_export', function(ctx) {
      var ed = ctx.editor;
      var id = ed.getID();
      if (!id) { ed.toast('Save the article first'); return; }
      // Trigger a download via a synthetic anchor click. Browser
      // handles the rest; no UI state to flip.
      var a = document.createElement('a');
      a.href = urlFor('api/export/' + encodeURIComponent(id));
      a.target = '_blank';
      a.rel = 'noopener';
      a.click();
    });
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', register);
  } else {
    register();
  }
})();
</script>`
