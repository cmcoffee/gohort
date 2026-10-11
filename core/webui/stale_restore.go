package webui

// StaleRestoreScript reloads a page the browser restores from its back/forward
// cache when something was saved after the page was drawn.
//
// The back arrow walks history, and a restored page is the page exactly as it
// was left: a card switched on in Customize, or a sub-agent deleted in the
// editor, was missing from the dashboard and the agent picker until a manual
// refresh. no-store does not prevent this; browsers restore no-store pages too.
//
// Any successful same-origin request that is not a GET or HEAD stamps the time
// in localStorage, shared by every page of the deployment. A restored page
// drawn before the last stamp reloads; one drawn after it is still current and
// stays as it was, scroll and all. Saves made in another tab count too.
// Storage that throws (a private window, blocked site data) leaves pages as
// they are, which is the old behavior.
const StaleRestoreScript = `<script>
(function(){
  var KEY = 'gohort_changed_at', drawn = Date.now();
  function stamp() { try { localStorage.setItem(KEY, String(Date.now())); } catch (_) {} }
  if (window.fetch && !window.fetch.__gohortStamps) {
    var plain = window.fetch;
    var wrapped = function(input, init) {
      var p = plain.apply(window, arguments);
      var method = String((init && init.method) || (input && input.method) || 'GET').toUpperCase();
      if (method === 'GET' || method === 'HEAD') return p;
      var url;
      try { url = new URL(typeof input === 'string' ? input : String((input && input.url) || input), location.href); } catch (_) { return p; }
      if (url.origin !== location.origin) return p;
      return p.then(function(r) { if (r && r.ok) stamp(); return r; });
    };
    wrapped.__gohortStamps = true;
    window.fetch = wrapped;
  }
  window.addEventListener('pageshow', function(ev) {
    if (!ev.persisted) return;
    var at = 0;
    try { at = Number(localStorage.getItem(KEY)) || 0; } catch (_) {}
    if (at > drawn) location.reload();
  });
})();
</script>
`
