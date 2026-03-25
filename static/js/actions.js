// actions.js — File actions: delete, mismatch report, batch operations.

import { state } from './state.js';
import { apiDelete, apiMismatchReport } from './api.js';
import { showToast, showConfirm } from './components.js';
import { renderResults } from './render.js';
import { clearPreview } from './preview.js';

/**
 * Wire up global action handlers (exposed on window for inline calls).
 * Also wire up batch action buttons in the top bar.
 */
export function initActions() {
  window.deleteFile = deleteFile;
  window.reportMismatch = reportMismatch;

  // Batch action buttons (hidden by default, shown when groups are selected).
  document.getElementById('batch-keep-best').addEventListener('click', batchKeepBest);
  document.getElementById('batch-delete-all').addEventListener('click', batchDeleteAll);
}

/**
 * Show or hide batch action buttons based on group selection.
 */
export function updateBatchButtons() {
  const wrapper = document.getElementById('batch-actions');
  if (!wrapper) return;
  const count = state.selectedGroups.size;
  wrapper.style.display = count > 0 ? 'flex' : 'none';
  // Update label with count.
  const label = document.getElementById('batch-count');
  if (label) label.textContent = count + ' group' + (count !== 1 ? 's' : '');
}

/**
 * Prompt the user, then permanently delete a file by path.
 * Removes the image row from the DOM and cleans up empty groups.
 */
function deleteFile(path) {
  showConfirm('Permanently delete ' + path + '?', () => {
    apiDelete(path)
      .then(data => {
        if (data.success) {
          showToast('Deleted: ' + path, 'success');
          removeImageFromState(path);
          if (state.scanResult) renderResults(state.scanResult);
          clearPreview();
        } else {
          showToast('Delete failed: ' + (data.error || 'Unknown error'), 'error');
        }
      })
      .catch(err => showToast('Delete failed: ' + err.message, 'error'));
  });
}

/**
 * Remove an image from state.scanResult by path.
 * Also removes groups that have fewer than 2 images left.
 */
function removeImageFromState(path) {
  if (!state.scanResult || !state.scanResult.groups) return;
  for (const group of state.scanResult.groups) {
    group.images = (group.images || []).filter(img => img.path !== path);
  }
  // Remove groups with fewer than 2 images.
  state.scanResult.groups = state.scanResult.groups.filter(g => (g.images || []).length >= 2);
}

/**
 * Batch action: Keep only the best image in each selected group.
 * Deletes all non-best images in the selected groups.
 */
function batchKeepBest() {
  const paths = collectPathsForBatch(/* keepBest */ true);
  if (paths.length === 0) {
    showToast('No files to delete (all are best).', 'error');
    return;
  }
  const count = paths.length;
  const groupCount = state.selectedGroups.size;
  showConfirm('Delete ' + count + ' file(s) from ' + groupCount + ' group(s), keeping only the best?', () => {
    executeBatchDelete(paths);
  });
}

/**
 * Batch action: Delete ALL images in each selected group.
 */
function batchDeleteAll() {
  const paths = collectPathsForBatch(/* keepBest */ false);
  if (paths.length === 0) {
    showToast('No files to delete.', 'error');
    return;
  }
  const count = paths.length;
  const groupCount = state.selectedGroups.size;
  showConfirm('Delete ALL ' + count + ' file(s) from ' + groupCount + ' group(s)? This cannot be undone.', () => {
    executeBatchDelete(paths);
  });
}

/**
 * Collect file paths to delete for the batch operation.
 * If keepBest=true, only collect non-best images. If false, collect all.
 */
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

/**
 * Execute sequential deletes for a list of file paths.
 * Shows progress toasts and re-renders when done.
 */
async function executeBatchDelete(paths) {
  let ok = 0;
  let fail = 0;
  for (const path of paths) {
    try {
      const data = await apiDelete(path);
      if (data.success) {
        ok++;
        removeImageFromState(path);
      } else { fail++; }
    } catch (e) { fail++; }
  }
  // Clear selection and re-render.
  state.selectedGroups.clear();
  clearPreview();
  if (state.scanResult) renderResults(state.scanResult);
  showToast('Batch complete: ' + ok + ' deleted, ' + fail + ' failed.', fail > 0 ? 'error' : 'success');
}

/**
 * Download a mismatch diagnostic report for a perceptual match group.
 */
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
