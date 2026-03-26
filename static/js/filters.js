// filters.js — Dynamic filtering of scan results (no re-scan needed).
// Filters: filename search, % diff slider, extension checkboxes, file size.

import { state } from './state.js';
import { renderResults } from './table.js';

/** Initialize filter controls and wire up event listeners. */
export function initFilters() {
  // Filename search.
  const nameInput = document.getElementById('filter-filename');
  if (nameInput) {
    nameInput.addEventListener('input', () => {
      state.filters.filename = nameInput.value.trim().toLowerCase();
      refilter();
    });
  }

  // % diff slider.
  const diffSlider = document.getElementById('filter-diff');
  const diffLabel = document.getElementById('filter-diff-value');
  if (diffSlider) {
    diffSlider.addEventListener('input', () => {
      state.filters.maxDiff = parseInt(diffSlider.value, 10);
      if (diffLabel) diffLabel.textContent = diffSlider.value + '%';
      refilter();
    });
  }

  // Extension checkboxes.
  const extGrid = document.getElementById('filter-ext-grid');
  if (extGrid) {
    extGrid.querySelectorAll('input[type="checkbox"]').forEach(cb => {
      if (cb.checked) state.filters.extensions.add(cb.value.toLowerCase());
      cb.addEventListener('change', () => {
        if (cb.checked) state.filters.extensions.add(cb.value.toLowerCase());
        else state.filters.extensions.delete(cb.value.toLowerCase());
        refilter();
      });
    });
  }

  // Min file size filter (KB).
  const minSizeInput = document.getElementById('filter-min-filesize');
  if (minSizeInput) {
    minSizeInput.addEventListener('input', () => {
      state.filters.minFileSize = (parseInt(minSizeInput.value, 10) || 0) * 1024;
      refilter();
    });
  }

  // Max file size filter (MB).
  const maxSizeInput = document.getElementById('filter-max-filesize');
  if (maxSizeInput) {
    maxSizeInput.addEventListener('input', () => {
      state.filters.maxFileSize = (parseInt(maxSizeInput.value, 10) || 0) * 1024 * 1024;
      refilter();
    });
  }
}

/** Re-render results using current filters. */
function refilter() {
  if (state.scanResult) renderResults(state.scanResult);
}

/**
 * Apply all active filters to a list of groups.
 * Returns a new array with only matching groups (and within each group,
 * only images that match the filename + extension + file size filters).
 */
export function applyFilters(groups) {
  if (!groups) return [];

  return groups
    .map(group => {
      // Filter by % diff (group-level).
      const diff = group.confidence != null ? (100 - Number(group.confidence)) : 0;
      if (diff > state.filters.maxDiff) return null;

      // Filter images within the group.
      let images = group.images || [];
      if (state.filters.filename) {
        images = images.filter(img =>
          (img.filename || '').toLowerCase().includes(state.filters.filename)
        );
      }
      if (state.filters.extensions.size > 0) {
        images = images.filter(img => {
          const ext = extractExt(img.filename).toLowerCase();
          return state.filters.extensions.has(ext);
        });
      }
      // File size filters.
      if (state.filters.minFileSize > 0) {
        images = images.filter(img => (img.size || 0) >= state.filters.minFileSize);
      }
      if (state.filters.maxFileSize > 0) {
        images = images.filter(img => (img.size || 0) <= state.filters.maxFileSize);
      }

      // Need at least 2 images for a valid duplicate group.
      if (images.length < 2) return null;
      return { ...group, images };
    })
    .filter(Boolean);
}

/** Extract file extension without the dot (e.g. "jpg"). */
function extractExt(filename) {
  if (!filename) return '';
  const dot = filename.lastIndexOf('.');
  return dot >= 0 ? filename.substring(dot + 1) : '';
}
