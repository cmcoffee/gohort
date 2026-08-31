// The workbench's chat column used to be rebuilt from a fixed list of six
// fields, which discarded everything else the app declared — silently. An app
// author would set InjectURL (or a session rail) in Go, see it serialized into
// the page, and watch the panel behave as if it were never there, with no error
// to chase. This pins the pass-through.
//
// Extracts the mount block from 70_misc.js and drives it with a stub, so it
// tests the shipped code rather than a copy of it.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../70_misc.js', 'utf8');

var start = src.indexOf('var chatMount = {};');
var end = src.indexOf('mountComponent(chatMount, right);');
if (start < 0 || end < 0) {
  console.log('FAIL the workbench chat mount block is gone or renamed in 70_misc.js');
  process.exit(1);
}
var body = src.slice(start, end + 'mountComponent(chatMount, right);'.length);

function mount(chatCfg) {
  var captured = null;
  var fn = new Function('chatCfg', 'mountComponent', 'right',
    body + '\nreturn chatMount;');
  captured = fn(chatCfg, function(){}, null);
  return captured;
}

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

// The regression itself. A field the workbench has never heard of must survive.
var m = mount({
  type: 'agent_loop_panel',
  send_url: 'chat/send',
  cancel_url: 'chat/cancel',
  inject_url: 'chat/inject',
  list_url: 'chat/sessions',
  load_url: 'chat/sessions/{id}',
  delete_url: 'chat/sessions/{id}',
  list_title: 'Past sessions',
  markdown: true,
  lock_activity: true,
  empty_text: 'Ask me to draft a section.',
  placeholder: 'Ask the Guide Author…',
});
check('a mid-flight message has somewhere to go',   m.inject_url === 'chat/inject');
check('the session rail survives (list)',           m.list_url === 'chat/sessions');
check('the session rail survives (load)',           m.load_url === 'chat/sessions/{id}');
check('the session rail survives (delete)',         m.delete_url === 'chat/sessions/{id}');
check('the rail keeps its heading',                 m.list_title === 'Past sessions');
check("the app's own copy is not overwritten",      m.placeholder === 'Ask the Guide Author…');
check('and its empty state is not either',          m.empty_text === 'Ask me to draft a section.');

// The rail is opt-in on all three together (see hasList). An app that declares
// none must still get one clean window, not a half-built rail.
var bare = mount({send_url: 'chat/send', cancel_url: 'chat/cancel'});
check('no session URLs declared means no rail',
  !bare.list_url && !bare.load_url && !bare.delete_url);
check('an undeclared inject_url stays undeclared',  bare.inject_url === undefined);

// What the workbench legitimately imposes.
check('the SSE-compatible panel type is forced',    bare.type === 'agent_loop_panel');
check('the activity pane stays off — no room for it in a third column',
  bare.lock_activity === true);
check('a workbench that declares nothing still sends somewhere',
  bare.send_url === 'chat/send' && bare.cancel_url === 'chat/cancel');
check('markdown defaults on',                       bare.markdown === true);

// An app that names a ChatPanel still gets the parser that can read chat/send's
// frames — the reason type is forced rather than defaulted.
var wrong = mount({type: 'chat_panel', send_url: 'x/send'});
check('a chat_panel is corrected to the panel whose parser matches the wire format',
  wrong.type === 'agent_loop_panel');
check('the app-declared send URL still wins over the default',
  wrong.send_url === 'x/send');

process.exit(fail ? 1 : 0);
