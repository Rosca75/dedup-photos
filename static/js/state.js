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

  /** Whether the settings pane is currently visible. */
  settingsOpen: false,

  /** Set of group IDs that are currently expanded in the main area. */
  expandedGroups: new Set()
};
