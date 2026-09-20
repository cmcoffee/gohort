// What the bell actually DRAWS, not what the source says it draws.
//
// The glyph used to be the 🔔 emoji, which is not one shape: every platform
// draws its own, at its own size, off its own baseline, which is why it sat
// wrong on a phone. A colour emoji also cannot be muted, and muted-until-unread
// is the whole design of this control.
//
// Checking the emitted node rather than grepping the source, because the way
// this regresses is somebody putting a character back for convenience.

var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../71_notice_bell.js', 'utf8');

var fail = 0;
function check(name, cond, extra) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name + (extra === undefined ? '' : '  ' + extra));
}

function mkNode(tag) {
  return {
    tagName: tag, _attrs: {}, _kids: [],
    setAttribute: function (k, v) { this._attrs[k] = v; },
    getAttribute: function (k) { return this._attrs[k]; },
    appendChild: function (k) { this._kids.push(k); return k; },
  };
}
var documentStub = {
  createElement: mkNode,
  createElementNS: function (ns, tag) { var n = mkNode(tag); n._ns = ns; return n; },
};

var from = src.indexOf('function uiBellGlyph()');
var to = src.indexOf('function uiNoticeBell()');
if (from < 0 || to < from) { console.log('FAIL uiBellGlyph moved in 71_notice_bell.js'); process.exit(1); }
var lift = new Function('document', src.slice(from, to) + '\nreturn uiBellGlyph;');
var svg = lift(documentStub)();

check('it is an svg', svg.tagName === 'svg', svg.tagName);
check('in the svg namespace, or the browser builds an inert HTML element',
  svg._ns === 'http://www.w3.org/2000/svg', svg._ns);
// The same 64-unit grid the rest of the runtime's glyphs use, so one set of
// rasterization findings applies to all of them.
check('authored on the 64 grid', svg.getAttribute('viewBox') === '0 0 64 64', svg.getAttribute('viewBox'));
// currentColor is what lets opacity and the unread state do the work. A fixed
// fill would make the muted state a different colour rather than a quieter one.
check('filled with currentColor', svg.getAttribute('fill') === 'currentColor', svg.getAttribute('fill'));
// The button already carries the label; a second one here reads it twice.
check('hidden from the accessibility tree', svg.getAttribute('aria-hidden') === 'true');
check('it draws something', svg._kids.length >= 1, String(svg._kids.length));
svg._kids.forEach(function (k, i) {
  check('child ' + i + ' is a filled path', k.tagName === 'path' && !!k.getAttribute('d'));
  // No strokes: a hairline is the first thing a downsample to 16px eats.
  check('child ' + i + ' carries no stroke', k.getAttribute('stroke') === undefined);
});

// And nothing in the file went back to a typed glyph.
var emoji = /[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/u;
check('no emoji anywhere in the bell', !emoji.test(src.replace(/\u{1F514}/gu, function (m) {
  // The one allowed mention is the comment saying what this replaced.
  return '';
})), 'a platform-drawn glyph is back');

process.exit(fail ? 1 : 0);
