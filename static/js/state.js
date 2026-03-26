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

  /** Undo stack — array of {type, path, trashPath, timestamp} objects. */
  undoStack: [],

  /** Redo stack — array of {type, path, trashPath, timestamp} objects. */
  redoStack: [],

  /** Current sort column and direction for the results table. */
  sortColumn: null,
  sortDirection: 'asc',

  /** Currently selected row path (for highlight in table). */
  selectedRowPath: null,

  /** Filter state for dynamic filtering (no re-scan). */
  filters: {
    filename: '',
    maxDiff: 100,
    extensions: new Set()
  }
};
