// render.js — Central zone: render scan results as an interactive table.

import { state } from './state.js';
import { formatBytes, formatDuration } from './helpers.js';
import { resetScanUI } from './scan.js';
import { buildSidebarTree } from './sidebar.js';
import { showPreview, clearPreview } from './preview.js';
import { updateBatchButtons } from './actions.js';

/**
 * Render full scan results into the main area as a table.
 * Respects state.selectedFolder for sidebar filtering.
 */
export function renderResults(data) {
  resetScanUI();
  state.scanResult = data;

  // Update stats bar.
  updateStatsBar(data.stats || {});

  // Filter groups by selected folder if set.
  let groups = data.groups || [];
  if (state.selectedFolder) {
    groups = groups.filter(g =>
      (g.images || []).some(img => isInFolder(img.path, state.selectedFolder))
    );
  }

  const container = document.getElementById('groups-container');
  container.innerHTML = '';

  if (groups.length === 0) {
    showEmptyState(data.status);
    return;
  }
  document.getElementById('empty-state').style.display = 'none';

  // Build the results table.
  const table = buildResultsTable(groups);
  container.appendChild(table);

  // Build sidebar tree after rendering results.
  buildSidebarTree();
  // Update batch action buttons visibility.
  updateBatchButtons();
}

/** Update the stats bar at the bottom of the page. */
function updateStatsBar(stats) {
  if (stats.total_files != null) {
    const bar = document.getElementById('stats-bar');
    bar.style.display = 'grid';
    document.getElementById('stat-files').textContent = stats.total_files.toLocaleString();
    document.getElementById('stat-groups').textContent = (stats.duplicate_groups || 0).toLocaleString();
    document.getElementById('stat-savings').textContent = formatBytes(stats.wasted_bytes || 0);
    document.getElementById('stat-duration').textContent = formatDuration(stats.duration_ms);
  }
}

/** Show the empty state message. */
function showEmptyState(status) {
  const empty = document.getElementById('empty-state');
  empty.style.display = 'block';
  empty.querySelector('p').textContent = status === 'complete'
    ? 'No duplicates found. Your library is clean!' : 'No duplicates in this folder.';
}

/** Check if a file path is inside a given folder. */
function isInFolder(filePath, folder) {
  if (!filePath || !folder) return false;
  const n = filePath.replace(/\\/g, '/');
  const f = folder.replace(/\\/g, '/');
  return n.startsWith(f + '/') || n.startsWith(f + '\\');
}

/**
 * Build the full results table with group header rows and image rows.
 */
function buildResultsTable(groups) {
  const table = document.createElement('table');
  table.className = 'results-table';

  // Table header.
  const thead = document.createElement('thead');
  const hRow = document.createElement('tr');
  const headers = [
    { cls: 'col-check', content: createSelectAllCheckbox() },
    { cls: 'col-diff', text: '% Diff' },
    { cls: 'col-type', text: 'Type' },
    { cls: 'col-name', text: 'File Name' },
    { cls: 'col-path', text: 'File Path' },
    { cls: 'col-dim', text: 'Dimensions' },
    { cls: 'col-ext', text: 'Ext' },
    { cls: 'col-size', text: 'Size' },
    { cls: 'col-actions', text: '' }
  ];
  for (const h of headers) {
    const th = document.createElement('th');
    th.className = h.cls;
    if (h.content) th.appendChild(h.content);
    else th.textContent = h.text || '';
    hRow.appendChild(th);
  }
  thead.appendChild(hRow);
  table.appendChild(thead);

  // Table body with groups and images.
  const tbody = document.createElement('tbody');
  for (const group of groups) {
    appendGroupRows(tbody, group);
  }
  table.appendChild(tbody);
  return table;
}

/** Create the "select all" checkbox for the table header. */
function createSelectAllCheckbox() {
  const cb = document.createElement('input');
  cb.type = 'checkbox';
  cb.title = 'Select all groups';
  cb.addEventListener('change', () => {
    const groups = (state.scanResult && state.scanResult.groups) || [];
    if (cb.checked) {
      groups.forEach(g => state.selectedGroups.add(g.id));
    } else {
      state.selectedGroups.clear();
    }
    // Update all group checkboxes in the table.
    document.querySelectorAll('.group-checkbox').forEach(gcb => {
      gcb.checked = cb.checked;
    });
    updateBatchButtons();
  });
  return cb;
}

/**
 * Append a group header row + image rows to the table body.
 */
function appendGroupRows(tbody, group) {
  const images = group.images || [];
  const isExact = group.match_type === 'exact';
  const diff = group.confidence != null ? (100 - Number(group.confidence)).toFixed(1) : '--';
  const wastedBytes = images.slice(1).reduce((s, img) => s + (img.size || 0), 0);

  // Group header row.
  const gRow = document.createElement('tr');
  gRow.className = 'group-row';
  gRow.setAttribute('data-group-id', group.id || '');

  // Checkbox cell.
  const cbTd = document.createElement('td');
  const cb = document.createElement('input');
  cb.type = 'checkbox';
  cb.className = 'group-checkbox';
  cb.checked = state.selectedGroups.has(group.id);
  cb.addEventListener('change', () => {
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
  badge.className = 'badge ' + (isExact ? 'badge-exact' : 'badge-perceptual');
  badge.textContent = isExact ? 'Exact' : 'Perceptual';
  typeTd.appendChild(badge);
  gRow.appendChild(typeTd);

  // Summary spanning remaining columns.
  const sumTd = document.createElement('td');
  sumTd.colSpan = 5;
  sumTd.className = 'group-summary-cell';
  sumTd.textContent = images.length + ' images \u00B7 ' + formatBytes(wastedBytes) + ' wasted';
  gRow.appendChild(sumTd);

  // Mismatch button cell (for perceptual matches).
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

  // Image rows for this group.
  for (const img of images) {
    tbody.appendChild(buildImageRow(img));
  }
}

/**
 * Build a table row for a single image with file details.
 * Clicking the row shows the image preview in Zone 5.
 */
function buildImageRow(img) {
  const tr = document.createElement('tr');
  tr.className = 'image-row';
  tr.setAttribute('data-path', img.path || '');

  // Highlight if this is the currently previewed image.
  if (state.previewImage && state.previewImage.path === img.path) {
    tr.classList.add('active');
  }

  // Click to show preview.
  tr.addEventListener('click', () => {
    document.querySelectorAll('.image-row.active').forEach(r => r.classList.remove('active'));
    tr.classList.add('active');
    showPreview(img);
  });

  // Empty checkbox cell (only groups have checkboxes).
  tr.appendChild(document.createElement('td'));
  // Empty diff cell.
  tr.appendChild(document.createElement('td'));

  // KEEP/DELETE status cell.
  const statusTd = document.createElement('td');
  const sBadge = document.createElement('span');
  sBadge.className = 'badge badge-sm ' + (img.is_best ? 'badge-keep' : 'badge-delete');
  sBadge.textContent = img.is_best ? 'KEEP' : 'DEL';
  statusTd.appendChild(sBadge);
  tr.appendChild(statusTd);

  // File name cell.
  const nameTd = document.createElement('td');
  nameTd.className = 'cell-name';
  nameTd.textContent = img.filename || 'Unknown';
  nameTd.title = img.path || '';
  tr.appendChild(nameTd);

  // File path cell (directory only).
  const pathTd = document.createElement('td');
  pathTd.className = 'cell-path';
  pathTd.textContent = extractDir(img.path);
  pathTd.title = img.path || '';
  tr.appendChild(pathTd);

  // Dimensions cell.
  const dimTd = document.createElement('td');
  dimTd.textContent = (img.width && img.height) ? (img.width + 'x' + img.height) : '--';
  tr.appendChild(dimTd);

  // Extension cell.
  const extTd = document.createElement('td');
  extTd.textContent = extractExt(img.filename);
  tr.appendChild(extTd);

  // File size cell.
  const sizeTd = document.createElement('td');
  sizeTd.textContent = formatBytes(img.size);
  tr.appendChild(sizeTd);

  // Delete button cell.
  const delTd = document.createElement('td');
  const delBtn = document.createElement('button');
  delBtn.className = 'btn btn-danger btn-sm';
  delBtn.textContent = 'Del';
  delBtn.addEventListener('click', (e) => {
    e.stopPropagation();
    window.deleteFile(img.path);
  });
  delTd.appendChild(delBtn);
  tr.appendChild(delTd);

  return tr;
}

/** Extract directory path from a full file path. */
function extractDir(filePath) {
  if (!filePath) return '';
  const lastSlash = Math.max(filePath.lastIndexOf('/'), filePath.lastIndexOf('\\'));
  return lastSlash > 0 ? filePath.substring(0, lastSlash) : '/';
}

/** Extract file extension from a filename (e.g. ".jpg"). */
function extractExt(filename) {
  if (!filename) return '';
  const dot = filename.lastIndexOf('.');
  return dot >= 0 ? filename.substring(dot).toLowerCase() : '';
}
