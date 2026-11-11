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
    get: function() { return this.children.map(function(c) { return typeof c === 'string' ? c : c.textContent; }).join(''); },
    set: function(v) { this.children = [String(v)]; }});
  for (var k in (opts || {})) {
    if (k === 'class') n.className = opts[k];
    else if (k === 'style') n.style.cssText = opts[k];
    else if (k.indexOf('on') === 0) n.on[k.slice(2)] = opts[k];
    else n.attrs[k] = opts[k];
  }
  (children || []).forEach(function(c) { if (c != null) n.children.push(c); });
  return n;
}
var window = {};
new Function('el', 'window', m[0])(el, window);
var fold = window.uiFoldText;

var short = fold('failed 3 time(s) in a row', {style: 'color:grey'});
check('short text is one plain span', short.tag === 'span' && short.children.length === 1 && short.children[0] === 'failed 3 time(s) in a row');
check('a span with no class is given none', short.className === undefined);
check('short text is not clamped', !/line-clamp/.test(short.style.cssText));

var long = new Array(40).join('word word ');
var card = fold(long, {style: 'color:grey', 'class': 'ui-card-warn-why'});
var body = card.children[0], more = card.children[1];
check('long text keeps its class', card.className === 'ui-card-warn-why');
check('the whole text is on the page', body.textContent === long);
check('it starts folded to three lines', /-webkit-line-clamp:3/.test(body.style.cssText));
check('with "more" to open it', more.tag === 'button' && more.textContent === 'more' && more.attrs['aria-expanded'] === 'false');

var stopped = false;
more.on.click({stopPropagation: function() { stopped = true; }});
check('a click does not reach the row', stopped);
check('"more" opens it in place', !/line-clamp/.test(body.style.cssText) && more.textContent === 'less' && more.attrs['aria-expanded'] === 'true');
more.on.click({stopPropagation: function() {}});
check('"less" folds it again', /-webkit-line-clamp:3/.test(body.style.cssText) && more.textContent === 'more');

var report = fold('line one\nline two\n' + long, {pre: true, lines: 4});
check('a report keeps its own line breaks', /white-space:pre-wrap/.test(report.children[0].style.cssText));
check('and folds to the lines asked for', /-webkit-line-clamp:4/.test(report.children[0].style.cssText));

check('the cards layout folds a row\'s text with it', /function cardText\(text, style, cls\) \{[\s\S]*?window\.uiFoldText\(text, \{style: style, 'class': cls\}\)/.test(panel));
check('long card text takes the card\'s width', /if \(text\.length > 240\) style \+= ';flex:1 1 100%';/.test(panel));

process.exit(fail ? 1 : 0);
