// The suggested next message (cfg.suggest_url): after a turn the panel asks
// for what the person might say next and shows it as the empty box's
// placeholder; Tab puts it in the box. Typing, a send or another session
// drops it; a touch screen never asks. Checked against the shipped source.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}
function lift(start) {
  var i = src.indexOf(start), depth = 0, j = src.indexOf('{', i);
  if (i < 0) throw new Error('not found: ' + start);
  for (; j < src.length; j++) {
    if (src[j] === '{') depth++;
    else if (src[j] === '}' && --depth === 0) break;
  }
  return src.slice(i, j + 1);
}

var cfg = {suggest_url: 'api/s/{session}/suggest', placeholder: 'Ask something...'};
var activeSessionId = 's1', recordLocked = false, touch = false, grown = 0;
var inputArea = {value: '', placeholder: 'Ask something...', disabled: false, title: '',
  removeAttribute: function(n) { this[n] = ''; }, setSelectionRange: function() {}};
var window = {matchMedia: function() { return {matches: touch}; }};
var fetched = [], reply = {text: 'Yes, add the tests'};
function substituteExtras(u) { return u; }
function fetchJSON(u) {
  fetched.push(u);
  var r = reply;
  return {then: function(fn) { fn(r); return {catch: function() {}}; }};
}
function autosizeInput() { grown++; }
var nextSuggest = '', nextSuggestSid = '', suggestSeq = 0;
eval(lift('function clearNextSuggest()'));
eval(lift('function fetchNextSuggest()'));
eval(lift('function takeNextSuggest(ev)'));
function tab(extra) {
  var ev = {key: 'Tab', shiftKey: false};
  for (var k in extra || {}) ev[k] = extra[k];
  return takeNextSuggest(ev);
}

fetchNextSuggest();
check('it asks for the session that just ended a turn', fetched.length === 1 && fetched[0] === 'api/s/s1/suggest');
check('the suggestion is the empty box\'s placeholder', inputArea.placeholder === 'Yes, add the tests');
check('Shift+Tab is not taken', tab({shiftKey: true}) === false && inputArea.value === '');
check('Tab puts it in the box', tab() === true && inputArea.value === 'Yes, add the tests' && grown === 1);
check('and the placeholder goes back', inputArea.placeholder === 'Ask something...');
check('a second Tab is the browser\'s again', tab() === false);

inputArea.value = '';
fetchNextSuggest();
activeSessionId = 's2';
check('in another session Tab does not take it', tab() === false && inputArea.value === '');
activeSessionId = 's1';

inputArea.value = 'my own words';
fetchNextSuggest();
check('a box with text in it asks for nothing', fetched.length === 2);
inputArea.value = '';

touch = true;
fetchNextSuggest();
check('a touch screen asks for nothing', fetched.length === 2);
touch = false;

reply = {text: ''};
fetchNextSuggest();
check('no text shows nothing', inputArea.placeholder === 'Ask something...' && tab() === false);

var keys = lift("inputArea.addEventListener('keydown', function(ev)");
check('Tab is tried before the command list and history keys', keys.indexOf('takeNextSuggest(ev)') < keys.indexOf('cmdKey(ev)'));
check('a send drops it', lift('function sendMessage()').indexOf('clearNextSuggest();') > 0);
var done = src.slice(src.indexOf("case 'done':"), src.indexOf("case 'error':"));
check('a turn ending asks, unless unread notes go on as the next message',
  /if \(cfg\.resend_unread_notes && unread\.length\) sendUnreadNotes\(unread\);\s*else fetchNextSuggest\(\);/.test(done));

process.exit(fail ? 1 : 0);
