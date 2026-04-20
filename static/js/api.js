// api.js — Wails binding wrappers for all Go backend methods.
//
// This is the ONLY file that calls window.go.main.App.*
// All other modules import from here — never call Wails bindings directly.
//
// Wails makes each public method on *App available as:
//   window.go.main.App.MethodName(args...) → Promise
//
// The Go structs are automatically serialised to/from JS objects using
// the json: struct tags defined in server.go.

/** Helper: return the Wails App binding. */
const GoApp = () => window.go.main.App;

/**
 * Start a scan with the given settings object.
 * Matches the ScanRequest struct in server.go.
 * Returns immediately with {status:"scanning"}; poll GetResults() for progress.
 * @param {Object} settings - ScanRequest fields (path, threshold, algorithm, ...)
 */
export function apiScan(settings) {
  return GoApp().StartScan(settings);
}

/**
 * Poll for current scan progress and results.
 * Returns a ScanResult object (status, progress, stats, groups).
 */
export function apiResults() {
  return GoApp().GetResults();
}

/**
 * Lightweight progress-only poll used during an active scan.
 * Returns {status, progress} — no duplicate groups, so serialisation cost
 * stays small regardless of how many groups have been detected so far.
 */
export function apiProgress() {
  return GoApp().GetProgress();
}

/**
 * Cancel the currently running scan.
 */
export function apiCancel() {
  return GoApp().CancelScan();
}

/**
 * Permanently delete a file by path.
 * Returns {success: bool, message/error: string}.
 * @param {string} path - Absolute file path to delete.
 */
export function apiDelete(path) {
  return GoApp().DeleteFile(path);
}

/**
 * Open the native OS folder picker dialog.
 * Returns a Promise<string> with the selected folder path, or empty string if cancelled.
 */
export function apiOpenFolderDialog() {
  return GoApp().OpenFolderDialog();
}

/**
 * Fetch a base64-encoded JPEG thumbnail for the given image path.
 * Returns a Promise<string> — empty string if the thumbnail cannot be made.
 * Usage in JS:
 *   const b64 = await apiGetThumbnail(img.path);
 *   if (b64) imgEl.src = 'data:image/jpeg;base64,' + b64;
 * @param {string} path - Absolute file path.
 */
export function apiGetThumbnail(path) {
  return GoApp().GetThumbnail(path || '');
}

/**
 * Generate a JSON mismatch diagnostic report for a duplicate group.
 * Returns a Promise<string> containing the JSON. Convert to a download:
 *   const blob = new Blob([jsonStr], { type: "application/json" });
 * @param {string} groupId - UUID of the duplicate group.
 */
export function apiMismatchReport(groupId) {
  return GoApp().ReportMismatch(groupId);
}

/**
 * Compute blockiness and blurring scores for a single image (lazy).
 * Called when the user expands a group, so the expensive full-image decode
 * only happens for images actually viewed — not during the scan.
 * Returns a Promise<{blockiness: number, blurring: number}>.
 * @param {string} path - Absolute file path.
 */
export function apiGetImageQualityMetrics(path) {
  return GoApp().GetImageQualityMetrics(path || '');
}
