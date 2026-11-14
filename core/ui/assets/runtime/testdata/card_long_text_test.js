// Text is never cut where it cannot be read (uiFoldText): short text renders
// as one plain span, and text past the fold shows its first lines with
// "more", which opens the whole text in place, and "less", which closes it
// again. The whole text is on the page either way. The cards layout uses it
// for a row's text and its warning.
var fs = require('fs');
var prelude = fs.readFileSync(__dirname + '/../00_prelude.js', 'utf8');
var panel = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var m = prelude.match(/  window\.uiFoldText = function\(text, opts\) \{[\s\S]*?\n  \};\n/);
check('uiFoldText is in the prelude', !!m);
if (!m) process.exit(1);

// Just enough of an element for the fold: attributes, a style string,
// children, a click handler and text.
function el(tag, opts, children) {
  var n = {tag: tag, attrs: {}, children: [], style: {cssText: ''}, on: {},
    setAttribute: function(k, v) { this.attrs[k] = String(v); }};
  Object.defineProperty(n, 'textContent', {
    get: function() { return this.children.map(function(c) { return c.nodeValue != null ? c.nodeValue : c.textContent; }).join(''); },
    set: function(v) { this.children = [{nodeValue: String(v)}]; }});
  Object.defineProperty(n, 'firstChild', {get: function() { return this.children[0]; }});
  for (var k in (opts || {})) {
    if (k === 'class') n.className = opts[k];
    else if (k === 'style') n.style.cssText = opts[k];
    else if (k.indexOf('on') === 0) n.on[k.slice(2)] = opts[k];
    else n.attrs[k] = opts[k];
  }
  (children || []).forEach(function(c) { if (c != null) n.children.push(typeof c === 'string' ? {nodeValue: c} : c); });
  return n;
}
var window = {};
new Function('el', 'window', m[0])(el, window);
var fold = window.uiFoldText;

var short = fold('failed 3 time(s) in a row', {style: 'color:grey'});
check('short text is one plain span', short.tag === 'span' && short.children.length === 1 && short.children[0].nodeValue === 'failed 3 time(s) in a row');
check('a span with no class is given none', short.className === undefined);
check('short text is not clamped', !/max-height/.test(short.style.cssText));

var long = new Array(40).join('word word ');
var card = fold(long, {style: 'color:grey', 'class': 'ui-card-warn-why'});
var body = card.children[0], more = card.children[1];
check('long text keeps its class', card.className === 'ui-card-warn-why');
check('the whole text is on the page', body.textContent === long);
check('it starts folded to three lines, fading where it is cut', /max-height:calc\(3 \* 1lh\)/.test(body.style.cssText) && /animation:ui-cut-fade-y/.test(body.style.cssText));
check('with "more" to open it', more.tag === 'button' && more.textContent === 'more' && more.attrs['aria-expanded'] === 'false');

var stopped = false;
more.on.click({stopPropagation: function() { stopped = true; }});
check('a click does not reach the row', stopped);
check('"more" opens it in place', !/max-height/.test(body.style.cssText) && more.textContent === 'less' && more.attrs['aria-expanded'] === 'true');
more.on.click({stopPropagation: function() {}});
check('"less" folds it again', /max-height:calc\(3 \* 1lh\)/.test(body.style.cssText) && more.textContent === 'more');

var report = fold('line one\nline two\n' + long, {pre: true, lines: 4});
check('a report keeps its own line breaks', /white-space:pre-wrap/.test(report.children[0].style.cssText));
check('and folds to the lines asked for', /max-height:calc\(4 \* 1lh\)/.test(report.children[0].style.cssText));

// "more" only when the fold cuts something: long text that fits its lines
// at the width it has hides it, and a change of size (a narrower window)
// brings it back when the text no longer fits.
var watched = [];
window.ResizeObserver = function(fn) { this.observe = function(n) { watched.push({node: n, fn: fn}); }; };
var wide = fold(long, {});
var wbody = wide.children[0], wmore = wide.children[1];
check('a folded text is watched for its size', watched.length === 1 && watched[0].node === wbody);
wbody.isConnected = true; wbody.clientHeight = 60; wbody.scrollHeight = 60;
watched[0].fn();
check('text that fits its lines has no "more"', wmore.style.display === 'none');
wbody.clientHeight = 60; wbody.scrollHeight = 140;
watched[0].fn();
check('a narrower width that cuts it brings "more" back', wmore.style.display === '');
var unlaid = fold(long, {});
watched[1].fn();
check('before it is laid out, "more" shows', unlaid.children[1].style.display !== 'none');
wmore.on.click({stopPropagation: function() {}});
wbody.scrollHeight = 60;
watched[0].fn();
check('an opened text keeps its "less"', wmore.style.display === '' && wmore.textContent === 'less');
delete window.ResizeObserver;

// The one fold helper: an agent's description folds by its lines alone
// (fold 0), two of them, whatever its length, and its "more" is quiet.
var label = more.firstChild;
more.on.click({stopPropagation: function() {}});
more.on.click({stopPropagation: function() {}});
check('the toggle edits its own label in place', more.firstChild === label && more.textContent === 'more');
check('"more" is small and muted, not the accent', /color:var\(--text-mute/.test(more.style.cssText) && !/--accent/.test(more.style.cssText));
var desc = fold('Investigates people and companies.', {lines: 2, fold: 0, pre: true});
check('fold 0 folds even short text by its lines', desc.tag === 'div' && /max-height:calc\(2 \* 1lh\)/.test(desc.children[0].style.cssText));
check('there is no second fold helper', !/uiClampMore\s*=|function uiClampMore/.test(prelude));
check('an empty pane\'s text folds with it, everywhere it is drawn',
  (panel.match(/\[window\.uiFoldText\(cfg\.empty_text \|\| 'Start typing below\.', \{lines: 2, fold: 0, pre: true\}\)\]/g) || []).length === 4);
check('the cards layout folds a row\'s text with it', /function cardText\(text, style, cls\) \{[\s\S]*?window\.uiFoldText\(text, \{style: style, 'class': cls\}\)/.test(panel));
check('long card text takes the card\'s width', /if \(text\.length > 240\) style \+= ';flex:1 1 100%';/.test(panel));

process.exit(fail ? 1 : 0);
