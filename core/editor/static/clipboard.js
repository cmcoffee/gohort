// editorClipboardCopy copies text to the system clipboard, with a
// document.execCommand fallback for browsers that don't expose the
// async Clipboard API (older browsers, non-HTTPS contexts). Returns
// a Promise that resolves on success and rejects on failure so callers
// can show their own feedback.
//
// Usage: editorClipboardCopy(text).then(onSuccess, onFailure);
function editorClipboardCopy(text) {
  return new Promise(function(resolve, reject) {
    var value = text == null ? '' : String(text);
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(value).then(resolve, function() {
        editorClipboardFallback(value, resolve, reject);
      });
    } else {
      editorClipboardFallback(value, resolve, reject);
    }
  });
}

// editorClipboardFallback uses a hidden textarea + execCommand('copy')
// for environments where navigator.clipboard is unavailable or denied.
// This is the same pattern CodeWriter used inline before extraction.
function editorClipboardFallback(text, resolve, reject) {
  var ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.select();
  try {
    document.execCommand('copy');
    document.body.removeChild(ta);
    resolve();
  } catch (e) {
    document.body.removeChild(ta);
    reject(e);
  }
}

// editorClipboardButton wires a button element so clicking it copies
// the given text (or the result of a getter function) and flashes
// "Copied!" briefly on the button label. Restores the original label
// after 1.5 seconds. Use this for simple Copy buttons; for custom
// feedback, call editorClipboardCopy directly.
//
// The source argument may be a string or a function returning a string.
function editorClipboardButton(btn, source) {
  var original = btn.textContent;
  var text = (typeof source === 'function') ? source() : source;
  if (!text) { alert('Nothing to copy.'); return; }
  btn.disabled = true;
  editorClipboardCopy(text).then(function() {
    btn.textContent = 'Copied!';
    setTimeout(function() {
      btn.textContent = original;
      btn.disabled = false;
    }, 1500);
  }, function() {
    btn.textContent = 'Copy failed';
    setTimeout(function() {
      btn.textContent = original;
      btn.disabled = false;
    }, 1500);
  });
}
