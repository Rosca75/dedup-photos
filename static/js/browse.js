// browse.js — Folder browser dialog for selecting scan paths.
// Supports multiple folder selection and NAS/network paths.

import { apiBrowse } from './api.js';
import { showToast } from './components.js';

/** Wire up the Browse button click handler. */
export function initBrowse() {
  document.getElementById('browse-btn').addEventListener('click', () => openBrowse(''));
}

/** Fetch directory listing from the API and show the browse dialog. */
function openBrowse(startPath) {
  apiBrowse(startPath)
    .then(data => showBrowseDialog(data))
    .catch(err => showToast('Browse failed: ' + err.message, 'error'));
}

/**
 * Build and display the folder browser dialog from API data.
 * Supports selecting multiple folders and include-subfolders option.
 */
function showBrowseDialog(data) {
  const existing = document.querySelector('.browse-overlay');
  if (existing) existing.parentNode.removeChild(existing);

  // Parse currently selected paths from the scan-path input.
  const currentPaths = parsePaths(document.getElementById('scan-path').value);

  const overlay = document.createElement('div');
  overlay.className = 'browse-overlay';

  const box = document.createElement('div');
  box.className = 'browse-box';

  // Title.
  const h3 = document.createElement('h3');
  h3.textContent = 'Select Folders';
  box.appendChild(h3);

  // Manual path input row.
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

  // Selected paths list (shown above the directory listing).
  const selectedPaths = new Set(currentPaths);
  const selectedListEl = document.createElement('div');
  selectedListEl.className = 'browse-selected-list';
  updateSelectedList(selectedListEl, selectedPaths);
  box.appendChild(selectedListEl);

  // Directory listing.
  const list = document.createElement('div');
  list.className = 'browse-list';

  if (data.parent) {
    const parentItem = document.createElement('div');
    parentItem.className = 'browse-item';
    parentItem.textContent = '.. (parent)';
    parentItem.addEventListener('click', () => openBrowse(data.parent));
    list.appendChild(parentItem);
  }

  const entries = data.entries || [];
  for (const entry of entries) {
    const item = document.createElement('div');
    item.className = 'browse-item';
    item.textContent = entry.name;
    // Double-click navigates into folder.
    item.addEventListener('dblclick', () => openBrowse(entry.path));
    // Single-click toggles selection of the folder.
    item.addEventListener('click', () => {
      if (selectedPaths.has(entry.path)) {
        selectedPaths.delete(entry.path);
        item.classList.remove('browse-item-selected');
      } else {
        selectedPaths.add(entry.path);
        item.classList.add('browse-item-selected');
      }
      updateSelectedList(selectedListEl, selectedPaths);
    });
    // Mark as selected if already in the set.
    if (selectedPaths.has(entry.path)) {
      item.classList.add('browse-item-selected');
    }
    list.appendChild(item);
  }

  if (entries.length === 0 && !data.parent) {
    const empty = document.createElement('div');
    empty.className = 'browse-item';
    empty.textContent = '(no subdirectories)';
    empty.style.color = 'var(--text-light)';
    list.appendChild(empty);
  }

  box.appendChild(list);

  // Include subfolders checkbox.
  const subRow = document.createElement('div');
  subRow.className = 'browse-subfolder-row';
  const subLabel = document.createElement('label');
  subLabel.className = 'browse-subfolder-label';
  const subCb = document.createElement('input');
  subCb.type = 'checkbox';
  subCb.checked = window._includeSubfolders !== false;
  subCb.addEventListener('change', () => {
    window._includeSubfolders = subCb.checked;
  });
  subLabel.appendChild(subCb);
  subLabel.appendChild(document.createTextNode(' Include subfolders'));
  subRow.appendChild(subLabel);
  box.appendChild(subRow);

  // Action buttons.
  const actions = document.createElement('div');
  actions.className = 'browse-actions';

  // "Add Current Folder" — adds the folder being browsed.
  const addBtn = document.createElement('button');
  addBtn.className = 'btn btn-outline btn-sm';
  addBtn.textContent = 'Add This Folder';
  addBtn.addEventListener('click', () => {
    if (data.current) {
      selectedPaths.add(data.current);
      updateSelectedList(selectedListEl, selectedPaths);
    }
  });

  const selectBtn = document.createElement('button');
  selectBtn.className = 'btn btn-primary';
  selectBtn.textContent = 'Done';
  selectBtn.addEventListener('click', () => {
    const paths = Array.from(selectedPaths).filter(Boolean);
    document.getElementById('scan-path').value = paths.join('; ');
    window._includeSubfolders = subCb.checked;
    document.body.removeChild(overlay);
  });

  const cancelBtn = document.createElement('button');
  cancelBtn.className = 'btn';
  cancelBtn.style.cssText = 'background:var(--border);color:var(--text)';
  cancelBtn.textContent = 'Cancel';
  cancelBtn.addEventListener('click', () => document.body.removeChild(overlay));

  actions.appendChild(addBtn);
  actions.appendChild(selectBtn);
  actions.appendChild(cancelBtn);
  box.appendChild(actions);
  overlay.appendChild(box);

  overlay.addEventListener('click', (e) => {
    if (e.target === overlay) document.body.removeChild(overlay);
  });

  document.body.appendChild(overlay);
}

/** Parse semicolon-separated paths from the scan input. */
function parsePaths(value) {
  if (!value) return [];
  return value.split(';').map(p => p.trim()).filter(Boolean);
}

/** Update the visual list of selected paths in the browse dialog. */
function updateSelectedList(container, pathSet) {
  container.innerHTML = '';
  if (pathSet.size === 0) {
    container.style.display = 'none';
    return;
  }
  container.style.display = 'block';
  for (const p of pathSet) {
    const chip = document.createElement('span');
    chip.className = 'browse-selected-chip';
    chip.textContent = shortenPath(p);
    chip.title = p;
    // Click to remove.
    const x = document.createElement('span');
    x.className = 'browse-chip-remove';
    x.textContent = '\u00D7';
    x.addEventListener('click', () => {
      pathSet.delete(p);
      updateSelectedList(container, pathSet);
    });
    chip.appendChild(x);
    container.appendChild(chip);
  }
}

/** Shorten a path to last 2 segments for chip display. */
function shortenPath(fullPath) {
  if (!fullPath) return '/';
  const sep = fullPath.includes('\\') ? '\\' : '/';
  const parts = fullPath.split(sep).filter(Boolean);
  if (parts.length <= 2) return fullPath;
  return '.../' + parts.slice(-2).join('/');
}
