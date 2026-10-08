// The waiting dots sit at the bottom of the conversation: below a new reply
// bubble, and below whatever landed while they were hidden, with the
// person's queued messages still last of all.
//
// Drives the real functions, lifted from the runtime source, over a
// stand-in log.
var fs = require('fs');
var dir = __dirname + '/..';

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

function lift(src, start, label) {
  var i = src.indexOf(start);
  if (i < 0) { console.log('FAIL ' + label + ' is gone'); process.exit(1); }
  var depth = 0, j = src.indexOf('{', i);
  for (; j < src.length; j++) {
    if (src[j] === '{') depth++;
    else if (src[j] === '}' && --depth === 0) break;
  }
  return src.slice(i, j + 1);
}

var panel = fs.readFileSync(dir + '/30_agent_loop_panel.js', 'utf8');

function node(cls) {
  return {cls: cls, parentNode: null, style: {display: ''}};
}
var convoLog = {
  kids: [],
  appendChild: function(n) {
    var i = this.kids.indexOf(n);
    if (i >= 0) this.kids.splice(i, 1);
    this.kids.push(n);
    n.parentNode = this;
  },
  querySelectorAll: function() {
    return this.kids.filter(function(n) { return n.cls === 'pending'; });
  },
};
function order() { return convoLog.kids.map(function(n) { return n.cls; }).join(' '); }

var thinkingEl = node('dots');
var thinkingQuietTimer = null, convoStickToBottom = false;
function scrollConvo() {}
eval(lift(panel, 'function keepPendingInterjectionsLast()', 'keepPendingInterjectionsLast'));
eval(lift(panel, 'function waitingNow()', 'waitingNow'));

// The append step of addMessage, as the source writes it.
var appendStep = (function() {
  var i = panel.indexOf('      convoLog.appendChild(bubble);\n      // The waiting dots stay LAST');
  if (i < 0) { console.log('FAIL the bubble append is gone'); process.exit(1); }
  var j = panel.indexOf('keepPendingInterjectionsLast();', i);
  return panel.slice(i, j + 'keepPendingInterjectionsLast();'.length);
})();
function addBubble(cls, role) {
  var bubble = node(cls);
  eval(appendStep);
}

convoLog.appendChild(node('user'));
convoLog.appendChild(thinkingEl);
addBubble('reply1', 'assistant');
check('a new reply bubble lands above the dots', order() === 'user reply1 dots');

thinkingEl.style.display = 'none';
convoLog.appendChild(node('toolcard'));
convoLog.appendChild(node('pending'));
waitingNow();
check('dots coming back go below what landed while hidden, a queued message stays last',
  order() === 'user reply1 toolcard dots pending' && thinkingEl.style.display === '');

addBubble('reply2', 'assistant');
check('the next reply also lands above the dots, queued message still last',
  order() === 'user reply1 toolcard reply2 dots pending');

if (fail) process.exit(1);
