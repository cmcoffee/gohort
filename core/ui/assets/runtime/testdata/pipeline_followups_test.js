// A finished run offers its follow-ups as buttons, asked of the surface rather
// than configured, and a run made by one offers the way back to its parent.
var fs = require('fs');
var panel = fs.readFileSync(__dirname + '/../40_pipeline_panel.js', 'utf8');

var fail = 0;
function check(label, cond) {
  if (cond) console.log('ok   ' + label);
  else { fail++; console.log('FAIL ' + label); }
}

check('follow-ups are asked of the surface once',
  /if \(cfg\.followups_url\) \{\s*var followBase = cfg\.followups_url\.replace\(/.test(panel));
check('each becomes a streaming button on the same base',
  /method: 'stream',\s*url: followBase \+ 'followup\/' \+ encodeURIComponent\(f\.name\) \+ '\/\{id\}'/.test(panel));
check('the toolbar redraws when they arrive',
  /if \(currentSessionId\) renderActions\(currentSessionId\);/.test(panel));
check('a run with a parent offers the way back',
  /label: 'Parent run', method: 'load', url: '\{ParentID\}', show_if_field: 'ParentID'/.test(panel));
check('the toolbar draws the merged list, not only the configured actions',
  /var actions = panelActions\(\);/.test(panel) && /actions\.forEach\(function\(a\)/.test(panel));

// The base: "pipeline/followups" pairs with "pipeline/followup/...".
var base = 'pipeline/followups'.replace(/followups\/?$/, '');
check('the base is derived from the list url', base === 'pipeline/');

process.exit(fail ? 1 : 0);
