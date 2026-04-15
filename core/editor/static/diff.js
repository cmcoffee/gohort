// editorDiffLines computes a line-level diff between two strings using
// the standard LCS (longest common subsequence) algorithm. Returns an
// ordered list of operations: {type: "equal"|"add"|"remove", text: string}.
//
// Unchanged lines appear as "equal", lines in a but not b as "remove",
// lines in b but not a as "add". Ordering reflects the sequence a reader
// would see when transforming a into b top-to-bottom.
function editorDiffLines(a, b) {
  var aLines = (a == null ? '' : String(a)).split('\n');
  var bLines = (b == null ? '' : String(b)).split('\n');
  var m = aLines.length, n = bLines.length;

  // LCS table — dp[i][j] = length of LCS of a[0:i], b[0:j].
  var dp = new Array(m + 1);
  for (var i = 0; i <= m; i++) {
    dp[i] = new Array(n + 1);
    for (var j = 0; j <= n; j++) dp[i][j] = 0;
  }
  for (var i = 0; i < m; i++) {
    for (var j = 0; j < n; j++) {
      if (aLines[i] === bLines[j]) {
        dp[i + 1][j + 1] = dp[i][j] + 1;
      } else {
        dp[i + 1][j + 1] = Math.max(dp[i][j + 1], dp[i + 1][j]);
      }
    }
  }

  // Backtrack to build ops in reverse.
  var ops = [];
  var ai = m, bj = n;
  while (ai > 0 && bj > 0) {
    if (aLines[ai - 1] === bLines[bj - 1]) {
      ops.unshift({ type: 'equal', text: aLines[ai - 1] });
      ai--; bj--;
    } else if (dp[ai - 1][bj] >= dp[ai][bj - 1]) {
      ops.unshift({ type: 'remove', text: aLines[ai - 1] });
      ai--;
    } else {
      ops.unshift({ type: 'add', text: bLines[bj - 1] });
      bj--;
    }
  }
  while (ai > 0) { ops.unshift({ type: 'remove', text: aLines[ai - 1] }); ai--; }
  while (bj > 0) { ops.unshift({ type: 'add', text: bLines[bj - 1] }); bj--; }
  return ops;
}

// editorDiffEscape — small HTML escape for diff rendering.
function editorDiffEscape(s) {
  return String(s == null ? '' : s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

// editorDiffRender returns an HTML fragment showing the line-level diff
// between oldText and newText. Each changed line is prefixed with "+",
// "-", or " " and colored via the editor-diff-* CSS classes.
//
// Empty oldText renders as all additions. Identical texts render as all
// equal lines. Very long diffs are handled by the container's scroll.
function editorDiffRender(oldText, newText) {
  var ops = editorDiffLines(oldText, newText);
  var html = '<div class="editor-diff">';
  for (var i = 0; i < ops.length; i++) {
    var op = ops[i];
    var marker = op.type === 'add' ? '+' : op.type === 'remove' ? '-' : ' ';
    html += '<div class="editor-diff-' + op.type + '">';
    html += '<span class="editor-diff-marker">' + marker + '</span>';
    html += '<span class="editor-diff-text">' + editorDiffEscape(op.text) + '</span>';
    html += '</div>';
  }
  html += '</div>';
  return html;
}

// editorDiffStats returns {add: n, remove: m, equal: k} counts for a
// rendered diff. Useful for chat-message summary labels like "+8 -3".
function editorDiffStats(oldText, newText) {
  var ops = editorDiffLines(oldText, newText);
  var stats = { add: 0, remove: 0, equal: 0 };
  for (var i = 0; i < ops.length; i++) stats[ops[i].type]++;
  return stats;
}

// editorDiffState holds the single active review session. The widget
// is intentionally one-at-a-time: a second call to editorShowDiff
// while one is open replaces the pending proposal.
var editorDiffState = null;

// editorDiffIsOpen reports whether a diff review is currently active.
// Apps should check this before sending a new chat turn and warn the
// user if a pending proposal hasn't been resolved.
function editorDiffIsOpen() { return editorDiffState !== null; }

// editorShowDiff opens a review pane inside the editor pane, hiding
// the underlying textarea. The user can Apply or Dismiss.
//
// opts:
//   newText:        the proposed content (required)
//   editorPane:     container element (defaults to #editor-pane)
//   editorTextarea: textarea to hide (defaults to #editor)
//   onApply:        function(newText) — called on Apply. Apps usually
//                   set the textarea value and any associated fields
//                   (title/subject) here.
//   onDismiss:      function() — optional, called on Dismiss.
//   label:          optional header label (default "Review proposed changes")
function editorShowDiff(opts) {
  opts = opts || {};
  var editorPane = opts.editorPane || document.getElementById('editor-pane');
  var textarea = opts.editorTextarea || document.getElementById('editor');
  if (!editorPane || !textarea) return;

  // If a diff is already open, close it first (replace policy).
  if (editorDiffState) editorHideDiff();

  var oldText = textarea.value || '';
  var newText = opts.newText == null ? '' : String(opts.newText);
  var stats = editorDiffStats(oldText, newText);
  var label = opts.label || 'Review proposed changes';

  var pane = document.createElement('div');
  pane.id = 'editor-diff-pane';
  pane.innerHTML =
    '<div id="editor-diff-pane-header">' +
      '<span class="editor-diff-pane-title">' + editorDiffEscape(label) + '</span>' +
      '<span class="editor-diff-pane-stats">+' + stats.add + ' &minus;' + stats.remove + '</span>' +
      '<button class="editor-diff-pane-btn primary" id="editor-diff-apply-btn">Apply</button>' +
      '<button class="editor-diff-pane-btn secondary" id="editor-diff-dismiss-btn">Reject</button>' +
    '</div>' +
    '<div id="editor-diff-pane-body">' + editorDiffRender(oldText, newText) + '</div>';

  // Hide the textarea and any siblings below it that belong to edit mode
  // (tracked via data-diff-hidden so we can restore them).
  textarea.style.display = 'none';
  textarea.setAttribute('data-diff-hidden', 'true');

  // Insert the diff pane as the first child of editor-pane so it sits
  // where the textarea used to be.
  editorPane.insertBefore(pane, editorPane.firstChild);

  editorDiffState = {
    pane: pane,
    textarea: textarea,
    newText: newText,
    onApply: opts.onApply,
    onDismiss: opts.onDismiss
  };

  document.getElementById('editor-diff-apply-btn').onclick = function() {
    var state = editorDiffState;
    if (!state) return;
    if (typeof state.onApply === 'function') {
      state.onApply(state.newText);
    } else {
      state.textarea.value = state.newText;
    }
    editorHideDiff();
  };
  document.getElementById('editor-diff-dismiss-btn').onclick = function() {
    var state = editorDiffState;
    if (!state) return;
    if (typeof state.onDismiss === 'function') state.onDismiss();
    editorHideDiff();
  };
}

// editorHideDiff removes the review pane and restores the textarea.
function editorHideDiff() {
  if (!editorDiffState) return;
  var state = editorDiffState;
  if (state.pane && state.pane.parentNode) {
    state.pane.parentNode.removeChild(state.pane);
  }
  if (state.textarea) {
    state.textarea.style.display = '';
    state.textarea.removeAttribute('data-diff-hidden');
  }
  editorDiffState = null;
}
