// A message with a label and no text is shown. A turn that chose to say
// nothing is saved with only a label ("Stayed quiet: ..."), and an assistant
// bubble with no text is the hidden lazy one, so the label was drawn inside a
// bubble nobody could see, live and on a reload.
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

// Just enough element for a bubble: classes, direct children found by class,
// and children that can be inserted and removed.
function node(cls) {
  var n = {
    cls: {}, kids: [], parent: null, textContent: '',
    classList: {
      add: function(c) { n.cls[c] = true; },
      remove: function() { for (var i = 0; i < arguments.length; i++) delete n.cls[arguments[i]]; },
      contains: function(c) { return !!n.cls[c]; },
    },
    insertBefore: function(k) { k.parent = n; n.kids.unshift(k); },
    remove: function() { if (n.parent) n.parent.kids.splice(n.parent.kids.indexOf(n), 1); },
    querySelector: function(sel) {
      var want = sel.split(',').map(function(s) { return s.replace(':scope >', '').trim().replace(/^\./, ''); });
      for (var i = 0; i < n.kids.length; i++) {
        for (var w = 0; w < want.length; w++) if (n.kids[i].cls[want[w]]) return n.kids[i];
      }
      return null;
    },
  };
  if (cls) cls.split(' ').forEach(function(c) { n.cls[c] = true; });
  return n;
}
function el(tag, attrs) { return node(attrs && attrs.class); }

var msgEls = {};
eval(lift(panel, 'function markEmptyBubble(m)', 'markEmptyBubble'));
eval(lift(panel, 'function labelMessage(id, label)', 'labelMessage'));

// The quiet turn: an assistant bubble with no text, hidden as lazy.
var body = node('ui-agent-msg-body');
msgEls.q = {bubble: node('ui-agent-msg ui-agent-msg-assistant ui-agent-msg-empty'), body: body};
labelMessage('q', 'Stayed quiet: nothing new to add');
check('a labelled bubble with no text is shown', !msgEls.q.bubble.classList.contains('ui-agent-msg-empty'));
check('it carries the label', (msgEls.q.bubble.querySelector(':scope > .ui-agent-msg-label') || {}).textContent === 'Stayed quiet: nothing new to add');

markEmptyBubble(msgEls.q);
check('emptying it again leaves a labelled bubble shown', !msgEls.q.bubble.classList.contains('ui-agent-msg-empty'));

labelMessage('q', '');
check('taking the label off a bubble with no text hides it again', msgEls.q.bubble.classList.contains('ui-agent-msg-empty'));

// A labelled reply with text is unaffected.
var tbody = node('ui-agent-msg-body');
tbody.textContent = 'The corrected answer.';
msgEls.c = {bubble: node('ui-agent-msg ui-agent-msg-assistant'), body: tbody};
labelMessage('c', 'Correction');
labelMessage('c', '');
check('a bubble with text stays shown when its label comes off', !msgEls.c.bubble.classList.contains('ui-agent-msg-empty'));

if (fail) process.exit(1);
