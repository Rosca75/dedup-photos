// actions.js — File actions: delete, mismatch report, batch operations.

import { apiDelete, apiMismatchReport } from './api.js';
import { showToast, showConfirm } from './components.js';

/**
 * Wire up global action handlers (exposed on window for inline calls).
 */
export function initActions() {
  // Expose on window so dynamically-created buttons can call them.
  window.deleteFile = deleteFile;
  window.reportMismatch = reportMismatch;
}

/**
 * Prompt the user, then soft-delete a file by path.
 * Removes the image card from the DOM and cleans up empty groups.
 */
function deleteFile(path) {
  showConfirm("Permanently delete " + path + "?", () => {
    apiDelete(path)
      .then(data => {
        if (data.success) {
          showToast("Deleted: " + path, "success");
          removeCardFromDOM(path);
        } else {
          showToast("Delete failed: " + (data.error || "Unknown error"), "error");
        }
      })
      .catch(err => showToast("Delete failed: " + err.message, "error"));
  });
}

/**
 * Remove a deleted image's card from the DOM.
 * Also removes the parent group if only 0-1 images remain.
 */
function removeCardFromDOM(path) {
  const cards = document.querySelectorAll(".image-card");
  for (const card of cards) {
    if (card.getAttribute("data-path") === path) {
      const group = card.closest(".group-card");
      card.parentNode.removeChild(card);
      // Remove the group if fewer than 2 images remain.
      if (group && group.querySelectorAll(".image-card").length <= 1) {
        group.parentNode.removeChild(group);
      }
      break;
    }
  }
  // Show empty state if all groups are resolved.
  if (document.querySelectorAll(".group-card").length === 0) {
    const empty = document.getElementById("empty-state");
    empty.style.display = "block";
    empty.querySelector("p").textContent = "All duplicates resolved!";
  }
}

/**
 * Download a mismatch diagnostic report for a perceptual match group.
 */
function reportMismatch(groupId) {
  apiMismatchReport(groupId)
    .then(blob => {
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = "mismatch_report_" + groupId.substring(0, 8) + ".json";
      a.click();
      URL.revokeObjectURL(url);
      showToast("Mismatch report downloaded.", "success");
    })
    .catch(err => showToast("Report failed: " + err.message, "error"));
}
