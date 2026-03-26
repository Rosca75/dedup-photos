// actions.js — File actions: mark for deletion, confirm, batch operations, mismatch.

import { state } from './state.js';
import { apiDelete, apiMismatchReport } from './api.js';
import { showToast } from './components.js';
import { renderResults } from './table.js';
import { clearPreview } from './preview.js';

/** Wire up global action handlers, batch buttons, and confirm-deletions button. */
export function initActions() {
  window.deleteFile = markForDeletion;
  window.unmarkFile = unmarkForDeletion;
  window.reportMismatch = reportMismatch;
  document.getElementById('batch-keep-best').addEventListener('click', batchKeepBest);
  document.getElementById('batch-delete-all').addEventListener('click', batchDeleteAll);
  document.getElementById('confirm-deletions-btn').addEventListener('click', confirmDeletions);
}

/** Show or hide batch action buttons based on group selection. */
export function updateBatchButtons() {
  const wrapper = document.getElementById('batch-actions');
  if (!wrapper) return;
  const count = state.selectedGroups.size;
  wrapper.style.display = count > 0 ? 'flex' : 'none';
  const label = document.getElementById('batch-count');
  if (label) label.textContent = count + ' group' + (count !== 1 ? 's' : '');
}

/** Update the confirm-deletions button visibility and count. */
export function updateConfirmButton() {
  const btn = document.getElementById('confirm-deletions-btn');
  if (!btn) return;
  const count = state.pendingDeletions.size;
  if (count > 0) {
    btn.style.display = 'inline-flex';
    btn.textContent = 'Confirm ' + count + ' deletion' + (count !== 1 ? 's' : '');
  } else {
    btn.style.display = 'none';
  }
}

/** Mark a file for deletion (visual only, no API call yet). */
function markForDeletion(path) {
  state.pendingDeletions.add(path);
  updateConfirmButton();
  if (state.scanResult) renderResults(state.scanResult);
}

/** Unmark a file from pending deletion. */
function unmarkForDeletion(path) {
  state.pendingDeletions.delete(path);
  updateConfirmButton();
  if (state.scanResult) renderResults(state.scanResult);
}

/** Confirm and permanently delete all pending files. */
async function confirmDeletions() {
  const paths = Array.from(state.pendingDeletions);
  if (paths.length === 0) return;

  let ok = 0, fail = 0;
  for (const path of paths) {
    try {
      const data = await apiDelete(path);
      if (data.success) {
        ok++;
        removeImageFromState(path);
        state.pendingDeletions.delete(path);
      } else { fail++; }
    } catch (e) { fail++; }
  }

  clearPreview();
  updateConfirmButton();
  if (state.scanResult) renderResults(state.scanResult);
  showToast(ok + ' file(s) permanently deleted' + (fail > 0 ? ', ' + fail + ' failed' : ''), fail > 0 ? 'error' : 'success');
}

/** Remove an image from state and clean up empty groups. */
function removeImageFromState(path) {
  if (!state.scanResult || !state.scanResult.groups) return;
  for (const group of state.scanResult.groups) {
    group.images = (group.images || []).filter(img => img.path !== path);
  }
  state.scanResult.groups = state.scanResult.groups.filter(g => (g.images || []).length >= 2);
}

/** Batch action: keep only the best image in each selected group. */
function batchKeepBest() {
  const paths = collectPathsForBatch(true);
  if (paths.length === 0) { showToast('No files to delete.', 'error'); return; }
  paths.forEach(p => state.pendingDeletions.add(p));
  state.selectedGroups.clear();
  updateConfirmButton();
  updateBatchButtons();
  if (state.scanResult) renderResults(state.scanResult);
  showToast(paths.length + ' file(s) marked for deletion. Click "Confirm" to delete permanently.', 'success');
}

/** Batch action: delete ALL images in each selected group. */
function batchDeleteAll() {
  const paths = collectPathsForBatch(false);
  if (paths.length === 0) { showToast('No files to delete.', 'error'); return; }
  paths.forEach(p => state.pendingDeletions.add(p));
  state.selectedGroups.clear();
  updateConfirmButton();
  updateBatchButtons();
  if (state.scanResult) renderResults(state.scanResult);
  showToast(paths.length + ' file(s) marked for deletion. Click "Confirm" to delete permanently.', 'success');
}

/** Collect file paths for batch operation. keepBest=true skips best images. */
function collectPathsForBatch(keepBest) {
  if (!state.scanResult || !state.scanResult.groups) return [];
  const paths = [];
  for (const group of state.scanResult.groups) {
    if (!state.selectedGroups.has(group.id)) continue;
    for (const img of (group.images || [])) {
      if (keepBest && img.is_best) continue;
      if (img.path) paths.push(img.path);
    }
  }
  return paths;
}

/** Download a mismatch diagnostic report. */
function reportMismatch(groupId) {
  apiMismatchReport(groupId)
    .then(blob => {
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = 'mismatch_report_' + groupId.substring(0, 8) + '.json';
      a.click();
      URL.revokeObjectURL(url);
      showToast('Mismatch report downloaded.', 'success');
    })
    .catch(err => showToast('Report failed: ' + err.message, 'error'));
}
