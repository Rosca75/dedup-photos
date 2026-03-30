// state.js — Single source of truth for all shared application state.
// Other modules import and read/write this object directly.

/** Global application state shared across all modules. */
export const state = {
  /** Latest scan result data from the API (null before first scan). */
  scanResult: null,

  /** Interval timer ID for polling scan progress (null when not polling). */
  pollTimer: null,

  /** Currently selected folder path for sidebar filtering (null = show all). */
  selectedFolder: null,

  /** Set of group IDs selected for batch actions (checkboxes). */
  selectedGroups: new Set(),

  /** Image object currently shown in preview panel, or null. */
  previewImage: null,

  /** Set of file paths marked for deletion (pending confirmation). */
  pendingDeletions: new Set(),

  /** Set of file paths promoted to "Original" by user (one-click promotion). */
  promotedImages: new Set(),

  /** Current sort column and direction for the results table. */
  sortColumn: null,
  sortDirection: 'asc',

  /** Currently selected row path (for highlight in table). */
  selectedRowPath: null,

  /** Set of group IDs that are currently collapsed in the table. */
  collapsedGroups: new Set(),

  /** Filter state for dynamic filtering (no re-scan). */
  filters: {
    filename: '',
    maxDiff: 100,
    extensions: new Set(),
    minFileSize: 0,
    maxFileSize: 0,
    minGroupSize: 0,
    maxGroupSize: 0
  }
};
