// table.js — Central zone: render scan results as a flat data table.
// Supports sortable columns, collapsible groups, series badges, hover preview,
// keyboard navigation, and promote-to-original.

import { state } from './state.js';
import { formatBytes, formatDuration } from './helpers.js';
import { resetScanUI } from './scan.js';
import { buildSidebarTree } from './sidebar.js';
import { showPreview, clearPreview } from './preview.js';
import { updateBatchButtons, updateConfirmButton } from './actions.js';
import { applyFilters } from './filters.js';
import { apiGetThumbnail } from './api.js';

/**
 * Render full scan results into the main area as a data table.
 * Respects state.selectedFolder and dynamic filters.
 */
export function renderResults(data) {
  resetScanUI();
  state.scanResult = data;
  updateStatsBar(data.stats || {});

  // Apply folder filter (supports multiple folders).
  let groups = data.groups || [];
  if (state.selectedFolder) {
    groups = groups.filter(g =>
      (g.images || []).some(img => isInFolder(img.path, state.selectedFolder))
    );
  }

  // Apply dynamic filters (filename, % diff, extension, file size).
  groups = applyFilters(groups);

  const container = document.getElementById('groups-container');
  container.innerHTML = '';

  if (groups.length === 0) {
    showEmptyState(data.status);
    return;
  }
  document.getElementById('empty-state').style.display = 'none';

  // Sort groups if a sort column is active.
  if (state.sortColumn) {
    groups = sortGroups(groups, state.sortColumn, state.sortDirection);
  }

  container.appendChild(buildResultsTable(groups));
  buildSidebarTree();
  updateBatchButtons();
  updateConfirmButton();
  // Replace feather icon placeholders in dynamically created elements.
  if (typeof feather !== 'undefined') feather.replace();

  // Restore active row highlight after re-render.
  if (state.selectedRowPath) {
    const row = container.querySelector('[data-path="' + CSS.escape(state.selectedRowPath) + '"]');
    if (row) row.classList.add('active');
  }
}

/** Update the stats display in the top bar. */
function updateStatsBar(stats) {
  if (stats.total_files == null) return;
  document.getElementById('stat-files').textContent = stats.total_files.toLocaleString();
  document.getElementById('stat-groups').textContent = (stats.duplicate_groups || 0).toLocaleString();
  document.getElementById('stat-savings').textContent = formatBytes(stats.wasted_bytes || 0);
  document.getElementById('stat-duration').textContent = formatDuration(stats.duration_ms);
}

/** Show the empty state message. */
function showEmptyState(status) {
  const empty = document.getElementById('empty-state');
  empty.style.display = 'block';
  const p = empty.querySelector('p');
  if (p) p.innerHTML = status === 'complete'
    ? 'No duplicates found. Your library is clean!'
    : 'Enter a folder path above and click <strong>Scan</strong> to find duplicate photos.';
}

/** Build the full results table with sortable header, group rows, image rows. */
function buildResultsTable(groups) {
  const table = document.createElement('table');
  table.className = 'results-table';
  // Make table focusable for keyboard navigation.
  table.tabIndex = 0;
  table.appendChild(buildTableHead());

  const tbody = document.createElement('tbody');
  for (const group of groups) appendGroupRows(tbody, group);
  table.appendChild(tbody);

  // Keyboard arrow navigation within the table.
  table.addEventListener('keydown', handleTableKeydown);
  return table;
}

/** Handle keyboard events on the table for arrow navigation. */
function handleTableKeydown(e) {
  if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
  e.preventDefault();

  const rows = Array.from(document.querySelectorAll('.image-row'));
  if (rows.length === 0) return;

  // Find the currently active row index.
  const currentIdx = rows.findIndex(r => r.classList.contains('active'));
  let nextIdx;
  if (e.key === 'ArrowDown') {
    nextIdx = currentIdx < 0 ? 0 : Math.min(currentIdx + 1, rows.length - 1);
  } else {
    nextIdx = currentIdx < 0 ? 0 : Math.max(currentIdx - 1, 0);
  }

  // Simulate click on the target row to select it and show preview.
  rows[nextIdx].click();
  // Scroll the row into view if needed.
  rows[nextIdx].scrollIntoView({ block: 'nearest', behavior: 'smooth' });
}

/** Column definitions for the table header. */
const COLUMNS = [
  { cls: 'col-check', sortable: false },
  { cls: 'col-diff',  key: 'diff',  text: '% Diff' },
  { cls: 'col-type',  key: 'type',  text: 'Type' },
  { cls: 'col-name',  key: 'name',  text: 'Filename' },
  { cls: 'col-path',  key: 'path',  text: 'File Path' },
  { cls: 'col-dim',   key: 'dim',   text: 'Dimensions' },
  { cls: 'col-ext',   key: 'ext',   text: 'Ext' },
  { cls: 'col-size',  key: 'size',  text: 'File Size' },
  { cls: 'col-block', key: 'block', text: 'Blockiness' },
  { cls: 'col-blur',  key: 'blur',  text: 'Blurring' },
  { cls: 'col-actions', sortable: false, text: '' }
];

/** Build the table header with sortable columns. */
function buildTableHead() {
  const thead = document.createElement('thead');
  const tr = document.createElement('tr');
  for (const c of COLUMNS) {
    const th = document.createElement('th');
    th.className = c.cls;
    if (c.cls === 'col-check') {
      th.appendChild(createSelectAllCheckbox());
    } else {
      th.textContent = c.text || '';
      if (c.sortable !== false && c.key) {
        const arrow = document.createElement('span');
        arrow.className = 'sort-arrow';
        if (state.sortColumn === c.key) {
          arrow.textContent = state.sortDirection === 'asc' ? '\u25B2' : '\u25BC';
          th.classList.add('sort-' + state.sortDirection);
        } else {
          arrow.textContent = '\u21C5';
        }
        th.appendChild(arrow);
        th.addEventListener('click', () => handleSort(c.key));
      }
    }
    tr.appendChild(th);
  }
  thead.appendChild(tr);
  return thead;
}

/** Handle clicking a sortable column header. */
function handleSort(key) {
  if (state.sortColumn === key) {
    state.sortDirection = state.sortDirection === 'asc' ? 'desc' : 'asc';
  } else {
    state.sortColumn = key;
    state.sortDirection = 'asc';
  }
  if (state.scanResult) renderResults(state.scanResult);
}

/** Sort groups' images by the given column key and direction. */
function sortGroups(groups, key, dir) {
  return groups.map(g => {
    const images = [...(g.images || [])];
    images.sort((a, b) => {
      let va = getSortValue(a, key);
      let vb = getSortValue(b, key);
      if (typeof va === 'string') { va = va.toLowerCase(); vb = (vb || '').toLowerCase(); }
      if (va < vb) return dir === 'asc' ? -1 : 1;
      if (va > vb) return dir === 'asc' ? 1 : -1;
      return 0;
    });
    return { ...g, images };
  });
}

/** Extract a sortable value from an image for the given column key. */
function getSortValue(img, key) {
  switch (key) {
    case 'name':  return img.filename || '';
    case 'path':  return img.path || '';
    case 'dim':   return (img.width || 0) * (img.height || 0);
    case 'ext':   return extractExt(img.filename);
    case 'size':  return img.size || 0;
    case 'block': return img.blockiness != null ? img.blockiness : 0;
    case 'blur':  return img.blurring != null ? img.blurring : 0;
    default:      return '';
  }
}

/** Create the "select all" checkbox for the header. */
function createSelectAllCheckbox() {
  const cb = document.createElement('input');
  cb.type = 'checkbox';
  cb.title = 'Select all groups';
  cb.addEventListener('change', () => {
    const groups = (state.scanResult && state.scanResult.groups) || [];
    if (cb.checked) groups.forEach(g => state.selectedGroups.add(g.id));
    else state.selectedGroups.clear();
    document.querySelectorAll('.group-checkbox').forEach(gcb => { gcb.checked = cb.checked; });
    updateBatchButtons();
  });
  return cb;
}

/** Append group header row + image rows to tbody. */
function appendGroupRows(tbody, group) {
  const images = group.images || [];
  const matchType = group.match_type || 'perceptual';
  const diff = group.confidence != null ? (100 - Number(group.confidence)).toFixed(1) : '--';
  const wasted = images.slice(1).reduce((s, img) => s + (img.size || 0), 0);
  const isCollapsed = state.collapsedGroups.has(group.id);

  // Group header row.
  const gRow = document.createElement('tr');
  gRow.className = 'group-row' + (isCollapsed ? ' collapsed' : '');
  gRow.setAttribute('data-group-id', group.id || '');

  // Checkbox cell.
  const cbTd = document.createElement('td');
  const cb = document.createElement('input');
  cb.type = 'checkbox';
  cb.className = 'group-checkbox';
  cb.checked = state.selectedGroups.has(group.id);
  cb.addEventListener('change', (e) => {
    e.stopPropagation();
    if (cb.checked) state.selectedGroups.add(group.id);
    else state.selectedGroups.delete(group.id);
    updateBatchButtons();
  });
  cbTd.appendChild(cb);
  gRow.appendChild(cbTd);

  // % diff cell.
  const diffTd = document.createElement('td');
  diffTd.textContent = diff + '%';
  gRow.appendChild(diffTd);

  // Type badge cell.
  const typeTd = document.createElement('td');
  const badge = document.createElement('span');
  badge.className = 'badge badge-' + matchType;
  badge.textContent = matchType === 'exact' ? 'Exact' : matchType === 'series' ? 'Series' : 'Perceptual';
  typeTd.appendChild(badge);
  gRow.appendChild(typeTd);

  // Summary spanning remaining columns.
  const sumTd = document.createElement('td');
  sumTd.colSpan = 7;
  sumTd.className = 'group-summary-cell';
  const arrow = isCollapsed ? '\u25B6' : '\u25BC';
  sumTd.textContent = arrow + ' ' + images.length + ' images \u00B7 ' + formatBytes(wasted) + ' wasted';
  gRow.appendChild(sumTd);

  // Actions cell.
  const actTd = document.createElement('td');
  if (matchType === 'perceptual' && group.id) {
    const mBtn = document.createElement('button');
    mBtn.className = 'btn-mismatch';
    mBtn.textContent = 'Mismatch';
    mBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      window.reportMismatch(group.id);
    });
    actTd.appendChild(mBtn);
  }
  gRow.appendChild(actTd);

  // Click on group row to toggle collapse/expand.
  gRow.addEventListener('click', (e) => {
    if (e.target.type === 'checkbox') return;
    if (state.collapsedGroups.has(group.id)) {
      state.collapsedGroups.delete(group.id);
    } else {
      state.collapsedGroups.add(group.id);
    }
    if (state.scanResult) renderResults(state.scanResult);
  });

  tbody.appendChild(gRow);

  // Image rows (hidden when collapsed).
  if (!isCollapsed) {
    for (const img of images) tbody.appendChild(buildImageRow(img, group.id));
  }
}

/** Build a table row for a single image. */
function buildImageRow(img, groupId) {
  const tr = document.createElement('tr');
  const isPending = state.pendingDeletions.has(img.path);
  // Check if promoted by user.
  const isPromoted = state.promotedImages.has(img.path);
  const isOriginal = img.is_best || isPromoted;
  tr.className = 'image-row' + (isPending ? ' pending-delete' : '');
  tr.setAttribute('data-path', img.path || '');
  if (state.selectedRowPath === img.path) tr.classList.add('active');

  // Click to select and show preview in the left panel.
  tr.addEventListener('click', () => {
    document.querySelectorAll('.image-row.active').forEach(r => r.classList.remove('active'));
    tr.classList.add('active');
    state.selectedRowPath = img.path;
    showPreview(img);
  });

  // Mouseover to show a quick hover preview tooltip near the mouse.
  tr.addEventListener('mouseenter', (e) => showHoverPreview(e, img));
  tr.addEventListener('mousemove', moveHoverPreview);
  tr.addEventListener('mouseleave', hideHoverPreview);

  // Empty checkbox cell.
  tr.appendChild(document.createElement('td'));
  // Empty diff cell.
  tr.appendChild(document.createElement('td'));

  // Original/Duplicate badge cell — with promote button for non-originals.
  const statusTd = document.createElement('td');
  statusTd.className = 'cell-status';
  const sBadge = document.createElement('span');
  sBadge.className = 'badge badge-sm ' + (isOriginal ? 'badge-keep' : 'badge-delete');
  sBadge.textContent = isOriginal ? 'Original' : 'Duplicate';
  statusTd.appendChild(sBadge);

  // Promote button — only show for non-original images.
  if (!isOriginal) {
    const promBtn = document.createElement('button');
    promBtn.className = 'btn-promote';
    promBtn.innerHTML = '<i data-feather="arrow-up-circle"></i>';
    promBtn.title = 'Promote to Original';
    promBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      window.promoteImage(img.path);
    });
    statusTd.appendChild(promBtn);
  }
  tr.appendChild(statusTd);

  // Filename.
  addCell(tr, img.filename || 'Unknown', 'cell-name', img.path);
  // Path.
  addCell(tr, extractDir(img.path), 'cell-path', img.path);
  // Dimensions.
  addCell(tr, (img.width && img.height) ? img.width + 'x' + img.height : '--');
  // Extension.
  addCell(tr, extractExt(img.filename));
  // File size.
  addCell(tr, formatBytes(img.size));
  // Blockiness.
  addCell(tr, img.blockiness != null ? Number(img.blockiness).toFixed(2) : '--');
  // Blurring.
  addCell(tr, img.blurring != null ? Number(img.blurring).toFixed(2) : '--');

  // Delete / Undo button cell.
  const delTd = document.createElement('td');
  if (isPending) {
    const undoBtn = document.createElement('button');
    undoBtn.className = 'btn-del-icon btn-undo-icon';
    undoBtn.innerHTML = '<i data-feather="rotate-ccw"></i>';
    undoBtn.title = 'Undo: remove from deletion list';
    undoBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      window.unmarkFile(img.path);
    });
    delTd.appendChild(undoBtn);
  } else {
    const delBtn = document.createElement('button');
    delBtn.className = 'btn-del-icon';
    delBtn.innerHTML = '<i data-feather="trash-2"></i>';
    delBtn.title = 'Mark for deletion';
    delBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      window.deleteFile(img.path);
    });
    delTd.appendChild(delBtn);
  }
  tr.appendChild(delTd);

  return tr;
}

/** Show hover preview tooltip near the mouse pointer (to the right). */
function showHoverPreview(e, img) {
  hideHoverPreview();
  const tip = document.createElement('div');
  tip.className = 'hover-preview';
  tip.id = 'hover-preview-tip';
  const thumb = document.createElement('img');
  thumb.alt = img.filename || '';
  thumb.onerror = function () { tip.style.display = 'none'; };
  apiGetThumbnail(img.path || '').then(b64 => { if (b64) thumb.src = 'data:image/jpeg;base64,' + b64; else tip.style.display = 'none'; });
  tip.appendChild(thumb);

  const info = document.createElement('div');
  info.className = 'hover-preview-info';
  info.textContent = (img.filename || '') + ' \u2022 ' + formatBytes(img.size);
  tip.appendChild(info);

  document.body.appendChild(tip);
  positionHoverPreview(tip, e.clientX, e.clientY);
}

/** Reposition hover preview as mouse moves. */
function moveHoverPreview(e) {
  const tip = document.getElementById('hover-preview-tip');
  if (tip) positionHoverPreview(tip, e.clientX, e.clientY);
}

/** Position the hover preview to the right of the mouse cursor. */
function positionHoverPreview(tip, mouseX, mouseY) {
  const tipW = 270;
  const offset = 15;
  let left = mouseX + offset;
  let top = mouseY - 20;

  // If off-screen right, show on the left of mouse.
  if (left + tipW > window.innerWidth) {
    left = mouseX - tipW - offset;
  }
  // Keep within vertical bounds.
  if (top < 0) top = 0;
  if (top + 250 > window.innerHeight) {
    top = Math.max(0, window.innerHeight - 260);
  }

  tip.style.top = top + 'px';
  tip.style.left = left + 'px';
}

/** Hide the hover preview tooltip. */
function hideHoverPreview() {
  const existing = document.getElementById('hover-preview-tip');
  if (existing) existing.remove();
}

/** Helper: add a text cell to a row. */
function addCell(tr, text, cls, title) {
  const td = document.createElement('td');
  if (cls) td.className = cls;
  td.textContent = text;
  if (title) td.title = title;
  tr.appendChild(td);
}

/** Extract directory from a full file path. */
function extractDir(p) {
  if (!p) return '';
  const i = Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\'));
  return i > 0 ? p.substring(0, i) : '/';
}

/** Extract file extension (e.g. "JPG"). */
function extractExt(f) {
  if (!f) return '';
  const d = f.lastIndexOf('.');
  return d >= 0 ? f.substring(d + 1).toUpperCase() : '';
}

/** Check if a file path is inside a given folder. */
function isInFolder(filePath, folder) {
  if (!filePath || !folder) return false;
  const n = filePath.replace(/\\/g, '/');
  const f = folder.replace(/\\/g, '/');
  return n.startsWith(f + '/') || n.startsWith(f + '\\');
}
