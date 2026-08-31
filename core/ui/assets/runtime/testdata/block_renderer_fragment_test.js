// Can a DECLARATIVE app (app_def html section, no ExtraHeadHTML) register a
// custom pipeline block renderer?
//
// Three real pieces have to line up, and this harness drives the actual source
// of all three rather than a paraphrase:
//
//   1. 00_prelude.js  — hoists window.UIBlockRenderers + uiRegisterBlockRenderer
//   2. 70_misc.js     — components.card revives inline <script> (innerHTML does
//                       not execute scripts, so the card clones each one into a
//                       fresh node the browser will run)
//   3. 40_pipeline_panel.js — SNAPSHOTS the registry at mount time
//
// (3) is the whole story. A live lookup would make ordering irrelevant; a
// snapshot means an html section mounted AFTER the pipeline section registers
// into a map nobody reads again.
//
// The one browser rule this models rather than proves: a freshly created
// script element runs when it becomes CONNECTED to the document. That rule is
// also why the revival loop in components.card exists at all, per its comment.

var fs = require('fs');
var dir = __dirname + '/..';

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

// ---- 1. the real registry, lifted out of the prelude ------------------------

var prelude = fs.readFileSync(dir + '/00_prelude.js', 'utf8');
var rStart = prelude.indexOf('if (!window.UIBlockRenderers)');
var rEnd = prelude.indexOf('};', prelude.indexOf('window.uiRegisterBlockRenderer = function'));
if (rStart < 0 || rEnd < 0) {
  console.log('FAIL the block-renderer registry is gone from 00_prelude.js');
  process.exit(1);
}
var registrySrc = prelude.slice(rStart, rEnd + 2);

// ---- 2. the real card component --------------------------------------------

var misc = fs.readFileSync(dir + '/70_misc.js', 'utf8');
var cStart = misc.indexOf('components.card = function(cfg) {');
var cEnd = misc.indexOf('components.frame = function(cfg) {');
if (cStart < 0 || cEnd < 0) {
  console.log('FAIL components.card is gone from 70_misc.js');
  process.exit(1);
}
var cardSrc = misc.slice(cStart, misc.lastIndexOf('};', cEnd) + 2);
check('components.card still revives inline scripts',
  cardSrc.indexOf("createElement('script')") >= 0);

// ---- a DOM stub, modelling only what card touches ---------------------------
//
// The two rules that matter: innerHTML parses scripts INERT (HTML5), and a
// script node runs when it becomes connected.

function makeDOM(windowStub) {
  function node(tag) {
    return {
      tagName: tag, attributes: [], childNodes: [], parentNode: null,
      _connected: false, _code: null, _inert: false, _ran: false,
      set innerHTML(html) {
        // Inert by spec: scripts parsed from innerHTML never execute.
        this.childNodes = [];
        var re = /<script[^>]*>([\s\S]*?)<\/script>/g, m;
        while ((m = re.exec(html)) !== null) {
          var s = node('script');
          s._code = m[1];
          s._inert = true;          // parsed, will never run on its own
          s.parentNode = this;
          this.childNodes.push(s);
        }
      },
      get textContent() { return this._code || ''; },
      set textContent(v) { this._code = v; },
      set text(v) { this._code = v; },
      setAttribute: function(k, v) { this.attributes.push({name: k, value: v}); },
      querySelectorAll: function(sel) {
        var out = [];
        (function walk(n) {
          n.childNodes.forEach(function(c) {
            if (c.tagName === sel) out.push(c);
            walk(c);
          });
        })(this);
        return out;
      },
      appendChild: function(c) {
        c.parentNode = this;
        this.childNodes.push(c);
        if (this._connected) connect(c);
        return c;
      },
      replaceChild: function(fresh, old) {
        var i = this.childNodes.indexOf(old);
        if (i >= 0) this.childNodes[i] = fresh;
        fresh.parentNode = this;
        if (this._connected) connect(fresh);
        return old;
      },
    };
  }
  // Becoming connected is what runs a script that was never parsed inert.
  function connect(n) {
    n._connected = true;
    if (n.tagName === 'script' && !n._inert && !n._ran && n._code) {
      n._ran = true;
      new Function('window', n._code)(windowStub);
    }
    n.childNodes.forEach(connect);
  }
  var document = {
    createElement: node,
    createTextNode: function(t) { var n2 = node('#text'); n2._code = t; return n2; },
  };
  var root = node('div');          // stands in for #ui-root
  root._connected = true;          // getElementById returns a connected element
  return {document: document, root: root, node: node};
}

// The runtime's el() helper, reduced to what card asks of it.
function makeEl(document) {
  return function(tag, attrs, kids) {
    var n = document.createElement(tag);
    if (attrs && attrs.text) n.textContent = attrs.text;
    (kids || []).forEach(function(k) { n.appendChild(k); });
    return n;
  };
}

function scenario() {
  var windowStub = {};
  new Function('window', registrySrc)(windowStub);
  var dom = makeDOM(windowStub);
  var card = new Function('el', 'document', 'window', 'uiAutoRefresh', 'fetch',
    'var components = {};\n' + cardSrc + '\nreturn components.card;')(
      makeEl(dom.document), dom.document, windowStub,
      function() {}, function() { throw new Error('a plain card must not fetch'); });
  return {win: windowStub, dom: dom, card: card};
}

var REGISTRATION =
  "<div>x</div><script>window.uiRegisterBlockRenderer('verdict', " +
  "function(d){ return {wrap: null, body: null}; });</script>";

// ---- the actual questions ---------------------------------------------------

// Baseline: the registry starts empty and the script is inert until revived.
var s = scenario();
check('the registry starts empty', Object.keys(s.win.UIBlockRenderers).length === 0);

var wrap = s.card({html: REGISTRATION});
check('a card built but not yet in the page has not registered anything',
  s.win.UIBlockRenderers.verdict === undefined);

// The moment the section lands in the page, the revived script runs.
s.dom.root.appendChild(wrap);
check('an html FRAGMENT registers a block renderer once mounted',
  typeof s.win.UIBlockRenderers.verdict === 'function');

// And the revival is what did it: an innerHTML-only paint leaves it inert.
var s2 = scenario();
var inertWrap = s2.dom.node('div');
inertWrap.innerHTML = REGISTRATION;
s2.dom.root.appendChild(inertWrap);
check('innerHTML alone never registers (so the revival loop is load-bearing)',
  s2.win.UIBlockRenderers.verdict === undefined);

// ---- 3. ordering, which is the real constraint ------------------------------

var panel = fs.readFileSync(dir + '/40_pipeline_panel.js', 'utf8');
check('pipeline_panel still SNAPSHOTS the registry at mount',
  panel.indexOf('Object.assign({}, window.UIBlockRenderers || {})') >= 0);

// A snapshot taken before the html section mounts misses the renderer; the
// same snapshot taken after finds it. This is exactly the difference between
// listing the html section before or after the pipeline section.
var s3 = scenario();
var early = Object.assign({}, s3.win.UIBlockRenderers || {});
s3.dom.root.appendChild(s3.card({html: REGISTRATION}));
var late = Object.assign({}, s3.win.UIBlockRenderers || {});

check('a pipeline section listed FIRST snapshots an empty registry',
  early.verdict === undefined);
check('a pipeline section listed AFTER the html section sees the renderer',
  typeof late.verdict === 'function');

// The contrast worth knowing: the agent-loop panel looks up live, so ordering
// does not bind there. If this ever stops being true the asymmetry is gone.
var loop = fs.readFileSync(dir + '/30_agent_loop_panel.js', 'utf8');
check('agent_loop_panel still looks the renderer up LIVE (no ordering rule)',
  loop.indexOf('window.UIBlockRenderers && window.UIBlockRenderers[d.type]') >= 0);

process.exit(fail ? 1 : 0);
