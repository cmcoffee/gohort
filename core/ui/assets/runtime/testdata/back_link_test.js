// Headless harness for the header back link: pull uiArrivedFromInsideTheApp
// out of the epilogue, stub what it reads, and check each arrival it has to
// tell apart.
//
// The point of the function is the FALLBACK. When it cannot prove the reader
// followed a link from inside this deployment, the declared parent has to win,
// because history.back() from a bookmarked page leaves the app entirely or
// does nothing at all — both worse than landing on the page's parent.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../99_epilogue.js', 'utf8');

var start = src.indexOf('function uiArrivedFromInsideTheApp()');
if (start < 0) {
  console.log('FAIL uiArrivedFromInsideTheApp is gone from 99_epilogue.js');
  process.exit(1);
}
var end = src.indexOf('\n  }', start);
var body = src.slice(start, end + 4);

var arrived = new Function('window', 'document', 'history', 'location', 'URL',
  body + '\nreturn uiArrivedFromInsideTheApp;');

function run(opts) {
  var hist = {length: opts.historyLength};
  return arrived(
    {history: opts.noHistoryAPI ? undefined : hist},
    {referrer: opts.referrer},
    hist,
    {href: 'https://gohort.example/page-b', origin: 'https://gohort.example'},
    URL
  )();
}

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

// The case this exists for: one hub page linked to from another. Both declare
// the dashboard as their parent, so only history knows where the reader was.
check('a link from elsewhere in the deployment retraces',
  run({historyLength: 3, referrer: 'https://gohort.example/page-a/'}) === true);

// A bookmark, a pasted link, a fresh tab: no referrer, so no trail.
check('no referrer falls back to the declared parent',
  run({historyLength: 4, referrer: ''}) === false);

// A tab that has been elsewhere first still has history entries, and going
// back into them leaves the app.
check('a first entry in this tab falls back even with a long history',
  run({historyLength: 1, referrer: 'https://gohort.example/page-a/'}) === false);

// Arriving from another site: history.back() would return the reader there.
check('a cross-origin referrer falls back',
  run({historyLength: 5, referrer: 'https://example.com/somewhere'}) === false);

// A referrer that resolves against this page — including a nonsense relative
// one — is same-origin by construction, and treating it as a trail is safe:
// history.back() goes wherever the reader actually was regardless of what the
// referrer string said.
check('a nonsense relative referrer is same-origin and harmless',
  run({historyLength: 5, referrer: '::::'}) === true);

// The catch is not decoration. A click handler that throws leaves the reader
// on a back arrow that does nothing at all, which is worse than a back arrow
// that goes somewhere merely unideal.
var threw = arrived(
  {history: {length: 3}},
  {referrer: 'https://gohort.example/x'},
  {length: 3},
  {href: 'https://gohort.example/page-b', origin: 'https://gohort.example'},
  function() { throw new Error('no URL in this browser'); }
);
var caught = true;
try { check('a throwing URL parser falls back rather than breaking the link', threw() === false); }
catch (_) { caught = false; }
check('the throwing case did not escape the function', caught);

// Defensive: no history API at all.
check('no history API falls back',
  run({historyLength: 3, referrer: 'https://gohort.example/x', noHistoryAPI: true}) === false);

// --- in-page depth ----------------------------------------------------------
//
// The section rail addresses each section with a hash, so every section the
// reader opens is a history entry. Retracing blindly walked those first: the
// header arrow stepped back through the sub-menu they had just been using,
// several presses before it did the one thing it is labelled for.

var depthSrc = src.indexOf('function uiPageDepth()');
if (depthSrc < 0) {
  console.log('FAIL uiPageDepth is gone from 99_epilogue.js');
  process.exit(1);
}
// Pull the three depth helpers out together — they share uiLastPageDepth.
var depthStart = src.indexOf('var uiLastPageDepth = 0;');
var depthEnd = src.indexOf('// uiArrivedFromInsideTheApp');
var depthBody = src.slice(depthStart, depthEnd);

function depthHarness(initialState) {
  var entries = [{state: initialState || null, hash: ''}];
  var idx = 0;
  var hist = {
    get state() { return entries[idx].state; },
    get length() { return entries.length; },
    pushState: function(st, _t, url) {
      entries = entries.slice(0, idx + 1);
      entries.push({state: st, hash: url});
      idx = entries.length - 1;
    },
    replaceState: function(st) { entries[idx].state = st; },
    go: function(n) { idx = Math.max(0, Math.min(entries.length - 1, idx + n)); },
  };
  var win = {history: hist, location: {hash: ''}};
  var api = new Function('window', 'history',
    depthBody + '\nreturn {depth: uiPageDepth, push: uiPushPageStep, stamp: uiStampPageStep};')(win, hist);
  api._entries = function() { return entries; };
  api._at = function() { return idx; };
  api._go = hist.go;
  return api;
}

var d = depthHarness();
check('a freshly loaded page is depth zero', d.depth() === 0);

d.push('credentials');
check('one section opened is depth one', d.depth() === 1);
d.push('tools');
d.push('skills');
check('three sections opened is depth three', d.depth() === 3);

// The number rides the ENTRY, so the browser's own back and forward buttons
// leave it correct. A counter kept on the side cannot do this: nothing tells
// it which direction the reader went.
d._go(-2);
check('walking back two sections reads as depth one', d.depth() === 1);
d._go(1);
check('walking forward again reads as depth two', d.depth() === 2);

// A hash link from inside the CONTENT pushes an entry nobody labelled. Left
// unlabelled it reads as depth zero, and the arrow would land on the previous
// section believing it was the previous page.
var e = depthHarness();
e.push('tools');
e._entries().push({state: null, hash: '#deep'});
e._go(1);
check('an unlabelled entry reads as zero before stamping', e.depth() === 0);
e.stamp();
check('stamping labels it as one deeper than the rail knew', e.depth() === 2);

// Landing directly on a section by link is where this reader STARTED.
var f = depthHarness(null);
check('a deep link is depth zero, so the arrow leaves the page', f.depth() === 0);

process.exit(fail ? 1 : 0);
