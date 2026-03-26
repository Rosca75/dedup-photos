// preview.js — Left panel: image preview + metadata display.

import { state } from './state.js';
import { formatBytes, formatDate, formatGPS, qualityColor } from './helpers.js';

/** Initialize the preview panel with a placeholder. */
export function initPreview() {
  clearPreview();
}

/** Show a full preview of the given image object. */
export function showPreview(img) {
  state.previewImage = img;
  const container = document.getElementById('preview-area');
  if (!container) return;
  container.innerHTML = '';

  // Thumbnail.
  const thumbImg = document.createElement('img');
  thumbImg.src = '/api/thumbnail?path=' + encodeURIComponent(img.path || '');
  thumbImg.alt = img.filename || 'thumbnail';
  thumbImg.className = 'preview-thumb';
  thumbImg.onerror = function () { this.style.display = 'none'; };
  container.appendChild(thumbImg);

  // Info section.
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
    ['Resolution', (img.width && img.height) ? img.width + 'x' + img.height : '--'],
    ['Date', formatDate(img.date_taken)],
    ['Camera', img.camera || '--'],
    ['GPS', formatGPS(img.gps_lat, img.gps_lon)],
    ['Quality', img.quality_score != null ? img.quality_score + '/100' : '--'],
    ['Blockiness', img.blockiness != null ? Number(img.blockiness).toFixed(2) : '--'],
    ['Blurring', img.blurring != null ? Number(img.blurring).toFixed(2) : '--']
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

  // Quality bar.
  if (img.quality_score != null) {
    info.appendChild(buildQualityBar(img.quality_score));
  }

  // Original/Duplicate badge.
  const badge = document.createElement('span');
  badge.className = 'badge ' + (img.is_best ? 'badge-keep' : 'badge-delete');
  badge.textContent = img.is_best ? 'Original' : 'Duplicate';
  badge.style.marginTop = '8px';
  badge.style.display = 'inline-block';
  info.appendChild(badge);

  container.appendChild(info);
}

/** Build a quality bar widget. */
function buildQualityBar(score) {
  const qs = Number(score);
  const section = document.createElement('div');
  section.className = 'quality-section';
  section.style.marginTop = '6px';
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

/** Clear the preview panel and show a placeholder. */
export function clearPreview() {
  state.previewImage = null;
  const container = document.getElementById('preview-area');
  if (container) {
    container.innerHTML = '<div class="preview-empty"><i data-feather="image"></i><p>Click a row to preview</p></div>';
    if (typeof feather !== 'undefined') feather.replace();
  }
}
