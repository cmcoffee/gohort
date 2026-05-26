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

    window.uiRegisterClientAction('techwriter_reprocess', function(ctx) {
      var ed = ctx.editor;
      if (!ed.getBody().trim()) { ed.toast('Write or paste something first'); return; }
      if (!ed.confirm('Reprocess the current body? The article will be replaced with the assistant\'s polished version.')) return;
      ed.busy(ctx.button, 'Reprocessing…');
      var fixedMsg =
        'Process this article: fill in missing context, expand bare commands, ' +
        'ensure it follows the markdown renderer rules from the system prompt, and polish for ' +
        'clarity. Keep all facts, commands, and citations intact.';
      fetch(urlFor('api/chat'), {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({
          subject: ed.getTitle(),
          body:    ed.getBody(),
          message: fixedMsg,
          mode:    'edit',
          history: [],
        }),
      }).then(function(r) { return r.json(); }).then(function(d) {
        ed.restore(ctx.button);
        if (!d) { ed.toast('Empty response'); return; }
        if (d.error) { ed.toast('Error: ' + d.error); return; }
        if (d.type === 'article' && d.content) {
          ed.setBody(d.content);
          if (d.title) ed.setTitle(d.title);
          // Auto-save so the rewrite picks up a revision entry.
          // Without it, navigating away loses the result entirely.
          ed.save();
          ed.toast('Reprocessed and saved — use ◀ to revert if needed');
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
      // Open the popup synchronously inside the click handler so
      // it isn't blocked, then write into it once the preview
      // resolves. Using document.write into a real popup avoids
      // the blob:URL anchor-scroll bug (Chrome/Firefox treat
      // blob:URL#fragment as a navigation, not a same-page
      // scroll, so TOC links don't jump inside a blob preview).
      var win = window.open('', '_blank');
      if (!win) { ed.toast('Preview blocked by browser'); return; }
      win.document.write('<!DOCTYPE html><title>Loading preview…</title>' +
        '<body style="font-family:sans-serif;padding:2rem;color:#666">' +
        'Generating preview…</body>');
      fetch(urlFor('api/preview'), {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({subject: subject, body: body, image_url: ed.getImage() || ''}),
      }).then(function(r) { return r.text(); }).then(function(html) {
        win.document.open();
        win.document.write(html);
        win.document.close();
      }).catch(function(err) {
        try {
          win.document.open();
          win.document.write('<body style="font-family:sans-serif;padding:2rem;color:#c00">' +
            'Preview failed: ' + (err && err.message || err) + '</body>');
          win.document.close();
        } catch (_) {}
        ed.toast('Preview failed: ' + (err && err.message || err));
      });
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
