// Saved prompts as slash commands (AgentLoopPanel.CommandsURL), and the up
// arrow bringing back the last message: "/name more words" is sent as the
// prompt and the words, a "/word" naming nothing is sent as typed, and the
// recalled message is the last one sent, not the first.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');
var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}
function lift(start, end, label) {
  var i = src.indexOf(start), j = src.indexOf(end, i);
  if (i < 0 || j < 0) { console.log('FAIL ' + label + ' is gone'); process.exit(1); }
  return src.slice(i, j);
}
eval(lift('    function expandCommand(text, list) {', '    // recallKey:', 'expandCommand'));
var list = [{name: 'test', prompt: 'Run the tests and fix what fails.'}, {name: 'Review', prompt: 'Review the changes.'}];
check('a command is sent as its prompt', expandCommand('/test', list) === 'Run the tests and fix what fails.');
check('words after it follow the prompt', expandCommand('/test  only the parser\nplease', list) === 'Run the tests and fix what fails.\n\nonly the parser\nplease');
check('names match whatever their case', expandCommand('/review', list) === 'Review the changes.');
check('a word naming no command is sent as typed', expandCommand('/deploy now', list) === '/deploy now');
check('a path is sent as typed', expandCommand('/etc/hosts is wrong', list) === '/etc/hosts is wrong');
check('a message not starting with / is sent as typed', expandCommand('please /test', list) === 'please /test');

eval(lift('    function recallKey(ev) {', '    function renderAttachments()', 'recallKey'));
function bubble(order) { return {isConnected: true, order: order, compareDocumentPosition: function(o) { return o.order > order ? 4 : 2; }}; }
var userEdit = null, inputArea = {value: '', setSelectionRange: function() {}};
function autosizeInput() {}
// Ids that look like numbers are listed first by Object.keys, out of order.
var msgEls = {'u-9': {role: 'user', rawText: 'last', bubble: bubble(3)}, '2': {role: 'user', rawText: 'first', bubble: bubble(1)},
  'a1': {role: 'assistant', rawText: 'reply', bubble: bubble(4)}};
var up = {key: 'ArrowUp'};
check('up in an empty box brings back the last message sent', recallKey(up) === true && inputArea.value === 'last');
inputArea.value = 'typing';
check('up in a box with text in it moves the cursor as usual', recallKey(up) === false && inputArea.value === 'typing');
inputArea.value = '';
check('shift up is left alone', recallKey({key: 'ArrowUp', shiftKey: true}) === false);
// "@" at the start of a word, up to the caret, is a mention being typed.
eval(lift('    function mentionQuery() {', '    function hideSuggest() {', 'mentionQuery'));
function at(v, pos) { inputArea = {value: v, selectionStart: pos == null ? v.length : pos}; return mentionQuery(); }
var mq = at('look at @mai');
check('a word starting with @ is a mention, its start found', mq && mq.q === 'mai' && mq.start === 8 && mq.end === 12);
check('a bare @ asks for everything', at('@').q === '');
check('an address is not a mention', at('mail me@example') === null);
check('a finished mention is not asked about again', at('see @main.go ') === null);
check('the mention the caret is in, not the last one', at('@a.go and @b', 5).q === 'a.go');
process.exit(fail ? 1 : 0);
