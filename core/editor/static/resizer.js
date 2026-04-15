// editorStartResize starts a drag-resize interaction. Call from a
// mousedown handler on the resizer element. Generic over axis:
//
//   axis: "row"  -> vertical-drag, resizes target height
//   axis: "col"  -> horizontal-drag, resizes target width
//
// opts:
//   target:    the element whose size changes (required)
//   container: element whose bounding rect defines the clamp window
//              (defaults to target.parentElement)
//   min:       minimum pixel size (defaults to 60)
//   pad:       distance from the far edge of the container the target
//              must not exceed (defaults to 80)
//   resizer:   optional element that gets a "dragging" class during
//              the drag (for visual feedback)
//   onEnd:     optional callback invoked when the drag ends
//
// Size is set as an inline style (height/width) on the target, so
// subsequent page loads reset to CSS defaults unless the app persists
// the value itself.
function editorStartResize(event, axis, opts) {
  event.preventDefault();
  opts = opts || {};
  var target = opts.target;
  if (!target) return;
  var container = opts.container || target.parentElement;
  var min = opts.min != null ? opts.min : 60;
  var pad = opts.pad != null ? opts.pad : 80;
  var resizer = opts.resizer || null;
  if (resizer) resizer.classList.add('dragging');

  function onMove(ev) {
    var rect = container.getBoundingClientRect();
    var size;
    if (axis === 'row') {
      size = rect.bottom - ev.clientY;
    } else {
      size = rect.right - ev.clientX;
    }
    var max = (axis === 'row' ? rect.height : rect.width) - pad;
    if (size < min) size = min;
    if (size > max) size = max;
    if (axis === 'row') {
      target.style.height = size + 'px';
    } else {
      target.style.width = size + 'px';
    }
  }

  function onUp() {
    if (resizer) resizer.classList.remove('dragging');
    document.removeEventListener('mousemove', onMove);
    document.removeEventListener('mouseup', onUp);
    if (typeof opts.onEnd === 'function') opts.onEnd();
  }

  document.addEventListener('mousemove', onMove);
  document.addEventListener('mouseup', onUp);
}
