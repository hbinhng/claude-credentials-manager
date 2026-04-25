/* ccm serve · single-page UI.
 *
 * Vanilla JS, no framework, no build step. The server embeds this
 * file verbatim. It polls GET /api/credentials every 3 s and
 * re-renders the table in place, opens dialogs for the View Usage
 * and View Ticket actions, and surfaces API errors as toasts.
 */
(function () {
  "use strict";

  var POLL_MS = 3000;
  var appRoot = document.getElementById("app");

  // Inline SVG glyphs used by dialogs. Keeping them here avoids an
  // extra HTTP round-trip for icons.
  var ICON_COPY = '<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="14" height="14" x="8" y="8" rx="2" ry="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>';
  var ICON_CHECK = '<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>';
  var ICON_PLUS = '<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>';

  /* ---- rendering --------------------------------------------------- */

  // lastCredentials caches the most recent list so partial failures
  // (one poll hiccup) don't wipe the table.
  var lastCredentials = null;

  function renderShell() {
    appRoot.innerHTML = [
      '<div class="card">',
      '  <div class="card-header">',
      '    <div>',
      '      <div class="title-row">',
      '        <h2 class="card-title">Sessions</h2>',
      '        <button class="btn icon" id="add-credential-btn" type="button" aria-label="Add credential" title="Add credential">' + ICON_PLUS + '</button>',
      '      </div>',
      '      <p class="card-description">Manage shared credential sessions. Auto-refreshes every 3 s.</p>',
      '    </div>',
      '    <div class="card-toolbar">',
      '      <span class="muted" id="last-updated">loading…</span>',
      '    </div>',
      '  </div>',
      '  <div id="table-mount"></div>',
      '</div>',
      '<div id="toast-root"></div>',
      '<div id="reconnect">reconnecting…</div>',
    ].join("\n");
    document
      .getElementById("add-credential-btn")
      .addEventListener("click", openLoginDialog);
  }

  function renderTable(creds) {
    var mount = document.getElementById("table-mount");
    if (!creds || creds.length === 0) {
      mount.innerHTML =
        '<div class="empty">No credentials in the local store. Click + to add one.</div>';
      return;
    }
    var rows = creds.map(rowHTML).join("");
    mount.innerHTML =
      '<table>' +
        '<thead><tr>' +
          '<th>Name</th>' +
          '<th>Tier</th>' +
          '<th>Status</th>' +
          '<th>Session</th>' +
          '<th>Usage</th>' +
          '<th class="actions">Actions</th>' +
        '</tr></thead>' +
        '<tbody>' + rows + '</tbody>' +
      '</table>';
    bindRowActions(creds);
  }

  function rowHTML(c) {
    var tier = c.tier ? escapeHTML(c.tier) : '<span class="muted">—</span>';
    var credBadge =
      '<span class="badge status-' + c.credStatus + '">' + c.credStatus.replace("_", " ") + '</span>';
    var sessionCell;
    if (c.session) {
      sessionCell =
        '<span class="badge mode-' + c.session.mode + '">' +
          '<span class="dot"></span>' +
          c.session.mode + ' · live' +
        '</span>';
    } else {
      sessionCell = '<span class="badge idle">idle</span>';
    }
    var usageBtn =
      c.credStatus === "expired"
        ? '<button class="btn sm ghost" disabled>View usage</button>'
        : '<button class="btn sm ghost" data-action="usage" data-id="' + attr(c.id) + '">View usage</button>';
    var actionBtns = actionButtonsHTML(c);
    return (
      '<tr data-id="' + attr(c.id) + '">' +
        '<td class="name">' + escapeHTML(c.name || c.id.slice(0, 8)) + '</td>' +
        '<td class="tier">' + tier + '</td>' +
        '<td>' + credBadge + '</td>' +
        '<td>' + sessionCell + '</td>' +
        '<td>' + usageBtn + '</td>' +
        '<td class="actions">' + actionBtns + '</td>' +
      '</tr>'
    );
  }

  function actionButtonsHTML(c) {
    var buttons;
    if (c.session) {
      buttons =
        '<button class="btn sm" data-action="ticket" data-id="' + attr(c.id) + '">View ticket</button>' +
        '<button class="btn sm destructive" data-action="stop" data-id="' + attr(c.id) + '">Stop</button>';
    } else if (c.credStatus === "expired") {
      buttons =
        '<button class="btn sm" data-action="refresh" data-id="' + attr(c.id) + '">Refresh</button>';
    } else {
      buttons =
        '<button class="btn sm primary" data-action="start-tunnel" data-id="' + attr(c.id) + '">Start tunnel</button>' +
        '<button class="btn sm" data-action="start-lan" data-id="' + attr(c.id) + '">Start LAN</button>';
    }
    return '<span class="actions-group">' + buttons + '</span>';
  }

  function bindRowActions(creds) {
    var byID = {};
    creds.forEach(function (c) { byID[c.id] = c; });
    var nodes = appRoot.querySelectorAll("button[data-action]");
    for (var i = 0; i < nodes.length; i++) {
      (function (btn) {
        btn.addEventListener("click", function () {
          var id = btn.getAttribute("data-id");
          var action = btn.getAttribute("data-action");
          var cred = byID[id];
          switch (action) {
            case "usage":         return openUsageDialog(cred);
            case "ticket":        return openTicketDialog(cred);
            case "start-tunnel":  return startSession(cred, { mode: "tunnel" });
            case "start-lan":     return openStartLANDialog(cred);
            case "stop":          return stopSession(cred);
            case "refresh":       return refreshCredential(cred, btn);
          }
        });
      })(nodes[i]);
    }
  }

  /* ---- polling ----------------------------------------------------- */

  var pollTimer = null;

  function pollOnce() {
    return fetch("/api/credentials", { headers: { Accept: "application/json" } })
      .then(function (resp) {
        if (resp.status === 401) {
          window.location.href = "/login";
          throw new Error("unauthorized");
        }
        if (!resp.ok) { return resp.text().then(function (t) { throw new Error(t || ("HTTP " + resp.status)); }); }
        return resp.json();
      })
      .then(function (body) {
        lastCredentials = body.credentials || [];
        renderTable(lastCredentials);
        document.getElementById("reconnect").classList.remove("shown");
        document.getElementById("last-updated").textContent =
          "updated " + new Date().toLocaleTimeString();
      })
      .catch(function (err) {
        if (err && err.message === "unauthorized") return;
        document.getElementById("reconnect").classList.add("shown");
        // Keep the table as-is; don't wipe the last good render.
      });
  }

  function startPolling() {
    if (pollTimer) { return; }
    pollOnce();
    pollTimer = setInterval(pollOnce, POLL_MS);
  }

  function stopPolling() {
    if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
  }

  document.addEventListener("visibilitychange", function () {
    if (document.hidden) { stopPolling(); }
    else { startPolling(); }
  });

  /* ---- start/stop actions ----------------------------------------- */

  function startSession(cred, body) {
    postJSON("/api/credentials/" + encodeURIComponent(cred.id), body)
      .then(function () {
        toast("Started", "Tunnel session for " + (cred.name || cred.id) + ".", "success");
        pollOnce();
      })
      .catch(function (err) {
        toast("Could not start", err.message || "unknown error", "destructive");
      });
  }

  function refreshCredential(cred, btn) {
    // Disable the button for the duration of the round-trip — OAuth
    // refresh is a network call and the user shouldn't be able to
    // queue five.
    if (btn) {
      btn.disabled = true;
      btn.textContent = "Refreshing…";
    }
    postJSON("/api/credentials/" + encodeURIComponent(cred.id) + "/refresh", null)
      .then(function () {
        toast("Refreshed", "Tokens for " + (cred.name || cred.id) + " renewed.", "success");
        pollOnce();
      })
      .catch(function (err) {
        toast("Refresh failed", err.message || "unknown error", "destructive");
        if (btn) {
          btn.disabled = false;
          btn.textContent = "Refresh";
        }
      });
  }

  function stopSession(cred) {
    if (!confirm("Stop the session for " + (cred.name || cred.id) + "?")) { return; }
    fetch("/api/credentials/" + encodeURIComponent(cred.id), { method: "DELETE" })
      .then(function (resp) {
        if (resp.status === 204) {
          toast("Stopped", "Session for " + (cred.name || cred.id) + " ended.", "success");
          pollOnce();
          return;
        }
        return resp.json().then(function (b) { throw new Error((b && b.error) || ("HTTP " + resp.status)); });
      })
      .catch(function (err) {
        toast("Could not stop", err.message || "unknown error", "destructive");
      });
  }

  function openStartLANDialog(cred) {
    var dlg = dialog({
      title: "Start LAN session",
      description: "Expose " + (cred.name || cred.id) + " on the local network without a tunnel. The listener binds to 0.0.0.0; the host value goes into the ticket as the dial target.",
      body:
        '<label class="field-label" for="lan-host">Bind host</label>' +
        '<input class="input" id="lan-host" placeholder="e.g. host.docker.internal" required>' +
        '<label class="field-label" for="lan-port">Bind port (optional)</label>' +
        '<input class="input" id="lan-port" placeholder="auto" type="number" min="1" max="65535">',
      footer: [
        { label: "Cancel", ghost: true, action: "close" },
        { label: "Start LAN", primary: true, action: "submit" },
      ],
    });
    dlg.addEventListener("close", function () { dlg.remove(); });
    dlg.querySelector('[data-dialog-action="submit"]').addEventListener("click", function () {
      var host = dlg.querySelector("#lan-host").value.trim();
      var portStr = dlg.querySelector("#lan-port").value.trim();
      if (!host) { toast("Bind host required", "Enter the address the remote side will dial.", "destructive"); return; }
      var body = { mode: "lan", bindHost: host };
      if (portStr) {
        var n = parseInt(portStr, 10);
        if (isNaN(n) || n < 1 || n > 65535) {
          toast("Bad port", "Bind port must be between 1 and 65535.", "destructive");
          return;
        }
        body.bindPort = n;
      }
      dlg.close();
      startSession(cred, body);
    });
    setTimeout(function () { dlg.querySelector("#lan-host").focus(); }, 0);
  }

  /* ---- add-credential dialog -------------------------------------- */

  // openLoginDialog drives the two-step OAuth-add-credential flow:
  // step 1 calls /api/login/start to mint a server-side PKCE handshake;
  // step 2 swaps the dialog body to show the authorize URL + paste field
  // and posts to /api/login/finish on submit.
  function openLoginDialog() {
    var dlg = dialog({
      title: "Add Claude credential",
      description: "Generating login URL…",
      body: '<p class="muted">Contacting Claude…</p>',
      footer: [{ label: "Cancel", ghost: true, action: "close" }],
    });
    dlg.addEventListener("close", function () { dlg.remove(); });

    postJSON("/api/login/start", null)
      .then(function (resp) { renderLoginStep2(dlg, resp); })
      .catch(function (err) { renderLoginError(dlg, err); });
  }

  // renderLoginStep2 swaps the dialog body to the authorize-URL +
  // paste-code form once the handshake is in hand.
  function renderLoginStep2(dlg, start) {
    var bodyEl = dlg.querySelector(".dialog-body");
    bodyEl.innerHTML =
      fieldBlock("Authorize URL", start.authorizeUrl, "auth-url") +
      '<div>' +
      '  <a class="btn primary" id="login-open-link" href="' + attr(start.authorizeUrl) + '" target="_blank" rel="noopener noreferrer">Open authorize page ↗</a>' +
      '</div>' +
      '<label class="field-label" for="login-code">Paste code</label>' +
      '<input class="input" id="login-code" autocomplete="off" spellcheck="false">' +
      '<div id="login-error" class="quota-error" style="display:none"></div>';

    var copyFields = bodyEl.querySelectorAll(".copy-field");
    for (var i = 0; i < copyFields.length; i++) { bindCopyField(copyFields[i]); }

    var footer = dlg.querySelector(".dialog-footer");
    footer.innerHTML =
      '<button class="btn ghost" type="button" data-dialog-action="close">Cancel</button>' +
      '<button class="btn primary" type="button" id="login-submit">Submit</button>';
    footer.querySelector('[data-dialog-action="close"]')
      .addEventListener("click", function () { dlg.close(); });

    var input = bodyEl.querySelector("#login-code");
    var errBox = bodyEl.querySelector("#login-error");
    var submit = footer.querySelector("#login-submit");
    var openLink = bodyEl.querySelector("#login-open-link");

    openLink.addEventListener("click", function () {
      setTimeout(function () { input.focus(); }, 0);
    });

    function go() {
      var code = input.value.trim();
      if (!code) {
        toast("Code required", "Paste the code from the authorize page first.", "destructive");
        input.focus();
        return;
      }
      submit.disabled = true;
      input.disabled = true;
      errBox.style.display = "none";
      errBox.textContent = "";
      postJSON("/api/login/finish", { handshakeId: start.handshakeId, code: code })
        .then(function (resp) {
          dlg.close();
          var name = (resp && resp.credential && (resp.credential.name || resp.credential.id)) || "new credential";
          toast("Logged in", "Logged in as " + name + ".", "success");
          pollOnce();
        })
        .catch(function (err) {
          var msg = err && err.message ? err.message : "unknown error";
          errBox.textContent = msg;
          errBox.style.display = "";
          if (/expired|start over/i.test(msg)) {
            // Handshake is gone; force the user back to the (+) button.
            submit.disabled = true;
            input.disabled = true;
          } else {
            submit.disabled = false;
            input.disabled = false;
            input.focus();
          }
        });
    }
    submit.addEventListener("click", go);
    input.addEventListener("keydown", function (ev) {
      if (ev.key === "Enter") { ev.preventDefault(); go(); }
    });

    setTimeout(function () { input.focus(); }, 0);
  }

  // renderLoginError replaces the step-1 body with an error block and
  // a Retry button that re-fires /api/login/start in place.
  function renderLoginError(dlg, err) {
    var bodyEl = dlg.querySelector(".dialog-body");
    var msg = err && err.message ? err.message : "unknown error";
    bodyEl.innerHTML = '<div class="quota-error">' + escapeHTML(msg) + '</div>';

    var footer = dlg.querySelector(".dialog-footer");
    footer.innerHTML =
      '<button class="btn ghost" type="button" data-dialog-action="close">Close</button>' +
      '<button class="btn primary" type="button" id="login-retry">Retry</button>';
    footer.querySelector('[data-dialog-action="close"]')
      .addEventListener("click", function () { dlg.close(); });
    footer.querySelector("#login-retry").addEventListener("click", function () {
      bodyEl.innerHTML = '<p class="muted">Contacting Claude…</p>';
      footer.innerHTML = '<button class="btn ghost" type="button" data-dialog-action="close">Cancel</button>';
      footer.querySelector('[data-dialog-action="close"]')
        .addEventListener("click", function () { dlg.close(); });
      postJSON("/api/login/start", null)
        .then(function (resp) { renderLoginStep2(dlg, resp); })
        .catch(function (e) { renderLoginError(dlg, e); });
    });
  }

  /* ---- usage dialog ------------------------------------------------ */

  function openUsageDialog(cred) {
    var dlg = dialog({
      title: "Usage · " + (cred.name || cred.id),
      description: "Live quota fetched from Claude.",
      body: '<p class="muted">Loading…</p>',
      footer: [{ label: "Close", ghost: true, action: "close" }],
    });
    dlg.addEventListener("close", function () { dlg.remove(); });

    fetch("/api/credentials/" + encodeURIComponent(cred.id), {
      headers: { Accept: "application/json" },
    })
      .then(function (resp) {
        if (!resp.ok) { return resp.json().then(function (b) { throw new Error((b && b.error) || "HTTP " + resp.status); }); }
        return resp.json();
      })
      .then(function (detail) {
        var bodyEl = dlg.querySelector(".dialog-body");
        bodyEl.innerHTML = "";
        if (!detail.quota || !detail.quota.fetched) {
          bodyEl.appendChild(renderNote("Quota was not fetched — credential is expired or offline."));
          return;
        }
        if (detail.quota.error) {
          var err = document.createElement("div");
          err.className = "quota-error";
          err.textContent = "Fetch failed: " + detail.quota.error;
          bodyEl.appendChild(err);
          return;
        }
        if (!detail.quota.windows || detail.quota.windows.length === 0) {
          bodyEl.appendChild(renderNote("Upstream returned no quota windows."));
          return;
        }
        detail.quota.windows.forEach(function (w) {
          var row = document.createElement("div");
          row.className = "quota-row";
          var used = Math.max(0, Math.min(100, w.used));
          row.innerHTML =
            '<div class="name">' + escapeHTML(w.name) + '</div>' +
            '<div>' + used.toFixed(0) + '% used</div>' +
            '<div class="quota-bar"><span style="width:' + used + '%"></span></div>' +
            (w.resetsIn
              ? '<div class="sub">resets ' + escapeHTML(w.resetsIn) + '</div>'
              : "");
          bodyEl.appendChild(row);
        });
      })
      .catch(function (err) {
        var bodyEl = dlg.querySelector(".dialog-body");
        bodyEl.innerHTML = "";
        var errEl = document.createElement("div");
        errEl.className = "quota-error";
        errEl.textContent = err.message || String(err);
        bodyEl.appendChild(errEl);
      });
  }

  /* ---- ticket dialog ----------------------------------------------- */

  function openTicketDialog(cred) {
    if (!cred.session) {
      toast("No active session", "Start a session first.", "destructive");
      return;
    }
    var endpoint = cred.session.mode === "lan"
      ? "http://" + stripScheme(cred.session.reach)
      : cred.session.reach;
    var ticket = cred.session.ticket;
    var launchCmd = "ccm launch --via '" + ticket + "' --";

    var dlg = dialog({
      title: "Ticket · " + (cred.name || cred.id),
      description: "Click any field to copy.",
      body:
        fieldBlock("Endpoint", endpoint, "endpoint") +
        fieldBlock("Ticket",   ticket,   "ticket") +
        fieldBlock("Launch command", launchCmd, "launch"),
      footer: [{ label: "Close", ghost: true, action: "close" }],
    });
    dlg.addEventListener("close", function () { dlg.remove(); });

    var copyFields = dlg.querySelectorAll(".copy-field");
    for (var i = 0; i < copyFields.length; i++) { bindCopyField(copyFields[i]); }
  }

  function fieldBlock(label, value, key) {
    return (
      '<div>' +
        '<label class="field-label" for="copy-' + key + '">' + label + '</label>' +
        '<div class="copy-field" data-copy-value="' + attr(value) + '">' +
          '<input class="input" id="copy-' + key + '" readonly value="' + attr(value) + '">' +
          '<button class="btn" type="button" aria-label="copy">' + ICON_COPY + '</button>' +
        '</div>' +
      '</div>'
    );
  }

  function bindCopyField(el) {
    var value = el.getAttribute("data-copy-value");
    var input = el.querySelector("input");
    var btn = el.querySelector("button");
    function go() {
      input.select();
      navigator.clipboard.writeText(value).then(function () {
        el.classList.add("copied");
        btn.innerHTML = ICON_CHECK;
        setTimeout(function () {
          el.classList.remove("copied");
          btn.innerHTML = ICON_COPY;
        }, 1200);
      });
    }
    btn.addEventListener("click", go);
    input.addEventListener("click", go);
  }

  function stripScheme(s) {
    return String(s || "").replace(/^https?:\/\//, "");
  }

  /* ---- dialog scaffolding ----------------------------------------- */

  // dialog builds a <dialog> element, populates header/body/footer,
  // attaches it to the document, and calls showModal(). Returns the
  // element so the caller can wire handlers.
  function dialog(cfg) {
    var dlg = document.createElement("dialog");
    dlg.innerHTML =
      '<div class="dialog-header">' +
        '<h3 class="dialog-title">' + escapeHTML(cfg.title) + '</h3>' +
        (cfg.description ? '<p class="dialog-description">' + escapeHTML(cfg.description) + '</p>' : "") +
      '</div>' +
      '<div class="dialog-body">' + (cfg.body || "") + '</div>' +
      '<div class="dialog-footer">' + (cfg.footer || []).map(footerButton).join("") + '</div>';
    document.body.appendChild(dlg);
    dlg.querySelectorAll('[data-dialog-action="close"]').forEach(function (b) {
      b.addEventListener("click", function () { dlg.close(); });
    });
    dlg.showModal();
    return dlg;
  }

  function footerButton(f) {
    var cls = "btn";
    if (f.primary) cls += " primary";
    if (f.destructive) cls += " destructive";
    if (f.ghost) cls += " ghost";
    return '<button class="' + cls + '" type="button" data-dialog-action="' + (f.action || "close") + '">' + escapeHTML(f.label) + '</button>';
  }

  function renderNote(text) {
    var el = document.createElement("p");
    el.className = "muted";
    el.textContent = text;
    return el;
  }

  /* ---- toast ------------------------------------------------------- */

  function toast(title, msg, variant) {
    var root = document.getElementById("toast-root");
    if (!root) return;
    var el = document.createElement("div");
    el.className = "toast " + (variant || "");
    el.innerHTML =
      '<div>' +
        '<div class="title">' + escapeHTML(title) + '</div>' +
        '<div class="msg">' + escapeHTML(msg) + '</div>' +
      '</div>';
    root.appendChild(el);
    setTimeout(function () {
      el.style.opacity = "0";
      el.style.transition = "opacity 0.2s ease";
      setTimeout(function () { el.remove(); }, 220);
    }, 4500);
  }

  /* ---- fetch helpers ---------------------------------------------- */

  function postJSON(url, body) {
    return fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json", Accept: "application/json" },
      body: body ? JSON.stringify(body) : "",
    }).then(function (resp) {
      if (resp.status === 201 || resp.status === 200) { return resp.json(); }
      return resp.json().then(
        function (b) { throw new Error((b && b.error) || "HTTP " + resp.status); },
        function ()  { throw new Error("HTTP " + resp.status); },
      );
    });
  }

  /* ---- escaping --------------------------------------------------- */

  function escapeHTML(s) {
    return String(s == null ? "" : s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  function attr(s) { return escapeHTML(s); }

  /* ---- boot ------------------------------------------------------- */

  renderShell();
  startPolling();
})();
