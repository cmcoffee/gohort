// Headless harness for buildRevisionNav: stub el/fetchJSON/showToast and
// drive the builder the way the two panels do.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../45_document_core.js', 'utf8');

var fetchLog = [], toasts = [], responses = {};
function makeEl() {
  return {
    style: {}, textContent: '', disabled: false, _kids: [], _on: {},
    classList: {add: function(){}, remove: function(){}, contains: function(){return false;}},
    appendChild: function(k){ this._kids.push(k); return k; },
    addEventListener: function(n, f){ this._on[n] = f; },
  };
}
var harness = new Function('el','fetchJSON','showToast',
  src + '\nreturn buildRevisionNav;');

function build(opts) {
  return harness(
    function(tag, attrs, kids) {
      var n = makeEl();
      if (attrs && attrs.onclick) n._on.click = attrs.onclick;
      if (kids) kids.forEach(function(k){ if (typeof k !== 'string') n._kids.push(k); });
      n._label = kids && typeof kids[0] === 'string' ? kids[0] : '';
      return n;
    },
    function(url) {
      fetchLog.push(url);
      return Promise.resolve(responses[url] !== undefined ? responses[url] : []);
    },
    function(m){ toasts.push(m); }
  )(opts);
}

var fail = 0;
function check(label, cond, extra) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label + (extra ? '  ' + extra : '')); }
}
function tick() { return new Promise(function(r){ setImmediate(r); }); }

(async function() {
  // No listURL -> inert, and group is null so callers can append blindly.
  var inert = build({});
  check('no listURL yields null group', inert.group === null);
  inert.reload('x'); inert.clear();

  // Three revisions, sorted oldest-first by date even when served out of order.
  responses['/rev/list/a1'] = [
    {id: 'r3', date: '2026-03-01'},
    {id: 'r1', date: '2026-01-01'},
    {id: 'r2', date: '2026-02-01', label: 'edited'},
  ];
  responses['/rev/load/r2'] = {body: 'BODY-2', subject: 'SUBJ-2'};
  responses['/rev/load/r1'] = {body: 'BODY-1'};

  var loaded = [], saves = 0;
  var nav = build({
    listURL: '/rev/list/{id}',
    loadURL: '/rev/load/{revid}',
    onLoad: function(rev){ loaded.push(rev); },
    onMakeCurrent: function(){ saves++; },
  });
  var back = nav.group._kids[1], ind = nav.group._kids[2], fwd = nav.group._kids[3];
  var make = nav.group._kids[0];

  check('group hidden with no revisions', nav.group.style.display === 'none');

  nav.reload('a1');
  await tick();
  check('list URL substituted', fetchLog[0] === '/rev/list/a1', fetchLog[0]);
  check('group visible after load', nav.group.style.display === 'inline-flex');
  check('lands on newest', ind.textContent === 'rev 3/3', ind.textContent);
  check('forward disabled at newest', fwd.disabled === true);
  check('back enabled at newest', back.disabled === false);
  check('make-current hidden at newest', make.style.display === 'none');

  // Walk back one: r2 by date order, and its label shows.
  back._on.click();
  await tick();
  check('loads by revision id', fetchLog[1] === '/rev/load/r2', fetchLog[1]);
  check('onLoad got the record', loaded.length === 1 && loaded[0].body === 'BODY-2');
  check('indicator shows position', ind.textContent.indexOf('rev 2/3') === 0, ind.textContent);
  check('label appended when present', ind.textContent.indexOf('edited') > 0, ind.textContent);
  check('make-current shown on older rev', make.style.display === 'inline-flex');

  // Back to oldest: back must disable, forward must enable.
  back._on.click();
  await tick();
  check('reached oldest', ind.textContent === 'rev 1/3', ind.textContent);
  check('back disabled at oldest', back.disabled === true);
  check('forward enabled at oldest', fwd.disabled === false);

  // Past the start is a no-op, not a crash or a stray fetch.
  var before = fetchLog.length;
  back._on.click();
  await tick();
  check('navigating past oldest is a no-op', fetchLog.length === before);

  // Make-current delegates to the host's save.
  make._on.click();
  check('make-current calls onMakeCurrent', saves === 1);

  // clear() resets and hides.
  nav.clear();
  check('clear hides the group', nav.group.style.display === 'none');
  check('clear empties the indicator', ind.textContent === '');

  // reload(null) clears rather than fetching.
  before = fetchLog.length;
  nav.reload('');
  check('reload with no id does not fetch', fetchLog.length === before);

  // The other placeholder spelling must work too — codewriter uses {id}.
  var nav2 = build({
    listURL: '/rev/list/{id}', loadURL: '/rev/load/{id}',
    onLoad: function(){},
  });
  nav2.reload('a1');
  await tick();
  nav2.group._kids[1]._on.click();
  await tick();
  check('{id} placeholder in loadURL also substituted',
    fetchLog[fetchLog.length - 1] === '/rev/load/r2', fetchLog[fetchLog.length - 1]);

  console.log(fail === 0 ? '\nALL REVISION-NAV TESTS PASS' : '\n' + fail + ' FAILURES');
  process.exit(fail ? 1 : 0);
})();
