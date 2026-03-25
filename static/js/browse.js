// browse.js — Folder browser dialog for selecting a scan path.
// Supports NAS/network paths via a manual path input field.

import { apiBrowse } from './api.js';
import { showToast } from './components.js';

/**
 * Wire up the Browse button click handler.
 */
export function initBrowse() {
  document.getElementById('browse-btn').addEventListener('click', () => openBrowse(''));
}

/**
 * Fetch directory listing from the API and show the browse dialog.
 */
function openBrowse(startPath) {
  apiBrowse(startPath)
    .then(data => showBrowseDialog(data))
    .catch(err => showToast('Browse failed: ' + err.message, 'error'));
}

/**
 * Build and display the folder browser dialog from API data.
 * Includes a manual path input for NAS/network paths (e.g. \\NAS\share).
 */
function showBrowseDialog(data) {
  // Remove any existing dialog.
  const existing = document.querySelector('.browse-overlay');
  if (existing) existing.parentNode.removeChild(existing);

  const overlay = document.createElement('div');
  overlay.className = 'browse-overlay';

  const box = document.createElement('div');
  box.className = 'browse-box';

  // Title.
  const h3 = document.createElement('h3');
  h3.textContent = 'Select Folder';
  box.appendChild(h3);

  // Manual path input row — for NAS/network paths.
  const pathRow = document.createElement('div');
  pathRow.className = 'browse-path-row';

  const pathInput = document.createElement('input');
  pathInput.type = 'text';
  pathInput.className = 'browse-path-input';
  pathInput.placeholder = 'Type a path (e.g. \\\\NAS\\share\\photos)';
  pathInput.value = data.current || '';

  const goBtn = document.createElement('button');
  goBtn.className = 'btn btn-primary btn-sm';
  goBtn.textContent = 'Go';
  goBtn.addEventListener('click', () => {
    const p = pathInput.value.trim();
    if (p) openBrowse(p);
  });

  // Also navigate on Enter key.
  pathInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      const p = pathInput.value.trim();
      if (p) openBrowse(p);
    }
  });

  pathRow.appendChild(pathInput);
  pathRow.appendChild(goBtn);
  box.appendChild(pathRow);

  // Current path display.
  const cur = document.createElement('div');
  cur.className = 'browse-current';
  cur.textContent = 'Current: ' + (data.current || '/');
  box.appendChild(cur);

  // Directory listing.
  const list = document.createElement('div');
  list.className = 'browse-list';

  // Parent directory entry (go up one level).
  if (data.parent) {
    const parentItem = document.createElement('div');
    parentItem.className = 'browse-item';
    parentItem.textContent = '.. (parent)';
    parentItem.addEventListener('click', () => openBrowse(data.parent));
    list.appendChild(parentItem);
  }

  // Child directory entries.
  const entries = data.entries || [];
  for (const entry of entries) {
    const item = document.createElement('div');
    item.className = 'browse-item';
    item.textContent = entry.name;
    item.addEventListener('click', () => openBrowse(entry.path));
    list.appendChild(item);
  }

  // Show placeholder if no entries exist.
  if (entries.length === 0 && !data.parent) {
    const empty = document.createElement('div');
    empty.className = 'browse-item';
    empty.textContent = '(no subdirectories)';
    empty.style.color = 'var(--muted)';
    list.appendChild(empty);
  }

  box.appendChild(list);

  // Action buttons: Select and Cancel.
  const actions = document.createElement('div');
  actions.className = 'browse-actions';

  const selectBtn = document.createElement('button');
  selectBtn.className = 'btn btn-primary';
  selectBtn.textContent = 'Select This Folder';
  selectBtn.addEventListener('click', () => {
    document.getElementById('scan-path').value = data.current;
    document.body.removeChild(overlay);
  });

  const cancelBtn = document.createElement('button');
  cancelBtn.className = 'btn';
  cancelBtn.style.cssText = 'background:var(--border);color:var(--text)';
  cancelBtn.textContent = 'Cancel';
  cancelBtn.addEventListener('click', () => document.body.removeChild(overlay));

  actions.appendChild(selectBtn);
  actions.appendChild(cancelBtn);
  box.appendChild(actions);
  overlay.appendChild(box);

  // Close on background click.
  overlay.addEventListener('click', (e) => {
    if (e.target === overlay) document.body.removeChild(overlay);
  });

  document.body.appendChild(overlay);
}
