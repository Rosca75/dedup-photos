// render.js — Central zone: render scan results, group cards, image cards.

import { state } from './state.js';
import { formatBytes, formatDuration, formatDate, formatGPS, qualityColor } from './helpers.js';
import { resetScanUI } from './scan.js';
import { buildSidebarTree } from './sidebar.js';

/**
 * Render full scan results into the main area.
 * Respects state.selectedFolder for sidebar filtering.
 */
export function renderResults(data) {
  resetScanUI();
  state.scanResult = data;

  // Update stats bar.
  const stats = data.stats || {};
  const statsBar = document.getElementById("stats-bar");
  if (stats.total_files != null) {
    statsBar.style.display = "grid";
    document.getElementById("stat-files").textContent = stats.total_files.toLocaleString();
    document.getElementById("stat-groups").textContent = (stats.duplicate_groups || 0).toLocaleString();
    document.getElementById("stat-savings").textContent = formatBytes(stats.wasted_bytes || 0);
    document.getElementById("stat-duration").textContent = formatDuration(stats.duration_ms);
  }

  // Filter groups by selected folder if set.
  let groups = data.groups || [];
  if (state.selectedFolder) {
    groups = groups.filter(g =>
      (g.images || []).some(img => isInFolder(img.path, state.selectedFolder))
    );
  }

  const container = document.getElementById("groups-container");
  container.innerHTML = "";

  if (groups.length === 0) {
    const empty = document.getElementById("empty-state");
    empty.style.display = "block";
    empty.querySelector("p").textContent = data.status === "complete"
      ? "No duplicates found. Your library is clean!" : "No duplicates in this folder.";
    return;
  }

  document.getElementById("empty-state").style.display = "none";

  // Default: first 3 groups expanded, rest collapsed.
  if (state.expandedGroups.size === 0) {
    groups.slice(0, 3).forEach(g => state.expandedGroups.add(g.id));
  }

  for (const group of groups) {
    container.appendChild(buildGroup(group));
  }

  // Build sidebar tree after rendering results.
  buildSidebarTree();
}

/** Check if a file path is inside a given folder. */
function isInFolder(filePath, folder) {
  if (!filePath || !folder) return false;
  const normalized = filePath.replace(/\\/g, "/");
  const normalizedFolder = folder.replace(/\\/g, "/");
  return normalized.startsWith(normalizedFolder + "/") || normalized.startsWith(normalizedFolder + "\\");
}

/**
 * Build a group card element (collapsed or expanded).
 */
function buildGroup(group) {
  const card = document.createElement("div");
  card.className = "group-card";

  const isExact = (group.match_type === "exact");
  const expanded = state.expandedGroups.has(group.id);
  const images = group.images || [];

  // Header row: always visible.
  const header = document.createElement("div");
  header.className = "group-header";

  // Expand/collapse toggle button.
  const toggleBtn = document.createElement("button");
  toggleBtn.className = "group-toggle";
  toggleBtn.textContent = expanded ? "▼" : "▶";
  toggleBtn.addEventListener("click", () => toggleGroup(group.id));
  header.appendChild(toggleBtn);

  const badge = document.createElement("span");
  badge.className = "badge " + (isExact ? "badge-exact" : "badge-perceptual");
  badge.textContent = isExact ? "Exact" : "Perceptual";
  header.appendChild(badge);

  const conf = document.createElement("span");
  conf.className = "confidence";
  conf.textContent = group.confidence != null ? Number(group.confidence).toFixed(1) + "%" : "";
  header.appendChild(conf);

  // Collapsed summary: image count and wasted space.
  const summary = document.createElement("span");
  summary.className = "group-summary";
  const wastedBytes = images.slice(1).reduce((sum, img) => sum + (img.size || 0), 0);
  summary.textContent = images.length + " images · " + formatBytes(wastedBytes) + " wasted";
  header.appendChild(summary);

  // Mismatch report button for perceptual matches.
  if (!isExact && group.id) {
    const mismatchBtn = document.createElement("button");
    mismatchBtn.className = "btn-mismatch";
    mismatchBtn.textContent = "Report mismatch";
    mismatchBtn.addEventListener("click", () => window.reportMismatch(group.id));
    header.appendChild(mismatchBtn);
  }

  card.appendChild(header);

  // Expanded content: image cards grid.
  if (expanded) {
    const imagesWrap = document.createElement("div");
    imagesWrap.className = "group-images";
    for (const img of images) {
      imagesWrap.appendChild(buildImageCard(img));
    }
    card.appendChild(imagesWrap);
  }

  return card;
}

/** Toggle a group's expanded/collapsed state and re-render it. */
function toggleGroup(groupId) {
  if (state.expandedGroups.has(groupId)) {
    state.expandedGroups.delete(groupId);
  } else {
    state.expandedGroups.add(groupId);
  }
  // Re-render all results (simple approach; could optimize to single group).
  if (state.scanResult) renderResults(state.scanResult);
}

/**
 * Build an individual image card with thumbnail, metadata, quality bar, and actions.
 */
function buildImageCard(img) {
  const card = document.createElement("div");
  card.className = "image-card";
  card.setAttribute("data-path", img.path || "");

  // Thumbnail.
  card.appendChild(buildThumbnail(img));
  // Filename and path.
  card.appendChild(makeDiv("filename", img.filename || "Unknown"));
  card.appendChild(makeDiv("filepath", img.path || ""));
  // Metadata grid.
  card.appendChild(buildMetaGrid(img));
  // Quality bar (if score available).
  if (img.quality_score != null) card.appendChild(buildQualityBar(img.quality_score));
  // Action buttons.
  card.appendChild(buildCardActions(img));

  return card;
}

/** Build the thumbnail wrapper with lazy-loaded image. */
function buildThumbnail(img) {
  const wrap = document.createElement("div");
  wrap.className = "thumb-wrap";
  const thumbImg = document.createElement("img");
  thumbImg.src = "/api/thumbnail?path=" + encodeURIComponent(img.path || "");
  thumbImg.alt = img.filename || "thumbnail";
  thumbImg.loading = "lazy";
  thumbImg.onerror = function() { this.style.display = "none"; wrap.textContent = "No preview"; };
  wrap.appendChild(thumbImg);
  return wrap;
}

/** Build the 2-column metadata grid. */
function buildMetaGrid(img) {
  const meta = document.createElement("div");
  meta.className = "meta-grid";
  const fields = [
    ["Resolution", (img.width && img.height) ? (img.width + "x" + img.height) : "--"],
    ["File Size", formatBytes(img.size)],
    ["Date Taken", formatDate(img.date_taken)],
    ["GPS", formatGPS(img.gps_lat, img.gps_lon)],
    ["Camera", img.camera || "--"],
    ["Lens", img.lens || "--"],
    ["ISO", img.iso != null ? String(img.iso) : "--"],
    ["Quality", img.quality_score != null ? (img.quality_score + "/100") : "--"]
  ];
  for (const [label, value] of fields) {
    const item = document.createElement("div");
    item.className = "meta-item";
    item.appendChild(makeSpan("meta-label", label));
    item.appendChild(makeSpan("meta-value", value));
    meta.appendChild(item);
  }
  return meta;
}

/** Build a quality score bar with color gradient. */
function buildQualityBar(score) {
  const qs = Number(score);
  const section = document.createElement("div");
  section.className = "quality-section";
  section.appendChild(makeSpan("qlabel", "Quality"));

  const track = document.createElement("div");
  track.className = "quality-track";
  const fill = document.createElement("div");
  fill.className = "quality-fill";
  fill.style.width = qs + "%";
  fill.style.background = qualityColor(qs);
  track.appendChild(fill);
  section.appendChild(track);

  const val = makeSpan("qval", String(qs));
  val.style.color = qualityColor(qs);
  section.appendChild(val);
  return section;
}

/** Build the KEEP/DELETE badge and delete button row. */
function buildCardActions(img) {
  const actions = document.createElement("div");
  actions.className = "card-actions";

  const badge = document.createElement("span");
  badge.className = "badge " + (img.is_best ? "badge-keep" : "badge-delete");
  badge.textContent = img.is_best ? "KEEP" : "DELETE";
  actions.appendChild(badge);

  const delBtn = document.createElement("button");
  delBtn.className = "btn btn-danger";
  delBtn.textContent = "Delete";
  delBtn.style.marginLeft = "auto";
  delBtn.onclick = () => window.deleteFile(img.path);
  actions.appendChild(delBtn);
  return actions;
}

/** Helper: create a div with a class and text content. */
function makeDiv(cls, text) {
  const el = document.createElement("div");
  el.className = cls;
  el.textContent = text;
  return el;
}

/** Helper: create a span with a class and text content. */
function makeSpan(cls, text) {
  const el = document.createElement("span");
  el.className = cls;
  el.textContent = text;
  return el;
}
