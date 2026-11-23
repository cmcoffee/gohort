// Something waiting on the person shows in the tab's title, counted, and
// clears when it ends; a notification goes out only with the tab hidden and
// notifications allowed; the browser is asked only through uiWaitingAsk. The
// chat's approval card starts and ends its wait, and a turn's end clears any
// left. Run against the shipped source.
var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../71_notice_bell.js', 'utf8');
var panel = fs.readFileSync(__dirname + '/../30_agent_loop_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

var sent = [], asked = 0;
var window = {focus: function() {}};
var document = {title: 'Investigator', hidden: false};
function Notification(title, opts) { sent.push({title: title, body: opts.body}); this.close = function() {}; }
Notification.permission = 'default';
Notification.requestPermission = function() { asked++; };

eval(src.slice(src.indexOf('var uiWaiting = {};')));

check('nothing waiting leaves the title alone', document.title === 'Investigator');
window.uiWaitingStart('confirm-1', 'Allow fetch_url?');
check('one waiting is counted in the title', document.title === '(1) Waiting for you - Investigator');
window.uiWaitingStart('confirm-1', 'Allow fetch_url?');
check('the same key twice is one', document.title === '(1) Waiting for you - Investigator');
window.uiWaitingStart('confirm-2', 'Run the deploy?');
check('two are two', document.title === '(2) Waiting for you - Investigator');
check('no notification while the tab is in view', sent.length === 0);
window.uiWaitingEnd('confirm-1');
check('one answered leaves one', document.title === '(1) Waiting for you - Investigator');
document.title = '(1) Waiting for you - Investigator (renamed)';
window.uiWaitingEnd('confirm-2');
check('all answered gives back the page\'s own title, as it now is', document.title === 'Investigator (renamed)');

document.hidden = true;
window.uiWaitingStart('confirm-3', 'Allow it?');
check('hidden but not allowed: no notification', sent.length === 0);
window.uiWaitingEnd('confirm-3');
check('while undecided, the question may be asked', window.uiWaitingCanAsk() === true);
check('nothing asks the browser on its own', asked === 0);
window.uiWaitingAsk();
check('asked only when uiWaitingAsk is called', asked === 1);
Notification.permission = 'granted';
check('decided, it is not offered again', window.uiWaitingCanAsk() === false);
window.uiWaitingStart('confirm-4', 'Allow it?');
check('hidden and allowed: a notification with what waits', sent.length === 1 && sent[0].body === 'Allow it?');
document.hidden = false;
window.uiWaitingEnd('confirm-4');
window.uiWaitingStart('confirm-5', 'Again?');
check('back in view: no notification', sent.length === 1);

var add = panel.slice(panel.indexOf('function addConfirm(d) {'), panel.indexOf('function endWaitingCards('));
check('an approval card starts its wait', add.indexOf("window.uiWaitingStart('confirm-' + id") > 0);
check('the card offers the permission click only while it is open', add.indexOf('window.uiWaitingCanAsk()') > 0 && add.indexOf('window.uiWaitingAsk()') > 0);
var settle = panel.slice(panel.indexOf('function settleConfirmCard('), panel.indexOf('function editConfirmInput('));
check('an answered card ends its wait, a replayed one too', settle.indexOf("window.uiWaitingEnd('confirm-' + id)") > 0 &&
  settle.indexOf("window.uiWaitingEnd('confirm-' + id)") < settle.indexOf("classList.contains('ui-agent-confirm-resolved')"));
check('a turn that ends clears what is left', /case 'done':\s*endWaitingCards\(\);/.test(panel) && /case 'error':\s*endWaitingCards\(\);/.test(panel));

if (fail) { console.log(fail + ' failed'); process.exit(1); }
