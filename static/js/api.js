// api.js — All fetch() calls to the backend API.
// This is the ONLY file that calls fetch(). Other modules use these functions.

/**
 * Start a scan with the given settings object.
 * Returns the JSON response (may be immediate "complete" or "scanning").
 */
export async function apiScan(settings) {
  const resp = await fetch("/api/scan", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(settings)
  });
  return resp.json();
}

/**
 * Poll for current scan results / progress.
 * Returns the full results JSON including status, progress, stats, groups.
 */
export async function apiResults() {
  const resp = await fetch("/api/results");
  return resp.json();
}

/**
 * Cancel the currently running scan.
 */
export async function apiCancel() {
  const resp = await fetch("/api/cancel", { method: "POST" });
  return resp.json();
}

/**
 * Soft-delete a file by path (renames to .deleted).
 * Returns { success: true } or { success: false, error: "..." }.
 */
export async function apiDelete(path) {
  const resp = await fetch("/api/delete", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ path })
  });
  return resp.json();
}

/**
 * Browse directories starting from the given path.
 * Returns { current, parent, entries: [{name, path}] }.
 */
export async function apiBrowse(path) {
  const resp = await fetch("/api/browse", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ path })
  });
  return resp.json();
}

/**
 * Download a mismatch diagnostic report for a group.
 * Returns a Blob (JSON file) for the user to download.
 */
export async function apiMismatchReport(groupId) {
  const resp = await fetch("/api/report-mismatch", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ group_id: groupId })
  });
  if (!resp.ok) throw new Error("Server returned " + resp.status);
  return resp.blob();
}
