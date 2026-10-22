// A live tool call hangs on the newest visible line the model wrote, not on
// the person's question. The turn's bubble is made early and stays empty
// through its tool rounds; its fallback host was found by walking back from
// that bubble and cached for the whole turn, so it was the question, and a
// mid-turn line ("Let me check...") further down never got the calls it went
// on to make. Each call remembers its own host so its result and the calls run
// beneath it land beside it. Checked against the shipped source.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var fn = src.slice(src.indexOf('function liveToolHost(m) {'), src.indexOf('function callHostFor(m, callID) {'));
check('the search starts from the end of the conversation', fn.indexOf('var node = convoLog.lastElementChild;') > 0);
check('it skips the spinner, the empty bubble itself and other empties',
  fn.indexOf("node !== thinkingEl && node !== m.bubble && !node.classList.contains('ui-agent-msg-empty')") > 0);
check('a bubble with text hosts its own calls', fn.indexOf("if (!m.bubble.classList.contains('ui-agent-msg-empty')) return m.bubble;") > 0);

var call = src.slice(src.indexOf("case 'tool_call': {"), src.indexOf("case 'tool_progress':"));
check('a live call is placed by liveToolHost', call.indexOf('var host = liveToolHost(bm);') > 0);
check('and its host is remembered by call id', call.indexOf('bm.callHosts[ev.call_id] = host;') > 0);
check('a call beneath a running one finds its parent where the parent was drawn',
  call.indexOf('callHostFor(pm, ev.parent_call_id)') > 0);

var result = src.slice(src.indexOf("case 'tool_result': {"), src.indexOf("case 'tool_result': {") + 400);
check('a result lands where its call was drawn', result.indexOf('var host2 = callHostFor(bm2, ev.call_id);') > 0);

process.exit(fail ? 1 : 0);
