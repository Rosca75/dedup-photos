// scan.js — Scan lifecycle: start, poll, cancel, progress updates.

import { state } from './state.js';
import { apiScan, apiResults, apiCancel } from './api.js';
import { showToast } from './components.js';
import { renderResults } from './render.js';

/**
 * Wire up scan and cancel button event listeners.
 */
export function initScan() {
  document.getElementById("scan-btn").addEventListener("click", startScan);
  document.getElementById("cancel-btn").addEventListener("click", cancelScan);
}

/**
 * Gather settings from the DOM and start a scan via the API.
 */
function startScan() {
  const path = document.getElementById("scan-path").value.trim();
  if (!path) {
    showToast("Please enter a folder path.", "error");
    document.getElementById("scan-path").focus();
    return;
  }

  // Read settings from the DOM inputs.
  const threshold = parseInt(document.getElementById("scan-threshold").value, 10) || 10;
  const algorithm = document.getElementById("setting-algorithm").value;
  const extStr = document.getElementById("setting-extensions").value.trim();
  const extensions = extStr ? extStr.split(",").map(s => s.trim()).filter(Boolean) : [];
  const minWidth = parseInt(document.getElementById("setting-min-width").value, 10) || 0;
  const maxHeight = parseInt(document.getElementById("setting-max-height").value, 10) || 0;

  // Update UI to scanning state.
  setScanningUI(true);
  document.getElementById("groups-container").innerHTML = "";
  document.getElementById("empty-state").style.display = "none";
  document.getElementById("stats-bar").style.display = "none";
  showProgress(true);
  updateProgress("Starting...", 0, 0);

  apiScan({ path, threshold, algorithm, extensions, min_width: minWidth, max_height: maxHeight })
    .then(data => {
      if (data.status === "complete") { stopPolling(); renderResults(data); }
      else { startPolling(); }
    })
    .catch(err => {
      showToast("Scan request failed: " + err.message, "error");
      resetScanUI();
    });
}

/** Cancel the currently running scan. */
function cancelScan() {
  apiCancel()
    .then(() => {
      showToast("Scan cancelled.", "error");
      stopPolling();
      resetScanUI();
    })
    .catch(err => showToast("Cancel failed: " + err.message, "error"));
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
        updateProgress(data.progress.phase || "Scanning...", data.progress.current || 0, data.progress.total || 0);
      }
      if (data.status === "complete" || data.status === "idle" || data.status === "cancelled") {
        stopPolling();
        renderResults(data);
      }
    })
    .catch(err => {
      stopPolling();
      showToast("Failed to fetch results: " + err.message, "error");
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
  const scanBtn = document.getElementById("scan-btn");
  scanBtn.disabled = scanning;
  scanBtn.textContent = scanning ? "Scanning..." : "Scan";
  document.getElementById("cancel-btn").style.display = scanning ? "inline-block" : "none";
}

/** Show or hide the progress bar section. */
function showProgress(show) {
  const el = document.getElementById("progress-section");
  if (show) el.classList.add("active"); else el.classList.remove("active");
}

/** Update progress bar text and fill width. */
function updateProgress(phase, current, total) {
  document.getElementById("progress-phase").textContent = phase;
  document.getElementById("progress-counts").textContent = current + " / " + total;
  const pct = (total > 0) ? Math.round(current / total * 100) : 0;
  document.getElementById("progress-fill").style.width = pct + "%";
}
