// Anything a click waits on shows that it is alive: a spinner frame and,
// past three seconds, the seconds elapsed, then the button's own label back.
// The Suggest buttons and a toolbar's post actions all use it, and a Suggest
// can be stopped with a second click.
var fs = require('fs');
var prelude = fs.readFileSync(__dirname + '/../00_prelude.js', 'utf8');
var panel = fs.readFileSync(__dirname + '/../40_pipeline_panel.js', 'utf8');
var chat = fs.readFileSync(__dirname + '/../20_chat_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var src = prelude.match(/  function busyButton\(btn, stoppable\) \{[\s\S]*?\n  \}\n/);
check('busyButton is in the prelude', !!src);
eval(src[0]);

// A stand-in button: enough DOM for the helper.
function fakeButton(label) {
  var b = {
    title: 'Suggest', disabled: false, childNodes: [{label: label}], cls: {},
    classList: {add: function(c) { b.cls[c] = true; }, remove: function(c) { delete b.cls[c]; }},
    appendChild: function(n) { b.childNodes.push(n); },
  };
  Object.defineProperty(b, 'textContent', {
    get: function() { return b.childNodes.map(function(n) { return n.label || n.text || ''; }).join(''); },
    set: function(v) { b.childNodes = v ? [{text: v}] : []; },
  });
  return b;
}

var realNow = Date.now, t0 = realNow();
var b = fakeButton('Suggest');
var busy = busyButton(b, true);
check('it spins in place of the label', /^[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]$/.test(b.textContent));
check('a stoppable button stays live and says how to stop it', !b.disabled && /stop/.test(b.title));
Date.now = function() { return t0 + 5000; };
var spun = false;
setTimeout(function() {
  spun = /^[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏] 5s$/.test(b.textContent);
  busy.stop();
  Date.now = realNow;
  check('past three seconds it counts the seconds', spun);
  check('stopped, the label and title come back', b.textContent === 'Suggest' && b.title === 'Suggest' && !b.cls['ui-busy']);
  var d = fakeButton('Run');
  var busy2 = busyButton(d, false);
  check('a plain one is disabled while it works', d.disabled);
  busy2.stop();
  check('and enabled again after', !d.disabled && d.textContent === 'Run');

  check('the panel Suggest spins and stops on a second click',
    /if \(prefillRun\) \{ prefillRun\.ctl\.abort\(\); return; \}/.test(panel) && /busy: busyButton\(prefillBtn, true\)/.test(panel));
  check('the chat Suggest does the same',
    /if \(prefillRun\) \{ prefillRun\.ctl\.abort\(\); return; \}/.test(chat) && /busy: busyButton\(prefillBtn, true\)/.test(chat));
  check('no static ellipsis is left on a Suggest button', !/prefillBtn\.textContent = '…'/.test(panel + chat));
  check('what is typed goes along as ?input=', /'input=' \+ encodeURIComponent\(typed\)/.test(panel));
  check('a page with no suggest of its own asks the surface for one',
    /fetchJSON\(suggestURL \+ '\?probe=1'\)\.then\(function\(p\) \{\s*if \(p && p\.label && !prefillBtn\) addPrefill\(suggestURL, p\.label, true\);/.test(panel));
  check('a toolbar post spins and shows the action\'s own message',
    /var busy = busyButton\(btn, false\);/.test(panel) && /r\.message \? r\.message : a\.label \+ ' done'/.test(panel));
  process.exit(fail ? 1 : 0);
}, 150);
