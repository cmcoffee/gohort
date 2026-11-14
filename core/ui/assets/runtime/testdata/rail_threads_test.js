// Threads (cfg.threads_url): the rail's second view, reached from the first
// item of the header's ⋯ menu, lists the agent's own conversations apart from
// its sessions; while it is up, the header shows an arrow back. Nothing marks
// a thread as new: threads are checked in on, not waiting on the person.
// The list is flat, newest first as sent, each row tagged with its kind; a
// thread opens like a session, its composer locked.
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
    get: function() { return this.children.map(function(c) { return typeof c === 'string' ? c : c.nodeValue != null ? c.nodeValue : c.textContent; }).join(''); },
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
  {id: 'mcp-desktop:k1:review', title: 'Check test review', tag: 'MCP', with: 'Claude Code', unread: true, created: '2026-11-13T09:41:00Z'},
  {id: 'dispatch:s1:a1', title: '', tag: 'Agent', with: 'Wren', created: '2026-11-12T15:05:00Z'},
  {id: 'mcp-desktop:k1', title: 'Default conversation', tag: 'MCP', created: '0001-01-01T00:00:00Z'},
  {id: 'machine:t1:sort', title: 'Step sort'},
];
var harness = new Function('el', 'cfg', 'list', 'opened', 'locked',
  'var railView = "sessions", threadIds = {}, activeSessionId = "", sideList = el("div"), sideSearch = null;\n' +
  'var threadsBtn = el("button", {}, [{nodeValue: "Threads"}]), threadsBack = el("button", {}, ["\u2190"]); threadsBack.style.display = "none";\n' +
  'var sideHdrLabel = el("span", {}, ["Sessions"]);\n' +
  'var document = {createTextNode: function(t) { return {nodeValue: t}; }};\n' +
  'function fetchJSON() { return Promise.resolve(list); }\n' +
  'function substituteExtras(u) { return u; }\n' +
  'function openSession(id) { opened.push(id); activeSessionId = id; }\n' +
  'function closeDrawer() {}\n' +
  'function currentAgentLabel() { return "Scout"; }\n' +
  'function applyRecordLock(id) { locked.push(id); }\n' +
  'function loadSessions() { refreshThreads(); }\n' +
  'var window = {uiConfirm: function() { return Promise.resolve(false); }};\n' +
  src.slice(from, to) +
  '\nreturn {refresh: refreshThreads, view: setRailView, side: sideList, btn: threadsBtn, hdr: sideHdrLabel,' +
  ' back: threadsBack, ids: function() { return threadIds; }, open: function(id) { activeSessionId = id; }, transcript: transcriptFor};');

var opened = [], locked = [];
var r = harness(el, {threads_url: 'api/threads'}, threads, opened, locked);
check('the way to the threads is the first item of the ⋯ menu, not a tab row',
  /var moreItemCount = 0;\s*\/\/ Threads:[\s\S]{0,1200}?moreMenu\.appendChild\(threadsBtn\);\s*moreItemCount\+\+;\s*\}\s*if \(cfg\.mark_all_read_url\)/.test(src) &&
  !/ui-rail-tabs?\b/.test(src));
check('the menu item flips between the two lists',
  /setRailView\(railView === 'threads' \? 'sessions' : 'threads'\);/.test(src));
var prelude = fs.readFileSync(__dirname + '/../00_prelude.js', 'utf8');
var css = fs.readFileSync(__dirname + '/../../runtime.css', 'utf8');
check('the title keeps stretching with the arrow before it, so the buttons stay right',
  /el\('span', \{class: 'ui-chat-side-h-label', text: opts\.label\}\)/.test(prelude) &&
  /\.ui-chat-side-h > \.ui-chat-side-h-label \{\s*flex: 1;/.test(css));
check('the back arrow sits before the header title, marked as it is built',
  /sideHdrBuilt\.elt\.insertBefore\(threadsBack, sideHdrLabel\);\s*markRailView\(\);/.test(src));
r.refresh();
setTimeout(function() {
  check('the threads are known by id once read', !!r.ids()['mcp-desktop:k1'] && !!r.ids()['dispatch:s1:a1']);
  check('the way to the threads carries no new-activity dot', !/threadsDot/.test(src) && r.btn.textContent === 'Threads');
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
    check('the header says Threads, with the arrow back showing',
      r.hdr.textContent === 'Threads' && r.back.style.display === '');
    check('the menu item now leads back, its label edited in place',
      r.btn.textContent === 'Back to Sessions' && r.btn.children.length === 1 && typeof r.btn.children[0].nodeValue === 'string');
    var kids = r.side.children;
    check('one flat list, a row per thread in the order sent, no headings',
      kids.length === 4 && kids.every(function(k) { return k.className.indexOf('ui-chat-side-item') === 0; }));
    // A row: its text column, holding the tag-and-who line, then the
    // title-and-when line.
    var col = function(k) { return k.children[0]; };
    var line = function(k, cls) { return col(k).children.filter(function(c) { return c.className === cls; })[0] || null; };
    var tagOf = function(k) { var m = line(k, 'ui-threads-meta'); var c = m && m.children[0].children[0]; return c ? c.textContent : ''; };
    var whoOf = function(k) { var m = line(k, 'ui-threads-meta'); return m ? m.children[1].textContent : ''; };
    var whenOf = function(k) { var w = line(k, 'ui-threads-when'); return w ? w.textContent : null; };
    var handoff = kids[1], meta = line(handoff, 'ui-threads-meta');
    check('on top: the tag at the left, who centered across the row',
      col(handoff).className === 'ui-chat-side-text' && tagOf(handoff) === 'Agent' && whoOf(handoff) === 'Wren' &&
      /grid-template-columns:1fr minmax\(0, max-content\) 1fr/.test(meta.style.cssText) &&
      /text-align:center/.test(meta.children[1].style.cssText));
    check('who it is with stands out: the accent colour, a little bolder',
      /font-weight:600;color:var\(--accent/.test(meta.children[1].style.cssText));
    var stamp = new Date('2026-11-12T15:05:00Z').toLocaleString([], {month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit'});
    check('below, centered: a hand-off is when it began, no stand-in title',
      whenOf(handoff) === stamp && /text-align:center/.test(line(handoff, 'ui-threads-when').style.cssText));
    check('a thread with a title of its own keeps it, before when it began',
      whenOf(kids[0]).indexOf('Check test review \u00b7 ') === 0 && whoOf(kids[0]) === 'Claude Code');
    check('a zero time shows no date', whenOf(kids[2]) === 'Default conversation');
    check('a thread with neither tag nor who has no top line', line(kids[3], 'ui-threads-meta') === null && whenOf(kids[3]) === 'Step sort');
    var row = kids[0];
    check('a thread the app sends as unread still draws no dot',
      !row.children.some(function(c) { return c && c.className === 'ui-unread-dot'; }));
    check('a row says who the thread is with', row.textContent.indexOf('Check test review') >= 0 && row.textContent.indexOf('Claude Code') >= 0);
    row.listeners.click();
    check('a click opens the thread', opened[0] === 'mcp-desktop:k1:review');

    r.view('sessions');
    setTimeout(function() {
      check('back on sessions, the header says so and the arrow goes',
        r.hdr.textContent === 'Sessions' && r.back.style.display === 'none' && r.btn.textContent === 'Threads');
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
