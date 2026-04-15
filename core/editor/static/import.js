// editorImportTextFile wires a hidden file-input's change event to
// read a selected text file and hand its contents to a callback. Size
// over softLimitBytes triggers a confirmation prompt before reading.
//
// event:       the input's change event
// opts:
//   softLimit:  size in bytes past which the user is asked to confirm
//               (default 5 MB)
//   onLoad:     function(text, file) called with the file contents
//   onError:    function(err) called on read failure (defaults to alert)
//
// After reading (or canceling), the input's value is cleared so the
// same file can be selected again if needed.
function editorImportTextFile(event, opts) {
  opts = opts || {};
  var softLimit = opts.softLimit != null ? opts.softLimit : 5 * 1024 * 1024;
  var input = event.target;
  var file = input.files && input.files[0];
  input.value = '';
  if (!file) return;
  if (file.size > softLimit) {
    var kb = Math.round(file.size / 1024);
    if (!confirm('File is ' + kb + ' KB. Import anyway?')) return;
  }
  var reader = new FileReader();
  reader.onload = function(e) {
    var text = e.target.result || '';
    if (typeof opts.onLoad === 'function') opts.onLoad(text, file);
  };
  reader.onerror = function(err) {
    if (typeof opts.onError === 'function') {
      opts.onError(err);
    } else {
      alert('Failed to read file.');
    }
  };
  reader.readAsText(file);
}

// editorApplyImportedText handles the common case of pasting imported
// text into a textarea. If the textarea is non-empty, prompts the user
// to replace or append; on cancel, leaves the textarea untouched.
// Returns true if the text was applied.
function editorApplyImportedText(textarea, text, filename) {
  if (textarea.value) {
    if (!confirm('Replace current contents with ' + (filename || 'imported text') + '?')) {
      if (confirm('Append instead?')) {
        textarea.value = textarea.value + (textarea.value.endsWith('\n') ? '' : '\n') + text;
        return true;
      }
      return false;
    }
  }
  textarea.value = text;
  return true;
}
