// The ShowWhen grammar, driven for real: the evaluator is lifted out of the
// runtime and run against records. Two gaps it closes: a field used under two
// unrelated conditions could not be shown for both (no OR), and a checklist
// could not be tested for what it holds (its String() is the joined list).
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../10_basics.js', 'utf8');

function lift(name) {
  var start = src.indexOf('function ' + name + '(');
  if (start < 0) throw new Error('no ' + name + ' in the runtime');
  var depth = 0, i = src.indexOf('{', start);
  for (; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}' && --depth === 0) break;
  }
  return src.slice(start, i + 1);
}
var matchesWhen = new Function(lift('hasValue') + '\n' + lift('matchesWhen') + '\nreturn matchesWhen;')();

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

// Existing grammar keeps its meaning.
check('bare field is truthiness', matchesWhen('on', {on: true}) && !matchesWhen('on', {}));
check('! is falsiness', matchesWhen('!on', {}) && !matchesWhen('!on', {on: 'x'}));
check('membership', matchesWhen('p:a|b', {p: 'b'}) && !matchesWhen('p:a|b', {p: 'c'}));
check('negated membership holds while untouched', matchesWhen('p:!a|b', {}));
check('; clauses all must hold', matchesWhen('type:oauth2;grant:!jwt', {type: 'oauth2', grant: 'code'}) &&
  !matchesWhen('type:oauth2;grant:!jwt', {type: 'oauth2', grant: 'jwt'}));

// OR between groups.
var userField = 'type:basic_auth||type:oauth2;grant:password';
check('|| holds when the first group does', matchesWhen(userField, {type: 'basic_auth', grant: 'client_credentials'}));
check('|| holds when the second group does', matchesWhen(userField, {type: 'oauth2', grant: 'password'}));
check('|| fails when neither does', !matchesWhen(userField, {type: 'oauth2', grant: 'client_credentials'}));
check('an empty group is not a match-all', !matchesWhen('p:a||', {p: 'z'}));

// A checklist matches on what it contains.
check('list contains one of the values', matchesWhen('tags:red|blue', {tags: ['green', 'red']}));
check('list without any of them', !matchesWhen('tags:red|blue', {tags: ['green']}));
check('negated contains', matchesWhen('tags:!red', {tags: ['green']}) && !matchesWhen('tags:!red', {tags: ['red']}));
check('an empty list contains nothing', !matchesWhen('tags:red', {tags: []}));

// The row code honours show_when as well as hide_when.
check('row cells consult show_when', /c\.show_when && !matchesWhen\(c\.show_when, row\)/.test(src));

process.exit(fail ? 1 : 0);
