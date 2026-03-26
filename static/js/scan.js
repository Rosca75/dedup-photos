// scan.js — Scan lifecycle: start, poll, cancel, progress updates.

import { state } from './state.js';
import { apiScan, apiResults, apiCancel } from './api.js';
import { showToast } from './components.js';
import { renderResults } from './table.js';

/** Wire up scan, cancel, refresh button event listeners. */
export function initScan() {
  document.getElementById('scan-btn').addEventListener('click', startScan);
  document.getElementById('cancel-btn').addEventListener('click', cancelScan);
  document.getElementById('refresh-btn').addEventListener('click', refreshResults);

  // Threshold slider: update value label in real time.
  const slider = document.getElementById('scan-threshold');
  const label = document.getElementById('threshold-value');
  if (slider && label) {
    slider.addEventListener('input', () => { label.textContent = slider.value + '%'; });
  }
}

/** Gather settings from the top bar and start a scan. */
function startScan() {
  const rawPath = document.getElementById('scan-path').value.trim();
  if (!rawPath) {
    showToast('Please enter a folder path.', 'error');
    document.getElementById('scan-path').focus();
    return;
  }

  // Support multiple paths separated by semicolons.
  const paths = rawPath.split(';').map(p => p.trim()).filter(Boolean);
  const path = paths[0]; // Primary path for backward compatibility.
  const extraPaths = paths.slice(1); // Additional paths to merge.

  // Read settings from inline top bar controls.
  const threshold = parseInt(document.getElementById('scan-threshold').value, 10) || 10;
  const algorithm = document.getElementById('setting-algorithm').value;
  const normalisedSize = parseInt(document.getElementById('setting-normalised-size').value, 10) || 32;
  const maxWidth = parseInt(document.getElementById('setting-max-width').value, 10) || 0;
  const maxHeight = parseInt(document.getElementById('setting-max-height').value, 10) || 0;
  const minFileSize = (parseInt(document.getElementById('setting-min-filesize').value, 10) || 0) * 1024;
  const maxFileSize = (parseInt(document.getElementById('setting-max-filesize').value, 10) || 0) * 1024 * 1024;

  const includeSubfolders = window._includeSubfolders !== false;

  const extensions = [];
  document.querySelectorAll('.topbar-extensions .ext-grid input[type="checkbox"]:checked')
    .forEach(cb => extensions.push(cb.value));

  // Clear promoted images for a fresh scan.
  state.pendingDeletions.clear();
  state.promotedImages.clear();

  setScanningUI(true);
  document.getElementById('groups-container').innerHTML = '';
  document.getElementById('empty-state').style.display = 'none';
  showProgress(true);
  updateProgress('Starting...', 0, 0);

  apiScan({
    path, threshold, algorithm, extensions,
    extra_paths: extraPaths,
    min_width: maxWidth, max_height: maxHeight,
    normalised_size: normalisedSize,
    include_subfolders: includeSubfolders,
    min_file_size: minFileSize,
    max_file_size: maxFileSize
  })
    .then(data => {
      if (data.status === 'complete') { stopPolling(); renderResults(data); }
      else { startPolling(); }
    })
    .catch(err => {
      showToast('Scan request failed: ' + err.message, 'error');
      resetScanUI();
    });
}

/** Cancel the currently running scan. */
function cancelScan() {
  apiCancel()
    .then(() => { showToast('Scan cancelled.', 'error'); stopPolling(); resetScanUI(); })
    .catch(err => showToast('Cancel failed: ' + err.message, 'error'));
}

/** Refresh: re-fetch results from server without re-scanning. */
function refreshResults() {
  apiResults()
    .then(data => { if (data.status === 'complete') renderResults(data); })
    .catch(err => showToast('Refresh failed: ' + err.message, 'error'));
}

/** Start polling for scan results every 500ms. */
function startPolling() {
  stopPolling();
  state.pollTimer = setInterval(loadResults, 500);
}

/** Stop the polling interval. */
function stopPolling() {
  if (state.pollTimer) { clearInterval(state.pollTimer); state.pollTimer = null; }
}

/** Fetch results from the API and update UI or finish scan. */
function loadResults() {
  apiResults()
    .then(data => {
      if (data.progress) {
        updateProgress(data.progress.phase || 'Scanning...', data.progress.current || 0, data.progress.total || 0);
      }
      if (data.status === 'complete' || data.status === 'idle' || data.status === 'cancelled') {
        stopPolling();
        renderResults(data);
      }
    })
    .catch(err => {
      stopPolling();
      showToast('Failed to fetch results: ' + err.message, 'error');
      resetScanUI();
    });
}

/** Reset scan buttons back to idle state. */
export function resetScanUI() {
  setScanningUI(false);
  showProgress(false);
}

/** Toggle scan/cancel button states. */
function setScanningUI(scanning) {
  const scanBtn = document.getElementById('scan-btn');
  scanBtn.disabled = scanning;
  document.getElementById('cancel-btn').style.display = scanning ? 'inline-flex' : 'none';
}

/** Show or hide the progress bar section. */
function showProgress(show) {
  const el = document.getElementById('progress-section');
  if (show) el.classList.add('active'); else el.classList.remove('active');
}

/** Update progress bar text and fill width. */
function updateProgress(phase, current, total) {
  document.getElementById('progress-phase').textContent = phase;
  document.getElementById('progress-counts').textContent = current + ' / ' + total;
  const pct = total > 0 ? Math.round(current / total * 100) : 0;
  document.getElementById('progress-fill').style.width = pct + '%';
}
