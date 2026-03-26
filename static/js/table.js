// table.js — Central zone: render scan results as a flat data table.
// Replaces the old render.js with sortable columns and group headers.

import { state } from './state.js';
import { formatBytes, formatDuration } from './helpers.js';
import { resetScanUI } from './scan.js';
import { buildSidebarTree } from './sidebar.js';
import { showPreview, clearPreview } from './preview.js';
import { updateBatchButtons } from './actions.js';
import { applyFilters } from './filters.js';

/**
 * Render full scan results into the main area as a data table.
 * Respects state.selectedFolder and dynamic filters.
 */
export function renderResults(data) {
  resetScanUI();
  state.scanResult = data;
  updateStatsBar(data.stats || {});

  // Apply folder filter.
  let groups = data.groups || [];
  if (state.selectedFolder) {
    groups = groups.filter(g =>
      (g.images || []).some(img => isInFolder(img.path, state.selectedFolder))
    );
  }

  // Apply dynamic filters (filename, % diff, extension).
  groups = applyFilters(groups);

  const container = document.getElementById('groups-container');
  container.innerHTML = '';

  if (groups.length === 0) {
    showEmptyState(data.status);
    return;
  }
  document.getElementById('empty-state').style.display = 'none';
  container.appendChild(buildResultsTable(groups));
  buildSidebarTree();
  updateBatchButtons();
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
  table.appendChild(buildTableHead());

  const tbody = document.createElement('tbody');
  for (const group of groups) appendGroupRows(tbody, group);
  table.appendChild(tbody);
  return table;
}

/** Build the table header with sortable columns. */
function buildTableHead() {
  const thead = document.createElement('thead');
  const tr = document.createElement('tr');
  const cols = [
    { cls: 'col-check', sortable: false, content: createSelectAllCheckbox() },
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
  for (const c of cols) {
    const th = document.createElement('th');
    th.className = c.cls;
    if (c.content) {
      th.appendChild(c.content);
    } else {
      th.textContent = c.text || '';
      if (c.sortable !== false) {
        const arrow = document.createElement('span');
        arrow.className = 'sort-arrow';
        arrow.textContent = '\u21C5';
        th.appendChild(arrow);
      }
    }
    tr.appendChild(th);
  }
  thead.appendChild(tr);
  return thead;
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
  const isExact = group.match_type === 'exact';
  const diff = group.confidence != null ? (100 - Number(group.confidence)).toFixed(1) : '--';
  const wasted = images.slice(1).reduce((s, img) => s + (img.size || 0), 0);

  // Group header row.
  const gRow = document.createElement('tr');
  gRow.className = 'group-row';
  gRow.setAttribute('data-group-id', group.id || '');

  // Checkbox.
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

  // % diff.
  const diffTd = document.createElement('td');
  diffTd.textContent = diff + '%';
  gRow.appendChild(diffTd);

  // Type badge.
  const typeTd = document.createElement('td');
  const badge = document.createElement('span');
  badge.className = 'badge ' + (isExact ? 'badge-exact' : 'badge-perceptual');
  badge.textContent = isExact ? 'Exact' : 'Perceptual';
  typeTd.appendChild(badge);
  gRow.appendChild(typeTd);

  // Summary spanning remaining columns.
  const sumTd = document.createElement('td');
  sumTd.colSpan = 7;
  sumTd.className = 'group-summary-cell';
  sumTd.textContent = images.length + ' images \u00B7 ' + formatBytes(wasted) + ' wasted';
  gRow.appendChild(sumTd);

  // Actions cell.
  const actTd = document.createElement('td');
  if (!isExact && group.id) {
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
  tbody.appendChild(gRow);

  // Image rows.
  for (const img of images) tbody.appendChild(buildImageRow(img));
}

/** Build a table row for a single image. */
function buildImageRow(img) {
  const tr = document.createElement('tr');
  tr.className = 'image-row';
  tr.setAttribute('data-path', img.path || '');
  if (state.selectedRowPath === img.path) tr.classList.add('active');

  tr.addEventListener('click', () => {
    document.querySelectorAll('.image-row.active').forEach(r => r.classList.remove('active'));
    tr.classList.add('active');
    state.selectedRowPath = img.path;
    showPreview(img);
  });

  // Empty checkbox cell.
  tr.appendChild(document.createElement('td'));
  // Empty diff cell.
  tr.appendChild(document.createElement('td'));

  // Original/Duplicate badge.
  const statusTd = document.createElement('td');
  const sBadge = document.createElement('span');
  sBadge.className = 'badge badge-sm ' + (img.is_best ? 'badge-keep' : 'badge-delete');
  sBadge.textContent = img.is_best ? 'Original' : 'Duplicate';
  statusTd.appendChild(sBadge);
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

  // Delete button.
  const delTd = document.createElement('td');
  const delBtn = document.createElement('button');
  delBtn.className = 'btn-del-icon';
  delBtn.innerHTML = '<i data-feather="trash-2"></i>';
  delBtn.title = 'Delete this file';
  delBtn.addEventListener('click', (e) => {
    e.stopPropagation();
    window.deleteFile(img.path);
  });
  delTd.appendChild(delBtn);
  tr.appendChild(delTd);

  return tr;
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
