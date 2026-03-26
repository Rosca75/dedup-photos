// actions.js — File actions: delete, mismatch report, batch operations.

import { state } from './state.js';
import { apiDelete, apiMismatchReport } from './api.js';
import { showToast, showConfirm } from './components.js';
import { renderResults } from './table.js';
import { clearPreview } from './preview.js';
import { pushAction } from './history.js';

/** Wire up global action handlers and batch action buttons. */
export function initActions() {
  window.deleteFile = deleteFile;
  window.reportMismatch = reportMismatch;
  document.getElementById('batch-keep-best').addEventListener('click', batchKeepBest);
  document.getElementById('batch-delete-all').addEventListener('click', batchDeleteAll);
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

/** Prompt the user, then delete a file by path. */
function deleteFile(path) {
  showConfirm('Delete ' + path + '?', () => {
    apiDelete(path)
      .then(data => {
        if (data.success) {
          showToast('Deleted: ' + path, 'success');
          // Push to undo stack for undo/redo support.
          pushAction({ type: 'delete', path, trashPath: data.trash_path || '', timestamp: Date.now() });
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
  const groupCount = state.selectedGroups.size;
  showConfirm('Delete ' + paths.length + ' file(s) from ' + groupCount + ' group(s), keeping only the best?', () => {
    executeBatchDelete(paths);
  });
}

/** Batch action: delete ALL images in each selected group. */
function batchDeleteAll() {
  const paths = collectPathsForBatch(false);
  if (paths.length === 0) { showToast('No files to delete.', 'error'); return; }
  const groupCount = state.selectedGroups.size;
  showConfirm('Delete ALL ' + paths.length + ' file(s) from ' + groupCount + ' group(s)?', () => {
    executeBatchDelete(paths);
  });
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

/** Execute sequential deletes for a list of paths. */
async function executeBatchDelete(paths) {
  let ok = 0, fail = 0;
  for (const path of paths) {
    try {
      const data = await apiDelete(path);
      if (data.success) {
        ok++;
        pushAction({ type: 'delete', path, trashPath: data.trash_path || '', timestamp: Date.now() });
        removeImageFromState(path);
      } else { fail++; }
    } catch (e) { fail++; }
  }
  state.selectedGroups.clear();
  clearPreview();
  if (state.scanResult) renderResults(state.scanResult);
  showToast('Batch: ' + ok + ' deleted, ' + fail + ' failed.', fail > 0 ? 'error' : 'success');
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
