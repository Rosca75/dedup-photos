// sidebar.js — Left sidebar: folder tree navigation for filtering scan results.

import { state } from './state.js';
import { renderResults } from './table.js';

/**
 * Initialize the sidebar (no-op until scan results arrive).
 */
export function initSidebar() {
  // Sidebar is populated after scan results are rendered.
}

/**
 * Build a folder tree from scan results and render it in the sidebar.
 * Extracts unique parent directories from all duplicate image paths.
 */
export function buildSidebarTree() {
  const container = document.getElementById("sidebar-tree");
  if (!container) return;
  container.innerHTML = "";

  const data = state.scanResult;
  if (!data || !data.groups || data.groups.length === 0) return;

  // Collect folder paths and their duplicate image counts.
  const folderCounts = {};
  for (const group of data.groups) {
    for (const img of (group.images || [])) {
      const dir = parentDir(img.path);
      folderCounts[dir] = (folderCounts[dir] || 0) + 1;
    }
  }

  // "All" option at the top.
  const allItem = createFolderItem("All", null, sumValues(folderCounts));
  container.appendChild(allItem);

  // Sort folders alphabetically and render each.
  const folders = Object.keys(folderCounts).sort();
  for (const folder of folders) {
    const item = createFolderItem(shortenPath(folder), folder, folderCounts[folder]);
    container.appendChild(item);
  }
}

/**
 * Create a clickable folder item element for the sidebar.
 */
function createFolderItem(label, folderPath, count) {
  const item = document.createElement("div");
  item.className = "sidebar-item";
  if (state.selectedFolder === folderPath) item.classList.add("active");

  const nameSpan = document.createElement("span");
  nameSpan.className = "sidebar-name";
  nameSpan.textContent = label;
  nameSpan.title = folderPath || "Show all groups";
  item.appendChild(nameSpan);

  const countSpan = document.createElement("span");
  countSpan.className = "sidebar-count";
  countSpan.textContent = count;
  item.appendChild(countSpan);

  // On click, filter results by this folder.
  item.addEventListener("click", () => {
    state.selectedFolder = folderPath;
    buildSidebarTree(); // Re-render sidebar to update active state.
    if (state.scanResult) renderResults(state.scanResult);
  });

  return item;
}

/** Extract the parent directory from a file path. */
function parentDir(filePath) {
  if (!filePath) return "/";
  const lastSlash = filePath.lastIndexOf("/");
  const lastBack = filePath.lastIndexOf("\\");
  const idx = Math.max(lastSlash, lastBack);
  return idx > 0 ? filePath.substring(0, idx) : "/";
}

/** Shorten a path to just the last 2 segments for display. */
function shortenPath(fullPath) {
  if (!fullPath) return "/";
  const sep = fullPath.includes("\\") ? "\\" : "/";
  const parts = fullPath.split(sep).filter(Boolean);
  if (parts.length <= 2) return fullPath;
  return ".../" + parts.slice(-2).join("/");
}

/** Sum all values in an object. */
function sumValues(obj) {
  return Object.values(obj).reduce((a, b) => a + b, 0);
}
