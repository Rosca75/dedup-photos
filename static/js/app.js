// app.js — Entry point: imports all modules and initializes the application.
// This is the only file loaded by index.html via <script type="module">.

import { initScan } from './scan.js';
import { initSidebar } from './sidebar.js';
import { initSettings } from './settings.js';
import { initBrowse } from './browse.js';
import { initActions } from './actions.js';

/**
 * Initialize all application modules once the DOM is ready.
 */
document.addEventListener('DOMContentLoaded', () => {
  initScan();       // Wire up scan/cancel buttons and polling.
  initSidebar();    // Prepare sidebar (populated after scan).
  initSettings();   // Wire up settings pane toggle.
  initBrowse();     // Wire up folder browse dialog.
  initActions();    // Expose delete/mismatch handlers.
});
