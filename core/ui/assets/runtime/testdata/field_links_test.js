// A field's Links are buttons beside its Edit button, and on a heading under
// its help line: a value set somewhere else links to where it is set. Only a
// path or an http(s) address becomes a link.
var fs = require('fs');
var basics = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

function el(tag, attrs, kids) {
  var n = {tag: tag, attrs: attrs || {}, kids: kids || [], style: {},
    setAttribute: function(k, v) { n.attrs[k] = v; }};
  return n;
}
var current = {docs_url: '/docs/thing', bad: 'javascript:alert(1)'};

var src = basics.match(/    function fieldLinkButtons\(f\) \{[\s\S]*?\n    \}\n/);
check('fieldLinkButtons is in the form code', !!src);
eval(src[0]);

var got = fieldLinkButtons({links: [
  {label: 'Set the key', url: '/settings#keys'},
  {label: 'Docs', field: 'docs_url', target: '_blank'},
  {label: 'Elsewhere', url: 'https://example.com/x'},
  {label: 'Unsafe', field: 'bad'},
  {label: 'Nothing'},
]});
check('a path, a field holding a path and an http address become links', got.length === 3);
check('each is a button-styled link with its label', got[0].tag === 'a' && got[0].attrs.class === 'ui-form-suggest-btn' && got[0].kids[0] === 'Set the key');
check('a same-page section link keeps its address', got[0].attrs.href === '/settings#keys');
check('a field link reads the loaded value and keeps its target', got[1].attrs.href === '/docs/thing' && got[1].attrs.target === '_blank');
check('no links, no buttons', fieldLinkButtons({}).length === 0);

// Wired in both places: a heading's links under its help, and the Edit row.
var header = basics.match(/if \(t === 'header'\) \{([\s\S]*?)\n        return fieldWrap;\n      \}/);
check('a heading draws its links', !!header && /fieldLinkButtons\(f\)/.test(header[1]));
check('the Edit row uses the same links', /var fieldLinks = fieldLinkButtons\(f\);/.test(basics));

if (fail) { console.log(fail + ' FAIL'); process.exit(1); }
