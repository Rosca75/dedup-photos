// history.js — Undo/redo stack management (max 20 actions).

import { state } from './state.js';
import { apiUndo, apiRedo } from './api.js';
import { showToast } from './components.js';

/** Maximum number of actions to keep in the undo stack. */
const MAX_UNDO = 20;

/** Initialize undo/redo buttons. */
export function initHistory() {
  document.getElementById('undo-btn').addEventListener('click', undo);
  document.getElementById('redo-btn').addEventListener('click', redo);
  updateHistoryButtons();
}

/** Push a new action onto the undo stack. Clears the redo stack. */
export function pushAction(action) {
  state.undoStack.push(action);
  if (state.undoStack.length > MAX_UNDO) state.undoStack.shift();
  state.redoStack = [];
  updateHistoryButtons();
}

/** Undo the last delete action. */
async function undo() {
  if (state.undoStack.length === 0) return;
  try {
    const data = await apiUndo();
    if (data.success) {
      const action = state.undoStack.pop();
      state.redoStack.push(action);
      showToast('Undo: restored ' + (action ? action.path : 'file'), 'success');
    } else {
      showToast('Undo failed: ' + (data.error || 'Unknown'), 'error');
    }
  } catch (err) {
    showToast('Undo failed: ' + err.message, 'error');
  }
  updateHistoryButtons();
}

/** Redo the last undone action. */
async function redo() {
  if (state.redoStack.length === 0) return;
  try {
    const data = await apiRedo();
    if (data.success) {
      const action = state.redoStack.pop();
      state.undoStack.push(action);
      showToast('Redo: deleted ' + (action ? action.path : 'file'), 'success');
    } else {
      showToast('Redo failed: ' + (data.error || 'Unknown'), 'error');
    }
  } catch (err) {
    showToast('Redo failed: ' + err.message, 'error');
  }
  updateHistoryButtons();
}

/** Enable/disable undo/redo buttons based on stack state. */
export function updateHistoryButtons() {
  const undoBtn = document.getElementById('undo-btn');
  const redoBtn = document.getElementById('redo-btn');
  if (undoBtn) undoBtn.disabled = state.undoStack.length === 0;
  if (redoBtn) redoBtn.disabled = state.redoStack.length === 0;
}
