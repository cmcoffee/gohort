package knowledge

// collectionWatchJS lets the collection page's documents list hear what the
// framework parts of the page change.
//
// The documents list is drawn by the page's own script, not by a framework
// table, so it never heard the data-changed event the rest of the page uses.
// A curation run (Run now, or a scheduled run the page rejoined) announces
// its end on its own source, as do the held pages and the plan, all under
// the collection's address; the list stayed as it was until a reload. Now a
// change under that address redraws it, once, after the changes settle.
const collectionWatchJS = `<script>
window.knowledgeWatchCollection = function(prefix, reload) {
  var timer = null;
  window.addEventListener('ui-data-changed', function(ev) {
    var sources = (ev && ev.detail && ev.detail.sources) || [];
    var ours = sources.some(function(s) { return typeof s === 'string' && s.indexOf(prefix) === 0; });
    if (!ours) return;
    if (timer) clearTimeout(timer);
    timer = setTimeout(function() { timer = null; reload(); }, 400);
  });
};
</script>`
