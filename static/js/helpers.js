// helpers.js — Pure utility functions for formatting and display.
// No DOM access, no side effects. All functions are stateless.

/**
 * Format a byte count into a human-readable string (e.g. "4.3 MB").
 * Returns "--" for null/NaN input.
 */
export function formatBytes(bytes) {
  if (bytes == null || isNaN(bytes)) return "--";
  if (bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = Math.floor(Math.log(bytes) / Math.log(1024));
  if (i >= units.length) i = units.length - 1;
  return (bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1) + " " + units[i];
}

/**
 * Format a duration in milliseconds to a readable string (e.g. "2.3s", "1m 45s").
 */
export function formatDuration(ms) {
  if (ms == null || isNaN(ms)) return "--";
  if (ms < 1000) return ms + "ms";
  let s = ms / 1000;
  if (s < 60) return s.toFixed(1) + "s";
  const m = Math.floor(s / 60);
  s = Math.floor(s % 60);
  return m + "m " + s + "s";
}

/**
 * Format an ISO date string into a locale-friendly display string.
 */
export function formatDate(iso) {
  if (!iso) return "--";
  try {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" }) +
      " " + d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  } catch (e) { return iso; }
}

/**
 * Format GPS coordinates as "lat, lon" with 4 decimal places.
 * Returns "--" if coordinates are missing or both zero.
 */
export function formatGPS(lat, lon) {
  if (lat == null || lon == null || (lat === 0 && lon === 0)) return "--";
  return Number(lat).toFixed(4) + ", " + Number(lon).toFixed(4);
}

/**
 * Escape HTML special characters to prevent XSS.
 */
export function escapeHtml(s) {
  if (!s) return "";
  return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;")
    .replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

/**
 * Return an RGB color string for a quality score (0–100).
 * Low scores are red, high scores are green.
 */
export function qualityColor(score) {
  let r, g;
  if (score <= 50) { r = 255; g = Math.round(score * 5.1); }
  else { r = Math.round((100 - score) * 5.1); g = 255; }
  return "rgb(" + r + "," + g + ",60)";
}
