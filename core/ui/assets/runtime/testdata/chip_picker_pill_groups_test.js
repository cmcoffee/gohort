// A picker whose options carry a group puts its selected pills under the same
// headings its attach list uses. A row of bare names (node-7, Ops, Intake)
// said nothing about which was a system, a guide or a store. Driven against a
// small DOM, so it is the drawing that is checked.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');
var fail = 0;
function check(name, cond, extra) {
  if (cond) { console.log('ok   ' + name); return; }
  fail++;
  console.log('FAIL ' + name + (extra === undefined ? '' : '  ' + extra));
}

var body = src.slice(src.indexOf('function renderPills() {'), src.indexOf('function addRow(opt, host) {'));
check('a grouped picker groups its pills', body.indexOf('if (!cfg.group_by_field) {') > 0 && body.indexOf("pills.classList.add('ui-cp-pills-grouped');") > 0);
check('headings come from the option group field', body.indexOf('opt[cfg.group_by_field]') > 0);
check('a pill no longer available goes last', body.indexOf("return a === '\\u0000' ? 1 : -1;") > 0);
check('one pill builder serves both shapes', (body.match(/pillFor\(v\)/g) || []).length >= 3);

// Behaviour: a minimal element model, enough for renderPills' own calls.
function El(tag, opts, kids) {
  this.tag = tag; this.cls = (opts && opts.class) || ''; this.text = (opts && opts.text) || '';
  this.children = []; var self = this;
  (kids || []).forEach(function(k){ self.children.push(typeof k === 'string' ? {text: k, children: []} : k); });
  this.classList = {add: function(c){ self.cls += ' ' + c; }, remove: function(){}};
}
El.prototype.appendChild = function(c){ this.children.push(c); };
El.prototype.addEventListener = function(){};
Object.defineProperty(El.prototype, 'innerHTML', {set: function(){ this.children = []; }});
function el(tag, opts, kids) { return new El(tag, opts, kids); }
function texts(n) { var out = n.text ? [n.text] : []; (n.children || []).forEach(function(c){ out = out.concat(texts(c)); }); return out; }

var cfg = {group_by_field: 'group'};
var byVal = {sys1: {id: 'sys1', name: 'node-7', group: 'Machines'},
             g1: {id: 'g1', name: 'Ops', group: 'Guides'},
             sys2: {id: 'sys2', name: 'web01', group: 'Machines'}};
var selected = ['sys1', 'g1', 'sys2', 'gone1'];
var flagged = {};
var pills = el('div', {class: 'ui-cp-pills'});
function labelOf(o){ return o.name; }
function toggle(){} function persist(){ return Promise.resolve(); } function showToast(){}
eval(body.replace(/^function renderPills/, 'var renderPills = function') .replace(/\n      \/\/ pillFor is[\s\S]*$/, ''));
eval(src.slice(src.indexOf('function pillFor(v) {'), src.indexOf('function addRow(opt, host) {')));
renderPills();
var rows = pills.children;
check('one row per kind, plus one for what is gone', rows.length === 3, rows.length);
var first = texts(rows[0]).join('|'), second = texts(rows[1]).join('|');
check('kinds in order, each with its pills', first.indexOf('Guides') === 0 && first.indexOf('Ops') > 0 && second.indexOf('Machines') === 0 && second.indexOf('node-7') > 0 && second.indexOf('web01') > 0, first + ' / ' + second);
check('the missing one is last, without a heading', texts(rows[2]).join('|').indexOf('gone1') === 0, texts(rows[2]).join('|'));

process.exit(fail ? 1 : 0);
