// static/app.js
(function(){
  "use strict";

  var pollTimer = null;

  // Helper functions
  function formatBytes(bytes) {
    if(bytes == null || isNaN(bytes)) return "--";
    if(bytes === 0) return "0 B";
    var units = ["B", "KB", "MB", "GB", "TB"];
    var i = Math.floor(Math.log(bytes) / Math.log(1024));
    if(i >= units.length) i = units.length - 1;
    return (bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1) + " " + units[i];
  }

  function formatDuration(ms) {
    if(ms == null || isNaN(ms)) return "--";
    if(ms < 1000) return ms + "ms";
    var s = ms / 1000;
    if(s < 60) return s.toFixed(1) + "s";
    var m = Math.floor(s / 60);
    s = Math.floor(s % 60);
    return m + "m " + s + "s";
  }

  function formatDate(iso) {
    if(!iso) return "--";
    try {
      var d = new Date(iso);
      if(isNaN(d.getTime())) return iso;
      return d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" }) +
             " " + d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
    } catch(e) { return iso; }
  }

  function formatGPS(lat, lon) {
    if(lat == null || lon == null) return "--";
    return Number(lat).toFixed(4) + ", " + Number(lon).toFixed(4);
  }

  function escapeHtml(s) {
    if(!s) return "";
    return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }

  function qualityColor(score) {
    var r, g;
    if(score <= 50) { r = 255; g = Math.round(score * 5.1); }
    else { r = Math.round((100 - score) * 5.1); g = 255; }
    return "rgb(" + r + "," + g + ",60)";
  }

  // Toast System
  function showToast(message, type) {
    var container = document.getElementById("toast-container");
    var el = document.createElement("div");
    el.className = "toast toast-" + (type || "success");
    el.textContent = message;
    container.appendChild(el);
    setTimeout(function() {
      el.classList.add("removing");
      setTimeout(function() { if(el.parentNode) el.parentNode.removeChild(el); }, 220);
    }, 3000);
  }

  // Confirm Dialog
  function showConfirm(message, onYes) {
    var overlay = document.createElement("div");
    overlay.className = "confirm-overlay";
    overlay.innerHTML = '<div class="confirm-box"><p>' + escapeHtml(message) + '</p><div class="btn-row"><button class="btn btn-danger" id="confirm-yes">Delete</button><button class="btn" style="background: var(--border); color: var(--text)" id="confirm-no">Cancel</button></div></div>';
    document.body.appendChild(overlay);
    overlay.querySelector("#confirm-yes").onclick = function() {
      document.body.removeChild(overlay); onYes();
    };
    overlay.querySelector("#confirm-no").onclick = function() {
      document.body.removeChild(overlay);
    };
    overlay.addEventListener("click", function(e) {
      if(e.target === overlay) document.body.removeChild(overlay);
    });
  }

  // API Calls
  window.startScan = function() {
    var pathInput = document.getElementById("scan-path");
    var thresholdInput = document.getElementById("scan-threshold");
    var path = pathInput.value.trim();
    if(!path) {
      showToast("Please enter a folder path.", "error");
      pathInput.focus();
      return;
    }
    var threshold = parseInt(thresholdInput.value, 10);
    if(isNaN(threshold) || threshold < 0) threshold = 10;

    var btn = document.getElementById("scan-btn");
    btn.disabled = true; btn.textContent = "Scanning...";

    document.getElementById("groups-container").innerHTML = "";
    document.getElementById("empty-state").style.display = "none";
    document.getElementById("stats-bar").style.display = "none";
    showProgress(true); updateProgress("Starting...", 0, 0);

    fetch("/api/scan", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path: path, threshold: threshold })
    })
    .then(function(r) { return r.json(); })
    .then(function(data) {
      if(data.status === "complete") { stopPolling(); renderResults(data); }
      else { startPolling(); }
    })
    .catch(function(err) {
      showToast("Scan request failed: " + err.message, "error");
      btn.disabled = false; btn.textContent = "Scan"; showProgress(false);
    });
  };

  function startPolling() { stopPolling(); pollTimer = setInterval(loadResults, 500); }
  function stopPolling() { if(pollTimer) { clearInterval(pollTimer); pollTimer = null; } }

  window.loadResults = function() {
    fetch("/api/results")
    .then(function(r) { return r.json(); })
    .then(function(data) {
      if(data.progress) {
        updateProgress(
          data.progress.phase || "Scanning...",
          data.progress.current || 0,
          data.progress.total || 0
        );
      }
      if(data.status === "complete" || data.status === "idle") { stopPolling(); renderResults(data); }
    })
    .catch(function(err) {
      stopPolling(); showToast("Failed to fetch results: " + err.message, "error");
      var btn = document.getElementById("scan-btn"); btn.disabled = false; btn.textContent = "Scan"; showProgress(false);
    });
  };

  window.deleteFile = function(path) {
    showConfirm("Permanently delete " + path + "?", function() {
      fetch("/api/delete", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path: path })
      })
      .then(function(r) { return r.json(); })
      .then(function(data) {
        if(data.success) {
          showToast("Deleted: " + path, "success");
          var cards = document.querySelectorAll(".image-card");
          for(var i = 0; i < cards.length; i++) {
            if(cards[i].getAttribute("data-path") === path) {
              var group = cards[i].closest(".group-card");
              cards[i].parentNode.removeChild(cards[i]);
              if(group) {
                var remaining = group.querySelectorAll(".image-card");
                if(remaining.length <= 1) { group.parentNode.removeChild(group); }
              }
              break;
            }
          }
          if(document.querySelectorAll(".group-card").length === 0) {
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

  // UI Updates
  function showProgress(show) {
    var el = document.getElementById("progress-section");
    if(show) el.classList.add("active"); else el.classList.remove("active");
  }

  function updateProgress(phase, current, total) {
    document.getElementById("progress-phase").textContent = phase;
    document.getElementById("progress-counts").textContent = current + " / " + total;
    var pct = (total > 0) ? Math.round(current / total * 100) : 0;
    document.getElementById("progress-fill").style.width = pct + "%";
  }

  function renderResults(data) {
    var btn = document.getElementById("scan-btn");
    btn.disabled = false; btn.textContent = "Scan"; showProgress(false);

    var stats = data.stats || {};
    var statsBar = document.getElementById("stats-bar");
    if(stats.total_files != null) {
      statsBar.style.display = "grid";
      document.getElementById("stat-files").textContent = stats.total_files.toLocaleString();
      document.getElementById("stat-groups").textContent = (stats.duplicate_groups || 0).toLocaleString();
      document.getElementById("stat-savings").textContent = formatBytes(stats.wasted_bytes || 0);
      document.getElementById("stat-duration").textContent = formatDuration(stats.duration_ms);
    }

    var groups = data.groups || [];
    var container = document.getElementById("groups-container");
    container.innerHTML = "";

    if(groups.length === 0) {
      var empty = document.getElementById("empty-state");
      empty.style.display = "block";
      if(data.status === "complete") {
        empty.querySelector("p").textContent = "No duplicates found. Your library is clean!";
      }
      return;
    }

    document.getElementById("empty-state").style.display = "none";
    for(var g = 0; g < groups.length; g++) {
      container.appendChild(buildGroup(groups[g]));
    }
  }

  function buildGroup(group) {
    var card = document.createElement("div");
    card.className = "group-card";

    var isExact = (group.match_type === "exact");
    var badgeClass = isExact ? "badge-exact" : "badge-perceptual";
    var badgeText = isExact ? "Exact" : "Perceptual";

    var header = document.createElement("div");
    header.className = "group-header";
    header.innerHTML = '<span class="group-title">Group</span><span class="badge ' + badgeClass + '">' + badgeText + '</span><span class="confidence">' + (group.confidence != null ? Number(group.confidence).toFixed(1) + "% match" : "") + '</span>';
    card.appendChild(header);

    var imagesWrap = document.createElement("div");
    imagesWrap.className = "group-images";
    var images = group.images || [];
    for(var i = 0; i < images.length; i++) {
      imagesWrap.appendChild(buildImageCard(images[i]));
    }
    card.appendChild(imagesWrap);
    return card;
  }

  function buildImageCard(img) {
    var card = document.createElement("div");
    card.className = "image-card";
    card.setAttribute("data-path", img.path || "");

    // Thumbnail
    var thumbWrap = document.createElement("div");
    thumbWrap.className = "thumb-wrap";
    var thumbImg = document.createElement("img");
    thumbImg.src = "/api/thumbnail?path=" + encodeURIComponent(img.path || "");
    thumbImg.alt = escapeHtml(img.filename || "thumbnail");
    thumbImg.loading = "lazy";
    thumbImg.onerror = function() { this.style.display = "none"; thumbWrap.textContent = "No preview"; };
    thumbWrap.appendChild(thumbImg);
    card.appendChild(thumbWrap);

    // Filename
    var fnEl = document.createElement("div");
    fnEl.className = "filename";
    fnEl.textContent = img.filename || "Unknown";
    card.appendChild(fnEl);

    // Path
    var fpEl = document.createElement("div");
    fpEl.className = "filepath";
    fpEl.textContent = img.path || "";
    card.appendChild(fpEl);

    // Metadata grid
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
      ["Quality Score", img.quality_score != null ? (img.quality_score + "/100") : "--"]
    ];
    for(var f = 0; f < fields.length; f++) {
      meta.innerHTML += '<div class="meta-item"><span class="meta-label">' + fields[f][0] + '</span><span class="meta-value">' + escapeHtml(fields[f][1]) + '</span></div>';
    }
    card.appendChild(meta);

    // Quality bar
    if(img.quality_score != null) {
      var qs = Number(img.quality_score);
      var qSection = document.createElement("div");
      qSection.className = "quality-section";
      qSection.innerHTML = '<span class="qlabel">Quality</span><div class="quality-track"><div class="quality-fill" style="width:' + qs + '%;background:' + qualityColor(qs) + '"></div></div><span class="qval" style="color:' + qualityColor(qs) + '">' + qs + '</span>';
      card.appendChild(qSection);
    }

    // Actions
    var actions = document.createElement("div");
    actions.className = "card-actions";
    if(img.is_best) {
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
    (function(p){
      delBtn.onclick = function() { window.deleteFile(p); };
    })(img.path);
    actions.appendChild(delBtn);
    card.appendChild(actions);
    return card;
  }

})();