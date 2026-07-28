// Headless harness for buildDocChat. Stubs fetchJSON and the render
// callbacks, then drives the send loop the way both panels do.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../45_document_core.js', 'utf8');

var fetchLog = [], responses = {}, nextURL = '/chat';
var harness = new Function('el','fetchJSON','showToast','relTime','renderBulkBar','window',
  src + '\nreturn buildDocChat;');

function mkChat(over) {
  var msgs = [], busy = [], proposals = [], replies = [], removed = 0;
  var opts = {
    url: nextURL,
    appendMsg: function(role, text) {
      var node = {role: role, text: text, remove: function(){ removed++; }};
      msgs.push(node);
      return node;
    },
    thinking: function() {
      var node = {role: 'thinking', remove: function(){ removed++; }};
      msgs.push(node);
      return node;
    },
    buildBody: function(message, mode, history) {
      return {message: message, mode: mode, history: history};
    },
    setBusy: function(on){ busy.push(on); },
    proposalOf: function(data, mode) {
      if (mode === 'chat') return null;
      return data.doc || null;
    },
    onProposal: function(text, data){ proposals.push({text: text, data: data}); },
  };
  for (var k in (over||{})) opts[k] = over[k];
  var chat = harness(
    null,
    function(url, o) {
      var body = JSON.parse(o.body);
      fetchLog.push(body);
      var r = responses[url];
      if (r instanceof Error) return Promise.reject(r);
      if (typeof r === 'function') return r();
      return Promise.resolve(r === undefined ? {content: 'ok'} : r);
    },
    function(){}, function(){}, function(){}, {}
  )(opts);
  return {chat: chat, msgs: msgs, busy: busy, proposals: proposals,
          replies: replies, removedCount: function(){ return removed; }};
}
var fail = 0;
function check(l, c, e){ if (c) console.log('ok   '+l); else { fail++; console.log('FAIL '+l+(e?'  '+e:'')); } }
function tick(){ return new Promise(function(r){ setImmediate(r); }); }

(async function(){
  // --- plain reply ---
  responses['/chat'] = {content: 'Hello there'};
  var h = mkChat();
  h.chat.send('hi', 'edit');
  await tick(); await tick();
  check('user bubble rendered', h.msgs[0].role === 'user' && h.msgs[0].text === 'hi');
  check('thinking placeholder shown', h.msgs[1].role === 'thinking');
  check('thinking removed on reply', h.removedCount() === 1);
  check('assistant reply rendered', h.msgs[2].role === 'assistant' && h.msgs[2].text === 'Hello there');
  check('busy toggled on then off', JSON.stringify(h.busy) === '[true,false]', JSON.stringify(h.busy));
  check('history has both turns', h.chat.history().length === 2);
  check('history records the reply prose',
    h.chat.history()[1].content === 'Hello there');

  // The message must NOT also appear in the history it ships with.
  check('history sent excludes the current message', fetchLog[0].history.length === 0,
    JSON.stringify(fetchLog[0].history));
  check('mode forwarded', fetchLog[0].mode === 'edit');

  // Second turn carries the first.
  h.chat.send('again', 'edit');
  await tick(); await tick();
  check('second turn ships prior history', fetchLog[1].history.length === 2,
    JSON.stringify(fetchLog[1].history.length));

  // --- blank input is a no-op ---
  var before = fetchLog.length;
  h.chat.send('   ', 'edit');
  await tick();
  check('whitespace-only message ignored', fetchLog.length === before);

  // --- re-entrancy guard (codewriter had none before this merge) ---
  var release;
  responses['/chat'] = function(){ return new Promise(function(r){ release = function(){ r({content:'done'}); }; }); };
  var g = mkChat();
  g.chat.send('one', 'edit');
  await tick();
  var inflight = fetchLog.length;
  g.chat.send('two', 'edit');
  await tick();
  check('second send blocked while in flight', fetchLog.length === inflight,
    'sent ' + (fetchLog.length - inflight) + ' extra');
  release();
  await tick(); await tick();
  g.chat.send('three', 'edit');
  await tick();
  check('sending resumes after the reply', fetchLog.length === inflight + 1);

  // --- proposal routing ---
  responses['/chat'] = {content: 'Rewrote it.', doc: 'NEW BODY'};
  var p = mkChat();
  p.chat.send('rewrite', 'edit');
  await tick(); await tick();
  check('proposal routed to onProposal', p.proposals.length === 1 && p.proposals[0].text === 'NEW BODY');
  check('proposal suppresses the assistant bubble',
    p.msgs.filter(function(m){ return m.role === 'assistant'; }).length === 0);
  check('proposal still records prose in history',
    p.chat.history()[1].content === 'Rewrote it.');

  // chat mode must never route to the diff, even with a doc present.
  var c = mkChat();
  c.chat.send('discuss', 'chat');
  await tick(); await tick();
  check('chat mode never proposes', c.proposals.length === 0);
  check('chat mode renders the bubble',
    c.msgs.filter(function(m){ return m.role === 'assistant'; }).length === 1);

  // --- error surfaces (codewriter ignored data.error before) ---
  responses['/chat'] = {error: 'model unavailable'};
  var e1 = mkChat();
  e1.chat.send('x', 'edit');
  await tick(); await tick();
  check('200-with-error renders as error',
    e1.msgs[2].role === 'error' && e1.msgs[2].text === 'model unavailable');
  check('error clears busy', e1.busy[e1.busy.length-1] === false);
  check('errored turn adds no assistant history', e1.chat.history().length === 1);

  responses['/chat'] = null;
  var e2 = mkChat();
  e2.chat.send('x', 'edit');
  await tick(); await tick();
  check('empty response reported', e2.msgs[2].role === 'error' && /empty/.test(e2.msgs[2].text));

  responses['/chat'] = new Error('network down');
  var e3 = mkChat();
  e3.chat.send('x', 'edit');
  await tick(); await tick();
  check('rejected fetch renders as error', e3.msgs[2].role === 'error' && e3.msgs[2].text === 'network down');
  check('thinking removed on error', e3.removedCount() === 1);
  check('busy cleared after error', e3.busy[e3.busy.length-1] === false);
  check('sending resumes after an error', (function(){
    responses['/chat'] = {content: 'ok'};
    var n = fetchLog.length;
    e3.chat.send('retry', 'edit');
    return fetchLog.length === n + 1;
  })());

  // --- history cap (article_editor was unbounded before this merge) ---
  await tick(); await tick();
  responses['/chat'] = {content: 'r'};
  var cap = mkChat({historyLimit: 4});
  for (var i = 0; i < 6; i++) { cap.chat.send('m' + i, 'edit'); await tick(); await tick(); }
  check('history capped at the limit', cap.chat.history().length === 4,
    String(cap.chat.history().length));
  check('cap keeps the NEWEST turns',
    cap.chat.history()[cap.chat.history().length-2].content === 'm5',
    JSON.stringify(cap.chat.history().map(function(t){return t.content;})));

  // --- clear() ---
  cap.chat.clear();
  check('clear empties history', cap.chat.history().length === 0);

  console.log(fail === 0 ? '\nALL DOC-CHAT TESTS PASS' : '\n' + fail + ' FAILURES');
  process.exit(fail ? 1 : 0);
})();
