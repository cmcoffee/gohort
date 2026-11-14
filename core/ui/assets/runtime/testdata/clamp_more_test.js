// uiClampMore: a text that may run long shows its first two lines, and More
// opens the rest in place. The cut is made on screen, so the button appears
// only once the text is laid out and found to overflow. The shipped function
// is run here over a stub DOM with a hand-cranked animation frame, along with
// the places that hand it their text: the agent panel's empty message.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../00_prelude.js', 'utf8');
var panel = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var start = src.indexOf('  function uiClampMore(text, lines) {');
var end = src.indexOf('\n  }\n', src.indexOf("btn.addEventListener('click'", start));
check('uiClampMore is in the prelude', start > 0 && end > start);
if (!(start > 0 && end > start)) process.exit(1);

function style() {
  var st = {display: '', props: {}};
  st.setProperty = function(k, v) { this.props[k] = v; };
  return st;
}
function el(tag, opts, children) {
  var n = {tag: tag, attrs: {}, children: [], style: style(), listeners: {}, isConnected: false,
    scrollHeight: 0, clientHeight: 0,
    setAttribute: function(k, v) { this.attrs[k] = String(v); },
    getAttribute: function(k) { return this.attrs[k] == null ? null : this.attrs[k]; },
    appendChild: function(c) { this.children.push(c); return c; },
    addEventListener: function(ev, fn) { this.listeners[ev] = fn; }};
  Object.defineProperty(n, 'firstChild', {get: function() { return this.children[0]; }});
  Object.defineProperty(n, 'textContent', {get: function() {
    return this.children.map(function(c) { return c.nodeValue != null ? c.nodeValue : c.textContent; }).join('');
  }});
  for (var k in (opts || {})) {
    if (k === 'class') n.className = opts[k];
    else if (k === 'style') {
      n.style.cssText = opts[k];
      if (/display:none/.test(opts[k])) n.style.display = 'none';
    } else n.attrs[k] = opts[k];
  }
  (children || []).forEach(function(c) { n.children.push(typeof c === 'string' ? {nodeValue: c} : c); });
  return n;
}
var frames = [];
function requestAnimationFrame(fn) { frames.push(fn); }
function tick() { var f = frames; frames = []; f.forEach(function(fn) { fn(); }); }
var clampMore = new Function('el', 'requestAnimationFrame', src.slice(start, end + 4) + '\nreturn uiClampMore;')(el, requestAnimationFrame);

function clamped(body) {
  return body.style.display === '-webkit-box' && body.style.overflow === 'hidden' &&
    body.style.props['-webkit-line-clamp'] === '2' && body.style.props['-webkit-box-orient'] === 'vertical';
}

// A long text, laid out taller than its two lines.
var long = clampMore('  Investigates people and companies from public sources.\nSpecialists for filings, accounts and addresses.  ');
var body = long.children[0], btn = long.children[1];
check('the whole text is there, trimmed, its line breaks kept',
  body.textContent === 'Investigates people and companies from public sources.\nSpecialists for filings, accounts and addresses.' &&
  /white-space:pre-line/.test(body.style.cssText));
check('it starts clamped to two lines', clamped(body));
check('More is hidden before the text is laid out', btn.style.display === 'none');
tick();
check('still hidden while it is off the page, and it keeps waiting', btn.style.display === 'none' && frames.length === 1);
long.isConnected = true; body.scrollHeight = 120; body.clientHeight = 40;
tick();
check('on the page and overflowing, More shows', btn.style.display === '' && btn.textContent === 'More' && btn.attrs['aria-expanded'] === 'false');
var label = btn.firstChild;
btn.listeners.click({stopPropagation: function() {}});
check('More opens the whole text and turns to Less', !clamped(body) && body.style.display === '' && btn.textContent === 'Less' && btn.attrs['aria-expanded'] === 'true');
btn.listeners.click({stopPropagation: function() {}});
check('Less clamps it again, editing the label in place', clamped(body) && btn.textContent === 'More' && btn.firstChild === label);

// A short text: fits in its two lines, so no button ever.
var short = clampMore('Finds an address.');
short.isConnected = true; short.children[0].scrollHeight = 20; short.children[0].clientHeight = 20;
tick();
check('a text that fits shows whole with no More', short.children[1].style.display === 'none');
check('a different line count is honoured', clampMore('x', 3).children[0].style.props['-webkit-line-clamp'] === '3');
check('empty or missing text gives an empty element', clampMore(null).textContent.indexOf('More') === 0 && clampMore(null).children[0].textContent === '');

check('the panel\'s empty message goes through it, everywhere it is drawn',
  (panel.match(/\[uiClampMore\(cfg\.empty_text \|\| 'Start typing below\.'\)\]/g) || []).length === 4 &&
  !/\[cfg\.empty_text \|\| 'Start typing below\.'\]/.test(panel));

process.exit(fail ? 1 : 0);
