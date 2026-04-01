// browse.js — Native OS folder picker integration.
//
// Clicking "Browse..." opens the OS-native folder dialog (Windows Explorer /
// macOS Finder) via Wails' runtime.OpenDirectoryDialog. Selected folders are
// shown as removable chips in the top bar.
//
// Multiple folders are supported: each click opens a fresh dialog and appends
// the result. The scan-path input is kept in sync for scan.js to read.

import { apiOpenFolderDialog } from './api.js';
import { showToast } from './components.js';

// Module-level set of currently selected folder paths.
const selectedPaths = new Set();

/** Wire up the Browse button and manual path entry. */
export function initBrowse() {
  document.getElementById('browse-btn').addEventListener('click', openNativeDialog);

  // Allow manual path entry: type a path in the input and press Enter to add it.
  const input = document.getElementById('scan-path');
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      const p = input.value.trim();
      if (p) {
        addPath(p);
        input.value = '';
      }
    }
  });

  // Inject the "Include subfolders" checkbox next to the browse button.
  // (Previously this lived inside the custom modal dialog.)
  injectSubfoldersCheckbox();
}

/** Open the native OS folder picker and add the result. */
async function openNativeDialog() {
  try {
    const dir = await apiOpenFolderDialog();
    if (dir) addPath(dir);
  } catch (err) {
    showToast('Browse failed: ' + err.message, 'error');
  }
}

/** Add a folder path to the selected set (no-op if already present). */
function addPath(p) {
  if (selectedPaths.has(p)) {
    showToast('Folder already added', 'info');
    return;
  }
  selectedPaths.add(p);
  syncUI();
}

/** Remove a folder path from the selected set. */
function removePath(p) {
  selectedPaths.delete(p);
  syncUI();
}

/**
 * Keep the hidden scan-path input and the visible chip list in sync with
 * the current selectedPaths set. scan.js reads scan-path for the ScanRequest.
 */
function syncUI() {
  // Update the text input (semicolon-separated; scan.js splits on '; ').
  document.getElementById('scan-path').value = Array.from(selectedPaths).join('; ');

  // Find or create the chips container, inserted after the input-group.
  let container = document.getElementById('browse-chips');
  if (!container) {
    container = document.createElement('div');
    container.id = 'browse-chips';
    container.className = 'browse-selected-list';
    const inputGroup = document.getElementById('scan-path').closest('.input-group');
    inputGroup.parentNode.insertBefore(container, inputGroup.nextSibling);
  }

  container.innerHTML = '';
  if (selectedPaths.size === 0) {
    container.style.display = 'none';
    return;
  }
  container.style.display = 'flex';

  for (const p of selectedPaths) {
    const chip = document.createElement('span');
    chip.className = 'browse-selected-chip';
    chip.textContent = shortenPath(p);
    chip.title = p;

    const x = document.createElement('span');
    x.className = 'browse-chip-remove';
    x.textContent = '\u00D7';
    x.addEventListener('click', () => removePath(p));
    chip.appendChild(x);
    container.appendChild(chip);
  }
}

/** Shorten a path to at most its last 2 segments for chip display. */
function shortenPath(fullPath) {
  if (!fullPath) return '/';
  const sep = fullPath.includes('\\') ? '\\' : '/';
  const parts = fullPath.split(sep).filter(Boolean);
  if (parts.length <= 2) return fullPath;
  return '...' + sep + parts.slice(-2).join(sep);
}

/**
 * Inject an "Include subfolders" checkbox into the top bar, adjacent to the
 * Browse button. Stores its state in window._includeSubfolders (read by scan.js).
 */
function injectSubfoldersCheckbox() {
  const browseBtn = document.getElementById('browse-btn');
  if (!browseBtn || document.getElementById('include-subfolders-label')) return;

  const label = document.createElement('label');
  label.id = 'include-subfolders-label';
  label.className = 'browse-subfolder-label';

  const cb = document.createElement('input');
  cb.type = 'checkbox';
  cb.id = 'include-subfolders-cb';
  cb.checked = window._includeSubfolders !== false; // Default: true.
  cb.addEventListener('change', () => { window._includeSubfolders = cb.checked; });

  label.appendChild(cb);
  label.appendChild(document.createTextNode(' Subfolders'));
  browseBtn.parentNode.insertBefore(label, browseBtn.nextSibling);
}

/** Expose selected paths for scan.js to read if needed. */
export function getSelectedPaths() {
  return Array.from(selectedPaths);
}
