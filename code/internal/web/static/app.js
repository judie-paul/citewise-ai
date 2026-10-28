// CiteWise AI: minimal progressive enhancements (copy, select all, busy state).
(function () {
  "use strict";

  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text);
    }
    // Fallback for plain-HTTP origins other than localhost.
    return new Promise(function (resolve, reject) {
      var ta = document.createElement("textarea");
      ta.value = text;
      ta.setAttribute("readonly", "");
      ta.className = "visually-hidden";
      document.body.appendChild(ta);
      ta.select();
      var ok = false;
      try { ok = document.execCommand("copy"); } catch (e) { ok = false; }
      document.body.removeChild(ta);
      if (ok) { resolve(); } else { reject(new Error("copy failed")); }
    });
  }

  document.addEventListener("click", function (ev) {
    var btn = ev.target.closest(".copy-btn");
    if (btn) {
      var label = btn.dataset.label || btn.textContent;
      btn.dataset.label = label;
      copyText(btn.dataset.copy || "").then(function () {
        btn.textContent = "Copied!";
      }, function () {
        btn.textContent = "Copy failed. Select the text manually.";
      });
      setTimeout(function () { btn.textContent = label; }, 2000);
      return;
    }

    var all = ev.target.closest("[data-select-all]");
    if (all) {
      var boxes = document.querySelectorAll('input[type="checkbox"][form="export-form"]');
      var anyUnchecked = Array.prototype.some.call(boxes, function (b) { return !b.checked; });
      boxes.forEach(function (b) { b.checked = anyUnchecked; });
      all.textContent = anyUnchecked ? "Clear selection" : "Select all";
    }
  });

  // Disable the submit button while a slow search or analysis is running.
  document.addEventListener("submit", function (ev) {
    var btn = ev.target.querySelector("[data-busy-text]");
    if (btn) {
      setTimeout(function () {
        btn.disabled = true;
        btn.textContent = btn.dataset.busyText;
      }, 0);
    }
  });

  // Re-enable buttons when the page is restored from the back/forward cache.
  window.addEventListener("pageshow", function () {
    document.querySelectorAll("[data-busy-text][disabled]").forEach(function (b) {
      b.disabled = false;
    });
  });
})();
