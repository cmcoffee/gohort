// Threads (cfg.threads_url): the rail's second view, behind two tabs on a row
// of their own under its header, lists the agent's own conversations apart
// from its sessions. A dot on the Threads tab says one has something new
// while the rail shows sessions.
// The list groups them under headings, every thread shown (no group folds
// away); a thread opens like a session, its composer locked.
// The shipped functions are run here over a stub page.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var from = src.indexOf('    // setRailView switches the rail');
var to = src.indexOf('    function loadSessions() {');
check('the threads functions are in the panel', from > 0 && to > from);
if (!(from > 0 && to > from)) process.exit(1);

function el(tag, opts, children) {
  var n = {tag: tag, attrs: {}, children: [], style: {display: ''}, classList: {
      on: {}, toggle: function(c, v) { this.on[c] = !!v; }, add: function(c) { this.on[c] = true; }},
    listeners: {},
    setAttribute: function(k, v) { this.attrs[k] = String(v); },
    appendChild: function(c) { this.children.push(c); return c; },
    addEventListener: function(ev, fn) { this.listeners[ev] = fn; }};
  Object.defineProperty(n, 'textContent', {
    get: function() { return this.children.map(function(c) { return typeof c === 'string' ? c : c.textContent; }).join(''); },
    set: function(v) { this.children = [String(v)]; }});
  Object.defineProperty(n, 'innerHTML', {set: function() { this.children = []; }});
  Object.defineProperty(n, 'firstChild', {get: function() { return this.children[0]; }});
  for (var k in (opts || {})) {
    if (k === 'class') n.className = opts[k];
    else if (k === 'style') n.style.cssText = opts[k];
    else if (k === 'text') n.children = [opts[k]];
    else if (k === 'onclick') n.onclick = opts[k];
    else n.attrs[k] = opts[k];
  }
  (children || []).forEach(function(c) { if (c != null) n.children.push(c); });
  return n;
}

var threads = [
  {id: 'mcp-desktop:k1:review', title: 'Check test review', group: 'Over MCP', with: 'Claude Code', unread: true},
  {id: 'mcp-desktop:k1', title: 'Over MCP', group: 'Over MCP'},
  {id: 'dispatch:s1:a1', title: 'Hand-off', group: 'Hand-offs', with: 'handed over by another agent'},
];
var harness = new Function('el', 'cfg', 'list', 'opened', 'locked',
  'var railView = "sessions", threadIds = {}, activeSessionId = "", sideList = el("div"), sideSearch = null;\n' +
  'var threadsDot = el("span"), threadsBtn = el("button", {}, [el("span", {}, ["Threads"]), threadsDot]), sessionsTab = el("button", {}, ["Sessions"]);\n' +
  'var sideHdrLabel = el("span", {}, ["Sessions"]);\n' +
  'function fetchJSON() { return Promise.resolve(list); }\n' +
  'function substituteExtras(u) { return u; }\n' +
  'function openSession(id) { opened.push(id); activeSessionId = id; }\n' +
  'function closeDrawer() {}\n' +
  'function currentAgentLabel() { return "Scout"; }\n' +
  'function applyRecordLock(id) { locked.push(id); }\n' +
  'function loadSessions() { refreshThreads(); }\n' +
  'var window = {uiConfirm: function() { return Promise.resolve(false); }};\n' +
  src.slice(from, to) +
  '\nreturn {refresh: refreshThreads, view: setRailView, side: sideList, dot: threadsDot, btn: threadsBtn, hdr: sideHdrLabel,' +
  ' tab: sessionsTab, ids: function() { return threadIds; }, open: function(id) { activeSessionId = id; }, transcript: transcriptFor};');

var opened = [], locked = [];
var r = harness(el, {threads_url: 'api/threads'}, threads, opened, locked);
check('the tab row keeps clear of the header above it, and is not squeezed',
  /class: 'ui-rail-tabs'[^}]*flex:0 0 auto;margin:0\.45rem /.test(src));
check('the tabs are marked as they are built, Sessions first',
  /\[sessionsTab, threadsBtn\]\);\s*markRailTabs\(\);/.test(src));
r.refresh();
setTimeout(function() {
  check('the threads are known by id once read', !!r.ids()['mcp-desktop:k1'] && !!r.ids()['dispatch:s1:a1']);
  check('a new thread shows the dot while the rail shows sessions', r.dot.style.display === 'inline-block');
  check('the sessions are left as they are', r.side.children.length === 0);
  var tr = r.transcript('mcp-desktop:k1:review', 'Check test review');
  check('an opened thread names its lines: the caller, and the agent replying',
    !!tr && tr.contact === 'Claude Code' && tr.agent === 'Scout');
  var room = r.transcript('chan:42', 'Ann');
  check('a channel room is still named by its contact', !!room && room.contact === 'Ann' && room.agent === 'Scout');
  check('a plain session stays anonymous', r.transcript('s-123', 'My chat') === null);
  check('the open view takes its labels from transcriptFor',
    /channelTranscript = transcriptFor\(sid, rec && rec\[ttlF\]\);/.test(src));

  r.view('threads');
  setTimeout(function() {
    check('the header says Threads and the Threads tab is the one marked',
      r.hdr.textContent === 'Threads' && r.btn.attrs['aria-selected'] === 'true' && r.tab.attrs['aria-selected'] === 'false');
    check('the dot goes while the threads show', r.dot.style.display === 'none');
    var kids = r.side.children;
    check('two groups, each a heading and its threads', kids.length === 4);
    check('headings say how many each holds', kids[0].textContent.indexOf('Over MCP (2)') >= 0 && kids[2].textContent.indexOf('Hand-offs (1)') >= 0);
    check('every group shows its threads, none folded away',
      kids[1].style.display === '' && kids[3].style.display === '' && kids[1].children.length === 2 && kids[3].children.length === 1);
    check('a heading is a label, not a fold toggle', !kids[0].onclick && !kids[2].onclick && kids[0].tag === 'div');
    check('a heading carries the dot when one of its threads is new',
      kids[0].children.length === 2 && kids[2].children.length === 1);
    var row = kids[1].children[0];
    check('a row says who the thread is with', row.textContent.indexOf('Check test review') >= 0 && row.textContent.indexOf('Claude Code') >= 0);
    row.listeners.click();
    check('a click opens the thread', opened[0] === 'mcp-desktop:k1:review');

    r.view('sessions');
    setTimeout(function() {
      check('back on sessions, the header and the tabs say so', r.hdr.textContent === 'Sessions' && r.tab.attrs['aria-selected'] === 'true');
      check('the tabs sit on a row of their own, not in the crowded header',
        /if \(railTabs\) side\.appendChild\(railTabs\);/.test(src) && !/leftExtras\.push\(threadsBtn\)/.test(src));
      r.open('mcp-desktop:k1');
      r.refresh();
      setTimeout(function() {
        check('a thread open on screen is locked when the threads are read', locked.indexOf('mcp-desktop:k1') >= 0);
        check('the composer lock covers threads', /var thread = !!\(sid && threadIds\[sid\]\);\s*recordLocked = altLocked \|\| thread/.test(src));
        check('the rail reads the threads on every load and stops there in the threads view',
          /function loadSessions\(\) \{\s*if \(!hasList\) return;\s*loadChannels\(\);\s*refreshThreads\(\);\s*if \(railView === 'threads'\) return;/.test(src));
        process.exit(fail ? 1 : 0);
      }, 0);
    }, 0);
  }, 0);
}, 0);
