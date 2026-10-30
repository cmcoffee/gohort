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

  // Backtrack to build ops in reverse. On a tie the ADD is taken first:
  // backtracking runs from the end, so what is taken first lands last, and a
  // changed line reads as the old one then the new one, as diffs do. Taking
  // the remove first listed every replacement new-above-old.
  var ops = [];
  var ai = m, bj = n;
  while (ai > 0 && bj > 0) {
    if (aLines[ai - 1] === bLines[bj - 1]) {
      ops.unshift({ type: 'equal', text: aLines[ai - 1] });
      ai--; bj--;
    } else if (dp[ai - 1][bj] > dp[ai][bj - 1]) {
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
