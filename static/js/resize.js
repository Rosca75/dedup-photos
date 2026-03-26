// resize.js — Drag-to-resize for left panel sections and panel width.
// Supports vertical handles (between preview/folders/filters) and
// a horizontal handle (left panel width vs main area).

/** Wire up all resize handles on page load. */
export function initResize() {
  initVerticalHandles();
  initHorizontalHandle();
}

/** Set up vertical drag handles between panel sections. */
function initVerticalHandles() {
  const handles = document.querySelectorAll('.panel-resize-handle');
  handles.forEach(handle => {
    handle.addEventListener('mousedown', (e) => startVerticalDrag(e, handle));
  });
}

/** Start vertical drag: resize the section above vs below the handle. */
function startVerticalDrag(e, handle) {
  e.preventDefault();
  const above = handle.previousElementSibling;
  const below = handle.nextElementSibling;
  if (!above || !below) return;

  const startY = e.clientY;
  const aboveH = above.offsetHeight;
  const belowH = below.offsetHeight;

  handle.classList.add('dragging');
  document.body.style.cursor = 'row-resize';
  document.body.style.userSelect = 'none';

  function onMove(ev) {
    const dy = ev.clientY - startY;
    const newAbove = Math.max(60, aboveH + dy);
    const newBelow = Math.max(60, belowH - dy);
    above.style.height = newAbove + 'px';
    above.style.maxHeight = 'none';
    above.style.flex = 'none';
    below.style.height = newBelow + 'px';
    below.style.maxHeight = 'none';
    below.style.flex = 'none';
  }

  function onUp() {
    handle.classList.remove('dragging');
    document.body.style.cursor = '';
    document.body.style.userSelect = '';
    document.removeEventListener('mousemove', onMove);
    document.removeEventListener('mouseup', onUp);
  }

  document.addEventListener('mousemove', onMove);
  document.addEventListener('mouseup', onUp);
}

/** Set up horizontal drag handle for left panel width. */
function initHorizontalHandle() {
  const handle = document.getElementById('left-panel-resize');
  if (!handle) return;
  handle.addEventListener('mousedown', startHorizontalDrag);
}

/** Start horizontal drag: resize the left panel width. */
function startHorizontalDrag(e) {
  e.preventDefault();
  const layout = document.querySelector('.app-layout');
  const handle = e.currentTarget;
  const startX = e.clientX;

  // Read current left panel width from computed style.
  const cols = getComputedStyle(layout).gridTemplateColumns.split(/\s+/);
  const startWidth = parseFloat(cols[0]) || 240;

  handle.classList.add('dragging');
  document.body.style.cursor = 'col-resize';
  document.body.style.userSelect = 'none';

  function onMove(ev) {
    const dx = ev.clientX - startX;
    const newWidth = Math.max(160, Math.min(500, startWidth + dx));
    layout.style.gridTemplateColumns = newWidth + 'px 5px 1fr';
  }

  function onUp() {
    handle.classList.remove('dragging');
    document.body.style.cursor = '';
    document.body.style.userSelect = '';
    document.removeEventListener('mousemove', onMove);
    document.removeEventListener('mouseup', onUp);
  }

  document.addEventListener('mousemove', onMove);
  document.addEventListener('mouseup', onUp);
}
