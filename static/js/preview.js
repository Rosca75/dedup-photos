// preview.js — Zone 5: image preview panel in the bottom of the left sidebar.

import { state } from './state.js';
import { formatBytes, formatDate, formatGPS, qualityColor } from './helpers.js';

/**
 * Initialize the preview panel with a placeholder message.
 */
export function initPreview() {
  clearPreview();
}

/**
 * Show a full preview of the given image object in Zone 5.
 * Displays thumbnail, metadata, quality bar, KEEP/DELETE badge, and delete button.
 */
export function showPreview(img) {
  state.previewImage = img;
  const container = document.getElementById('preview-area');
  if (!container) return;
  container.innerHTML = '';

  // Thumbnail image.
  const thumbImg = document.createElement('img');
  thumbImg.src = '/api/thumbnail?path=' + encodeURIComponent(img.path || '');
  thumbImg.alt = img.filename || 'thumbnail';
  thumbImg.className = 'preview-thumb';
  thumbImg.onerror = function () { this.style.display = 'none'; };
  container.appendChild(thumbImg);

  // Info section with metadata.
  const info = document.createElement('div');
  info.className = 'preview-info';

  // Filename.
  const name = document.createElement('div');
  name.className = 'preview-filename';
  name.textContent = img.filename || 'Unknown';
  info.appendChild(name);

  // Metadata fields.
  const fields = [
    ['Size', formatBytes(img.size)],
    ['Resolution', (img.width && img.height) ? (img.width + 'x' + img.height) : '--'],
    ['Date', formatDate(img.date_taken)],
    ['Camera', img.camera || '--'],
    ['GPS', formatGPS(img.gps_lat, img.gps_lon)],
    ['Quality', img.quality_score != null ? (img.quality_score + '/100') : '--']
  ];
  for (const [label, value] of fields) {
    const item = document.createElement('div');
    item.className = 'preview-meta-item';
    const l = document.createElement('span');
    l.className = 'preview-meta-label';
    l.textContent = label;
    const v = document.createElement('span');
    v.className = 'preview-meta-value';
    v.textContent = value;
    item.appendChild(l);
    item.appendChild(v);
    info.appendChild(item);
  }

  // Quality bar (if available).
  if (img.quality_score != null) {
    const qbar = buildQualityBar(img.quality_score);
    info.appendChild(qbar);
  }

  // KEEP or DELETE badge.
  const badge = document.createElement('span');
  badge.className = 'badge ' + (img.is_best ? 'badge-keep' : 'badge-delete');
  badge.textContent = img.is_best ? 'KEEP' : 'DELETE';
  badge.style.marginTop = '0.5rem';
  badge.style.display = 'inline-block';
  info.appendChild(badge);

  // Delete button.
  const delBtn = document.createElement('button');
  delBtn.className = 'btn btn-danger btn-sm';
  delBtn.textContent = 'Delete this file';
  delBtn.style.marginTop = '0.5rem';
  delBtn.style.width = '100%';
  delBtn.onclick = () => window.deleteFile(img.path);
  info.appendChild(delBtn);

  container.appendChild(info);
}

/** Build a small quality bar widget. */
function buildQualityBar(score) {
  const qs = Number(score);
  const section = document.createElement('div');
  section.className = 'quality-section';
  section.style.marginTop = '0.4rem';
  const track = document.createElement('div');
  track.className = 'quality-track';
  const fill = document.createElement('div');
  fill.className = 'quality-fill';
  fill.style.width = qs + '%';
  fill.style.background = qualityColor(qs);
  track.appendChild(fill);
  section.appendChild(track);
  return section;
}

/**
 * Clear the preview panel and show a placeholder message.
 */
export function clearPreview() {
  state.previewImage = null;
  const container = document.getElementById('preview-area');
  if (container) {
    container.innerHTML = '<div class="preview-empty">Click a row to preview</div>';
  }
}
