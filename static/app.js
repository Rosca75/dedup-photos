(function() {
  "use strict";

  var pollTimer = null;

  // =========================================================================
  // Helpers
  // =========================================================================

  function formatBytes(bytes) {
    if (bytes == null || isNaN(bytes)) return "--";
    if (bytes === 0) return "0 B";
    var units = ["B", "KB", "MB", "GB", "TB"];
    var i = Math.floor(Math.log(bytes) / Math.log(1024));
    if (i >= units.length) i = units.length - 1;
    return (bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1) + " " + units[i];
  }

  function formatDuration(ms) {
    if (ms == null || isNaN(ms)) return "--";
    if (ms < 1000) return ms + "ms";
    var s = ms / 1000;
    if (s < 60) return s.toFixed(1) + "s";
    var m = Math.floor(s / 60);
    s = Math.floor(s % 60);
    return m + "m " + s + "s";
  }

  function formatDate(iso) {
    if (!iso) return "--";
    try {
      var d = new Date(iso);
      if (isNaN(d.getTime())) return iso;
      return d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" }) +
        " " + d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
    } catch (e) { return iso; }
  }

  function formatGPS(lat, lon) {
    if (lat == null || lon == null || (lat === 0 && lon === 0)) return "--";
    return Number(lat).toFixed(4) + ", " + Number(lon).toFixed(4);
  }

  function escapeHtml(s) {
    if (!s) return "";
    return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }

  function qualityColor(score) {
    var r, g;
    if (score <= 50) { r = 255; g = Math.round(score * 5.1); }
    else { r = Math.round((100 - score) * 5.1); g = 255; }
    return "rgb(" + r + "," + g + ",60)";
  }

  // =========================================================================
  // Toast System
  // =========================================================================

  function showToast(message, type) {
    var container = document.getElementById("toast-container");
    var el = document.createElement("div");
    el.className = "toast toast-" + (type || "success");
    el.textContent = message;
    container.appendChild(el);
    setTimeout(function() {
      el.classList.add("removing");
      setTimeout(function() { if (el.parentNode) el.parentNode.removeChild(el); }, 220);
    }, 3000);
  }

  // =========================================================================
  // Confirm Dialog
  // =========================================================================

  function showConfirm(message, onYes) {
    var overlay = document.createElement("div");
    overlay.className = "confirm-overlay";
    var box = document.createElement("div");
    box.className = "confirm-box";
    var p = document.createElement("p");
    p.textContent = message;
    box.appendChild(p);
    var row = document.createElement("div");
    row.className = "btn-row";
    var yesBtn = document.createElement("button");
    yesBtn.className = "btn btn-danger";
    yesBtn.textContent = "Delete";
    var noBtn = document.createElement("button");
    noBtn.className = "btn";
    noBtn.style.cssText = "background:var(--border);color:var(--text)";
    noBtn.textContent = "Cancel";
    row.appendChild(yesBtn);
    row.appendChild(noBtn);
    box.appendChild(row);
    overlay.appendChild(box);
    document.body.appendChild(overlay);
    yesBtn.onclick = function() { document.body.removeChild(overlay); onYes(); };
    noBtn.onclick = function() { document.body.removeChild(overlay); };
    overlay.addEventListener("click", function(e) {
      if (e.target === overlay) document.body.removeChild(overlay);
    });
  }

  // =========================================================================
  // Settings Toggle
  // =========================================================================

  document.getElementById("settings-toggle-btn").addEventListener("click", function() {
    var panel = document.getElementById("settings-panel");
    panel.classList.toggle("active");
  });

  // =========================================================================
  // Browse Dialog
  // =========================================================================

  document.getElementById("browse-btn").addEventListener("click", function() {
    openBrowse("");
  });

  function openBrowse(startPath) {
    fetch("/api/browse", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path: startPath })
    })
    .then(function(r) { return r.json(); })
    .then(function(data) { showBrowseDialog(data); })
    .catch(function(err) { showToast("Browse failed: " + err.message, "error"); });
  }

  function showBrowseDialog(data) {
    // Remove existing dialog if any.
    var existing = document.querySelector(".browse-overlay");
    if (existing) existing.parentNode.removeChild(existing);

    var overlay = document.createElement("div");
    overlay.className = "browse-overlay";

    var box = document.createElement("div");
    box.className = "browse-box";

    var h3 = document.createElement("h3");
    h3.textContent = "Select Folder";
    box.appendChild(h3);

    var cur = document.createElement("div");
    cur.className = "browse-current";
    cur.textContent = data.current || "/";
    box.appendChild(cur);

    var list = document.createElement("div");
    list.className = "browse-list";

    // Parent directory entry.
    if (data.parent) {
      var parentItem = document.createElement("div");
      parentItem.className = "browse-item";
      parentItem.textContent = ".. (parent)";
      parentItem.addEventListener("click", function() {
        openBrowse(data.parent);
      });
      list.appendChild(parentItem);
    }

    // Directory entries.
    var entries = data.entries || [];
    for (var i = 0; i < entries.length; i++) {
      (function(entry) {
        var item = document.createElement("div");
        item.className = "browse-item";
        item.textContent = entry.name;
        item.addEventListener("click", function() {
          openBrowse(entry.path);
        });
        list.appendChild(item);
      })(entries[i]);
    }

    if (entries.length === 0 && !data.parent) {
      var empty = document.createElement("div");
      empty.className = "browse-item";
      empty.textContent = "(no subdirectories)";
      empty.style.color = "var(--muted)";
      list.appendChild(empty);
    }

    box.appendChild(list);

    var actions = document.createElement("div");
    actions.className = "browse-actions";

    var selectBtn = document.createElement("button");
    selectBtn.className = "btn btn-primary";
    selectBtn.textContent = "Select This Folder";
    selectBtn.addEventListener("click", function() {
      document.getElementById("scan-path").value = data.current;
      document.body.removeChild(overlay);
    });

    var cancelBtn = document.createElement("button");
    cancelBtn.className = "btn";
    cancelBtn.style.cssText = "background:var(--border);color:var(--text)";
    cancelBtn.textContent = "Cancel";
    cancelBtn.addEventListener("click", function() {
      document.body.removeChild(overlay);
    });

    actions.appendChild(selectBtn);
    actions.appendChild(cancelBtn);
    box.appendChild(actions);
    overlay.appendChild(box);

    overlay.addEventListener("click", function(e) {
      if (e.target === overlay) document.body.removeChild(overlay);
    });

    document.body.appendChild(overlay);
  }

  // =========================================================================
  // Scan
  // =========================================================================

  document.getElementById("scan-btn").addEventListener("click", startScan);

  function startScan() {
    var pathInput = document.getElementById("scan-path");
    var thresholdInput = document.getElementById("scan-threshold");
    var path = pathInput.value.trim();
    if (!path) {
      showToast("Please enter a folder path.", "error");
      pathInput.focus();
      return;
    }
    var threshold = parseInt(thresholdInput.value, 10);
    if (isNaN(threshold) || threshold < 0) threshold = 10;

    // Gather settings.
    var algorithm = document.getElementById("setting-algorithm").value;
    var extStr = document.getElementById("setting-extensions").value.trim();
    var extensions = extStr ? extStr.split(",").map(function(s) { return s.trim(); }).filter(Boolean) : [];
    var minWidth = parseInt(document.getElementById("setting-min-width").value, 10) || 0;
    var maxHeight = parseInt(document.getElementById("setting-max-height").value, 10) || 0;

    var scanBtn = document.getElementById("scan-btn");
    var cancelBtn = document.getElementById("cancel-btn");
    scanBtn.disabled = true;
    scanBtn.textContent = "Scanning...";
    cancelBtn.style.display = "inline-block";

    document.getElementById("groups-container").innerHTML = "";
    document.getElementById("empty-state").style.display = "none";
    document.getElementById("stats-bar").style.display = "none";
    showProgress(true);
    updateProgress("Starting...", 0, 0);

    fetch("/api/scan", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        path: path,
        threshold: threshold,
        algorithm: algorithm,
        extensions: extensions,
        min_width: minWidth,
        max_height: maxHeight
      })
    })
    .then(function(r) { return r.json(); })
    .then(function(data) {
      if (data.status === "complete") { stopPolling(); renderResults(data); }
      else { startPolling(); }
    })
    .catch(function(err) {
      showToast("Scan request failed: " + err.message, "error");
      resetScanUI();
    });
  }

  // =========================================================================
  // Cancel Scan
  // =========================================================================

  document.getElementById("cancel-btn").addEventListener("click", function() {
    fetch("/api/cancel", { method: "POST" })
    .then(function(r) { return r.json(); })
    .then(function() {
      showToast("Scan cancelled.", "error");
      stopPolling();
      resetScanUI();
    })
    .catch(function(err) { showToast("Cancel failed: " + err.message, "error"); });
  });

  // =========================================================================
  // Polling
  // =========================================================================

  function startPolling() { stopPolling(); pollTimer = setInterval(loadResults, 500); }
  function stopPolling() { if (pollTimer) { clearInterval(pollTimer); pollTimer = null; } }

  function loadResults() {
    fetch("/api/results")
    .then(function(r) { return r.json(); })
    .then(function(data) {
      if (data.progress) {
        updateProgress(
          data.progress.phase || "Scanning...",
          data.progress.current || 0,
          data.progress.total || 0
        );
      }
      if (data.status === "complete" || data.status === "idle" || data.status === "cancelled") {
        stopPolling();
        renderResults(data);
      }
    })
    .catch(function(err) {
      stopPolling();
      showToast("Failed to fetch results: " + err.message, "error");
      resetScanUI();
    });
  }

  // =========================================================================
  // Delete
  // =========================================================================

  window.deleteFile = function(path) {
    showConfirm("Permanently delete " + path + "?", function() {
      fetch("/api/delete", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path: path })
      })
      .then(function(r) { return r.json(); })
      .then(function(data) {
        if (data.success) {
          showToast("Deleted: " + path, "success");
          var cards = document.querySelectorAll(".image-card");
          for (var i = 0; i < cards.length; i++) {
            if (cards[i].getAttribute("data-path") === path) {
              var group = cards[i].closest(".group-card");
              cards[i].parentNode.removeChild(cards[i]);
              if (group) {
                var remaining = group.querySelectorAll(".image-card");
                if (remaining.length <= 1) group.parentNode.removeChild(group);
              }
              break;
            }
          }
          if (document.querySelectorAll(".group-card").length === 0) {
            document.getElementById("empty-state").style.display = "block";
            document.getElementById("empty-state").querySelector("p").textContent = "All duplicates resolved!";
          }
        } else {
          showToast("Delete failed: " + (data.error || "Unknown error"), "error");
        }
      })
      .catch(function(err) { showToast("Delete failed: " + err.message, "error"); });
    });
  };

  // =========================================================================
  // Mismatch Report
  // =========================================================================

  window.reportMismatch = function(groupId) {
    fetch("/api/report-mismatch", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ group_id: groupId })
    })
    .then(function(r) {
      if (!r.ok) throw new Error("Server returned " + r.status);
      return r.blob();
    })
    .then(function(blob) {
      var url = URL.createObjectURL(blob);
      var a = document.createElement("a");
      a.href = url;
      a.download = "mismatch_report_" + groupId.substring(0, 8) + ".json";
      a.click();
      URL.revokeObjectURL(url);
      showToast("Mismatch report downloaded.", "success");
    })
    .catch(function(err) { showToast("Report failed: " + err.message, "error"); });
  };

  // =========================================================================
  // UI Updates
  // =========================================================================

  function resetScanUI() {
    var scanBtn = document.getElementById("scan-btn");
    scanBtn.disabled = false;
    scanBtn.textContent = "Scan";
    document.getElementById("cancel-btn").style.display = "none";
    showProgress(false);
  }

  function showProgress(show) {
    var el = document.getElementById("progress-section");
    if (show) el.classList.add("active"); else el.classList.remove("active");
  }

  function updateProgress(phase, current, total) {
    document.getElementById("progress-phase").textContent = phase;
    document.getElementById("progress-counts").textContent = current + " / " + total;
    var pct = (total > 0) ? Math.round(current / total * 100) : 0;
    document.getElementById("progress-fill").style.width = pct + "%";
  }

  function renderResults(data) {
    resetScanUI();

    var stats = data.stats || {};
    var statsBar = document.getElementById("stats-bar");
    if (stats.total_files != null) {
      statsBar.style.display = "grid";
      document.getElementById("stat-files").textContent = stats.total_files.toLocaleString();
      document.getElementById("stat-groups").textContent = (stats.duplicate_groups || 0).toLocaleString();
      document.getElementById("stat-savings").textContent = formatBytes(stats.wasted_bytes || 0);
      document.getElementById("stat-duration").textContent = formatDuration(stats.duration_ms);
    }

    var groups = data.groups || [];
    var container = document.getElementById("groups-container");
    container.innerHTML = "";

    if (groups.length === 0) {
      var empty = document.getElementById("empty-state");
      empty.style.display = "block";
      if (data.status === "complete") {
        empty.querySelector("p").textContent = "No duplicates found. Your library is clean!";
      }
      return;
    }

    document.getElementById("empty-state").style.display = "none";
    for (var g = 0; g < groups.length; g++) {
      container.appendChild(buildGroup(groups[g]));
    }
  }

  // =========================================================================
  // Build Group Card
  // =========================================================================

  function buildGroup(group) {
    var card = document.createElement("div");
    card.className = "group-card";

    var isExact = (group.match_type === "exact");
    var badgeClass = isExact ? "badge-exact" : "badge-perceptual";
    var badgeText = isExact ? "Exact" : "Perceptual";

    var header = document.createElement("div");
    header.className = "group-header";

    var title = document.createElement("span");
    title.className = "group-title";
    title.textContent = "Group";
    header.appendChild(title);

    var badge = document.createElement("span");
    badge.className = "badge " + badgeClass;
    badge.textContent = badgeText;
    header.appendChild(badge);

    var conf = document.createElement("span");
    conf.className = "confidence";
    conf.textContent = group.confidence != null ? Number(group.confidence).toFixed(1) + "% match" : "";
    header.appendChild(conf);

    // Mismatch report button (for perceptual matches).
    if (!isExact && group.id) {
      var mismatchBtn = document.createElement("button");
      mismatchBtn.className = "btn-mismatch";
      mismatchBtn.textContent = "Report mismatch";
      (function(gid) {
        mismatchBtn.addEventListener("click", function() { window.reportMismatch(gid); });
      })(group.id);
      header.appendChild(mismatchBtn);
    }

    card.appendChild(header);

    var imagesWrap = document.createElement("div");
    imagesWrap.className = "group-images";
    var images = group.images || [];
    for (var i = 0; i < images.length; i++) {
      imagesWrap.appendChild(buildImageCard(images[i]));
    }
    card.appendChild(imagesWrap);
    return card;
  }

  // =========================================================================
  // Build Image Card
  // =========================================================================

  function buildImageCard(img) {
    var card = document.createElement("div");
    card.className = "image-card";
    card.setAttribute("data-path", img.path || "");

    // Thumbnail.
    var thumbWrap = document.createElement("div");
    thumbWrap.className = "thumb-wrap";
    var thumbImg = document.createElement("img");
    thumbImg.src = "/api/thumbnail?path=" + encodeURIComponent(img.path || "");
    thumbImg.alt = img.filename || "thumbnail";
    thumbImg.loading = "lazy";
    thumbImg.onerror = function() { this.style.display = "none"; thumbWrap.textContent = "No preview"; };
    thumbWrap.appendChild(thumbImg);
    card.appendChild(thumbWrap);

    // Filename.
    var fnEl = document.createElement("div");
    fnEl.className = "filename";
    fnEl.textContent = img.filename || "Unknown";
    card.appendChild(fnEl);

    // Path.
    var fpEl = document.createElement("div");
    fpEl.className = "filepath";
    fpEl.textContent = img.path || "";
    card.appendChild(fpEl);

    // Metadata grid.
    var meta = document.createElement("div");
    meta.className = "meta-grid";
    var fields = [
      ["Resolution", (img.width && img.height) ? (img.width + "x" + img.height) : "--"],
      ["File Size", formatBytes(img.size)],
      ["Date Taken", formatDate(img.date_taken)],
      ["GPS", formatGPS(img.gps_lat, img.gps_lon)],
      ["Camera", img.camera || "--"],
      ["Lens", img.lens || "--"],
      ["ISO", img.iso != null ? String(img.iso) : "--"],
      ["Quality", img.quality_score != null ? (img.quality_score + "/100") : "--"]
    ];
    for (var f = 0; f < fields.length; f++) {
      var item = document.createElement("div");
      item.className = "meta-item";
      var lbl = document.createElement("span");
      lbl.className = "meta-label";
      lbl.textContent = fields[f][0];
      var val = document.createElement("span");
      val.className = "meta-value";
      val.textContent = fields[f][1];
      item.appendChild(lbl);
      item.appendChild(val);
      meta.appendChild(item);
    }
    card.appendChild(meta);

    // Quality bar.
    if (img.quality_score != null) {
      var qs = Number(img.quality_score);
      var qSection = document.createElement("div");
      qSection.className = "quality-section";
      var qlbl = document.createElement("span");
      qlbl.className = "qlabel";
      qlbl.textContent = "Quality";
      qSection.appendChild(qlbl);
      var qtrack = document.createElement("div");
      qtrack.className = "quality-track";
      var qfill = document.createElement("div");
      qfill.className = "quality-fill";
      qfill.style.width = qs + "%";
      qfill.style.background = qualityColor(qs);
      qtrack.appendChild(qfill);
      qSection.appendChild(qtrack);
      var qval = document.createElement("span");
      qval.className = "qval";
      qval.style.color = qualityColor(qs);
      qval.textContent = String(qs);
      qSection.appendChild(qval);
      card.appendChild(qSection);
    }

    // Actions.
    var actions = document.createElement("div");
    actions.className = "card-actions";
    if (img.is_best) {
      var keepBadge = document.createElement("span");
      keepBadge.className = "badge badge-keep";
      keepBadge.textContent = "KEEP";
      actions.appendChild(keepBadge);
    } else {
      var delBadge = document.createElement("span");
      delBadge.className = "badge badge-delete";
      delBadge.textContent = "DELETE";
      actions.appendChild(delBadge);
    }
    var delBtn = document.createElement("button");
    delBtn.className = "btn btn-danger";
    delBtn.textContent = "Delete";
    delBtn.style.marginLeft = "auto";
    (function(p) {
      delBtn.onclick = function() { window.deleteFile(p); };
    })(img.path);
    actions.appendChild(delBtn);
    card.appendChild(actions);
    return card;
  }

})();
