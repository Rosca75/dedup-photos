// settings.js — Right settings pane: toggle visibility and read settings values.

import { state } from './state.js';

/**
 * Wire up the settings toggle button and close button.
 */
export function initSettings() {
  const toggleBtn = document.getElementById("settings-toggle-btn");
  const closeBtn = document.getElementById("settings-close-btn");
  const layout = document.querySelector(".app-layout");

  // Toggle settings pane open/closed.
  toggleBtn.addEventListener("click", () => {
    state.settingsOpen = !state.settingsOpen;
    layout.classList.toggle("settings-open", state.settingsOpen);
  });

  // Close button inside the settings pane.
  if (closeBtn) {
    closeBtn.addEventListener("click", () => {
      state.settingsOpen = false;
      layout.classList.remove("settings-open");
    });
  }
}

/**
 * Read current settings values from the DOM inputs.
 * Returns a plain object suitable for passing to apiScan().
 */
export function getSettings() {
  const algorithm = document.getElementById("setting-algorithm").value;
  const threshold = parseInt(document.getElementById("scan-threshold").value, 10) || 10;
  const extStr = document.getElementById("setting-extensions").value.trim();
  const extensions = extStr ? extStr.split(",").map(s => s.trim()).filter(Boolean) : [];
  const minWidth = parseInt(document.getElementById("setting-min-width").value, 10) || 0;
  const maxHeight = parseInt(document.getElementById("setting-max-height").value, 10) || 0;

  return { algorithm, threshold, extensions, min_width: minWidth, max_height: maxHeight };
}
