// components.js — Reusable UI components: toast notifications and confirm dialogs.

/**
 * Show a toast notification at the bottom-right of the screen.
 * Auto-removes after 3 seconds. Type is "success" or "error".
 */
export function showToast(message, type) {
  const container = document.getElementById("toast-container");
  const el = document.createElement("div");
  el.className = "toast toast-" + (type || "success");
  el.textContent = message;
  container.appendChild(el);
  // Auto-remove after 3s with a fade-out animation.
  setTimeout(() => {
    el.classList.add("removing");
    setTimeout(() => { if (el.parentNode) el.parentNode.removeChild(el); }, 220);
  }, 3000);
}

/**
 * Show a confirmation dialog with Delete/Cancel buttons.
 * Calls onYes() if the user confirms.
 */
export function showConfirm(message, onYes) {
  const overlay = document.createElement("div");
  overlay.className = "confirm-overlay";

  const box = document.createElement("div");
  box.className = "confirm-box";

  const p = document.createElement("p");
  p.textContent = message;
  box.appendChild(p);

  const row = document.createElement("div");
  row.className = "btn-row";

  const yesBtn = document.createElement("button");
  yesBtn.className = "btn btn-danger";
  yesBtn.textContent = "Delete";

  const noBtn = document.createElement("button");
  noBtn.className = "btn";
  noBtn.style.cssText = "background:var(--border);color:var(--text)";
  noBtn.textContent = "Cancel";

  row.appendChild(yesBtn);
  row.appendChild(noBtn);
  box.appendChild(row);
  overlay.appendChild(box);
  document.body.appendChild(overlay);

  // Close on button click or overlay background click.
  yesBtn.onclick = () => { document.body.removeChild(overlay); onYes(); };
  noBtn.onclick = () => { document.body.removeChild(overlay); };
  overlay.addEventListener("click", (e) => {
    if (e.target === overlay) document.body.removeChild(overlay);
  });
}
