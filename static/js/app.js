// app.js — Entry point: imports all modules and initializes the application.
// This is the only file loaded by index.html via <script type="module">.

import { initScan } from './scan.js';
import { initSidebar } from './sidebar.js';
import { initBrowse } from './browse.js';
import { initActions } from './actions.js';
import { initPreview } from './preview.js';
import { initHistory } from './history.js';
import { initFilters } from './filters.js';

/** Initialize all application modules once the DOM is ready. */
document.addEventListener('DOMContentLoaded', () => {
  initScan();       // Wire up scan/cancel/refresh buttons and polling.
  initSidebar();    // Prepare sidebar folder tree (populated after scan).
  initBrowse();     // Wire up folder browse dialog.
  initActions();    // Expose delete/mismatch handlers + batch actions.
  initPreview();    // Initialize image preview panel.
  initHistory();    // Wire up undo/redo buttons.
  initFilters();    // Wire up dynamic filter controls.

  // Render Feather icons (replaces <i data-feather="..."> with SVGs).
  if (typeof feather !== 'undefined') feather.replace();
});
