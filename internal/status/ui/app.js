(() => {
  const $ = (id) => document.getElementById(id);

  function esc(s) {
    return String(s ?? "").replace(/[&<>"']/g, (c) => ({
      "&": "&amp;",
      "<": "&lt;",
      ">": "&gt;",
      '"': "&quot;",
      "'": "&#39;",
    }[c]));
  }

  async function getJSON(path) {
    const r = await fetch(path);
    const data = await r.json().catch(() => ({}));
    return { ok: r.ok, status: r.status, data };
  }

  async function postJSON(path) {
    const r = await fetch(path, { method: "POST" });
    const data = await r.json().catch(() => ({}));
    return { ok: r.ok, status: r.status, data };
  }

  function pill(el, ok, label) {
    el.textContent = label;
    el.className = "pill " + (ok ? "ok" : "bad");
  }

  function statePill(state) {
    const s = String(state || "unknown").toLowerCase();
    let cls = "muted";
    if (s === "healthy") cls = "ok";
    else if (s === "degraded") cls = "warn";
    else if (s === "unhealthy") cls = "bad";
    return `<span class="pill ${cls}">${esc(s)}</span>`;
  }

  function layer(name, l) {
    if (!l) return "";
    const ok = l.ok ? "ok-text" : "err";
    const extra = l.error ? ` ${esc(l.error)}` : l.status ? ` ${esc(l.status)}` : "";
    return `<span class="${ok}">${esc(name)}${extra}</span>`;
  }

  function countOf(c, key) {
    if (!c) return 0;
    return c[key] ?? c[key[0].toUpperCase() + key.slice(1)] ?? 0;
  }

  let backendsCache = [];

  function renderBackends(list) {
    backendsCache = list || [];
    if (!backendsCache.length) {
      $("backends").innerHTML = "<p class=\"hint\">No backends.</p>";
      return;
    }
    const rows = backendsCache.map((b) => {
      const name = esc(b.name);
      const flags = [
        b.disabled ? "disabled" : "",
        b.config_disabled ? "config" : "",
        b.runtime_disabled ? "runtime" : "",
        b.ineligible_reason ? b.ineligible_reason : "",
      ].filter(Boolean);
      const meta = [b.public_name, b.version, b.public_id].filter(Boolean).map(esc).join(" · ");
      const layers = [
        layer("reach", b.reachability),
        layer("public", b.public_info),
        layer("health", b.jellyfin_health),
        layer("auth", b.auth_plane),
      ].join(" · ");
      let action = "";
      if (b.config_disabled) {
        action = `<button type="button" disabled title="YAML disabled">Enable</button>`;
      } else if (b.runtime_disabled || b.disabled) {
        action = `<button type="button" data-act="enable" data-name="${name}">Enable</button>`;
      } else {
        action = `<button type="button" class="danger" data-act="disable" data-name="${name}">Disable</button>`;
      }
      return `<tr>
        <td><strong>${name}</strong><div class="layers">${meta}</div></td>
        <td>${statePill(b.state)}<div class="layers">${esc(flags.join(" · "))}</div></td>
        <td class="layers">${layers}</td>
        <td>${action}</td>
      </tr>`;
    }).join("");
    $("backends").innerHTML = `<table>
      <thead><tr><th>Backend</th><th>State</th><th>Layers</th><th></th></tr></thead>
      <tbody>${rows}</tbody>
    </table>`;
  }

  function renderAffinity(counts) {
    const names = Object.keys(counts || {});
    if (!names.length) {
      $("affinity").innerHTML = "<p class=\"hint\">No bindings.</p>";
      return;
    }
    const rows = names.map((n) => {
      const c = counts[n];
      return `<tr><td>${esc(n)}</td><td>${esc(countOf(c, "Tokens"))}</td><td>${esc(countOf(c, "Devices"))}</td><td>${esc(countOf(c, "Anons"))}</td></tr>`;
    }).join("");
    $("affinity").innerHTML = `<table>
      <thead><tr><th>Backend</th><th>Tokens</th><th>Devices</th><th>Anon</th></tr></thead>
      <tbody>${rows}</tbody>
    </table>`;
  }

  function renderHints(entries) {
    if (!entries || !entries.length) {
      $("user-affinity").innerHTML = "<p class=\"hint\">None configured.</p>";
      return;
    }
    const rows = entries.map((e) => `<tr><td>${esc(e.username)}</td><td>${esc(e.backend)}</td></tr>`).join("");
    $("user-affinity").innerHTML = `<table>
      <thead><tr><th>Username</th><th>Backend</th></tr></thead>
      <tbody>${rows}</tbody>
    </table>`;
  }

  function userID(u) {
    return u == null || u.userId == null ? "" : String(u.userId);
  }

  function sortUsers(rows) {
    return [...rows].sort((a, b) => {
      const ua = String(a.username || "").toLowerCase();
      const ub = String(b.username || "").toLowerCase();
      if (ua !== ub) return ua < ub ? -1 : 1;
      const ia = userID(a);
      const ib = userID(b);
      if (ia !== ib) return ia < ib ? -1 : 1;
      const ba = String(a.backend || "");
      const bb = String(b.backend || "");
      return ba < bb ? -1 : ba > bb ? 1 : 0;
    });
  }

  function renderUsers(rows) {
    const list = sortUsers(rows || []);
    if (!list.length) {
      $("users").innerHTML = "<p class=\"hint\">No token-bound users.</p>";
      return;
    }
    const body = list.map((u) => {
      const id = userID(u);
      return `<tr class="user-row" data-id="${esc(id)}" data-backend="${esc(u.backend)}" data-username="${esc(u.username || "")}">
        <td>${esc(u.username || "—")}</td>
        <td>${esc(id || "—")}</td>
        <td>${esc(u.backend)}</td>
        <td>${esc(u.status)}${u.graylisted ? " · gray" : ""}</td>
        <td>${esc(u.lastActive || "")}</td>
      </tr>`;
    }).join("");
    $("users").innerHTML = `<table>
      <thead><tr><th>User</th><th>Id</th><th>Backend</th><th>Status</th><th>Last</th></tr></thead>
      <tbody>${body}</tbody>
    </table>`;
  }

  function renderDetail(dumps) {
    const box = $("user-detail");
    if (!dumps || !dumps.length) {
      box.innerHTML = "<p class=\"hint\">No sessions for this user.</p>";
      return;
    }
    const blocks = dumps.map((d) => {
      const sess = (d.sessions || []).map((s) =>
        `<tr><td>${esc(s.deviceId || "")}</td><td>${esc(s.client || "")}</td><td>${esc(s.tokenHashPrefix || "")}</td><td>${esc(s.lastPath || "")}</td><td>${esc(s.lastActive || "")}</td></tr>`
      ).join("");
      return `<h3>${esc(d.username || d.userId || "user")} · ${esc(d.backend)}</h3>
        <p class="layers">${statePill(d.backendHealth && d.backendHealth.state)} ${esc((d.backendHealth && d.backendHealth.public_name) || "")}</p>
        <table>
          <thead><tr><th>Device</th><th>Client</th><th>Token hash</th><th>Path</th><th>Last</th></tr></thead>
          <tbody>${sess || "<tr><td colspan=\"5\">No sessions</td></tr>"}</tbody>
        </table>`;
    }).join("");
    const username = dumps.find((d) => d.username)?.username || selected?.username || "";
    const unpin = username
      ? `<div class="modal-actions"><button type="button" class="danger" data-act="unpin" data-username="${esc(username)}">Unpin ${esc(username)}</button></div>`
      : "";
    box.innerHTML = blocks + unpin;
  }

  function renderPerf(p, cache) {
    const img = (p && p.images) || cache || {};
    const lib = (p && p.library) || {};
    const coal = (p && p.coalesce) || {};
    const conc = (p && p.libraryConcurrency) || {};
    const disk = img.disk || {};
    $("perf").innerHTML = `<table>
      <tbody>
        <tr><td>auth_timeout</td><td>${esc(p && p.authTimeout || "—")}</td></tr>
        <tr><td>images</td><td>${img.enabled ? `${esc(img.objects)} obj · ${esc(img.bytes)} B · hit ${esc(img.hits)}` : "off"}</td></tr>
        <tr><td>image disk</td><td>${disk.enabled ? `${esc(disk.objects)} obj · ${esc(disk.bytes)} B · ${esc(disk.path || "")}` : "off"}</td></tr>
        <tr><td>library</td><td>${lib.enabled ? `${esc(lib.objects)} obj · ${esc(lib.bytes)} B` : "off"}</td></tr>
        <tr><td>coalesce</td><td>${coal.enabled ? `solo ${esc(coal.solo)} · shared ${esc(coal.shared)}` : "off"}</td></tr>
        <tr><td>library concurrency</td><td>${conc.enabled ? `max ${esc(conc.max)}` : "off"}</td></tr>
      </tbody>
    </table>`;
  }

  const tabs = ["overview", "users"];
  let selected = null;

  function showTab(name) {
    if (!tabs.includes(name)) name = "overview";
    for (const t of tabs) {
      const on = t === name;
      $("tab-" + t).hidden = !on;
      $("tab-btn-" + t).setAttribute("aria-selected", on ? "true" : "false");
    }
    if ((location.hash || "").replace(/^#/, "") !== name) {
      history.replaceState(null, "", "#" + name);
    }
  }

  function closeModal() {
    selected = null;
    $("user-modal").hidden = true;
    $("user-detail").innerHTML = "";
  }

  async function loadUser(sel) {
    if (!sel || !sel.id) {
      $("user-detail").innerHTML = "<p class=\"hint\">No user id.</p>";
      return;
    }
    const path = "/hap/users/" + encodeURIComponent(sel.id) + (sel.backend ? "?backend=" + encodeURIComponent(sel.backend) : "");
    const res = await getJSON(path);
    if (!selected || selected.id !== sel.id || selected.backend !== sel.backend) return;
    if (res.status === 404) {
      $("user-detail").innerHTML = "<p class=\"hint\">User is no longer pinned.</p>";
      return;
    }
    renderDetail(Array.isArray(res.data) ? res.data : []);
  }

  async function openUser(id, backend, username) {
    selected = { id, backend, username };
    $("user-modal").hidden = false;
    $("user-modal-title").textContent = username || id || "User";
    $("user-detail").innerHTML = "<p class=\"hint\">Loading…</p>";
    $("user-modal").querySelector(".modal-close").focus();
    await loadUser(selected);
  }

  async function refresh() {
    const [health, ready, backends, affinity, hints, users, perf, cache] = await Promise.all([
      getJSON("/hap/health"),
      getJSON("/hap/ready"),
      getJSON("/hap/backends"),
      getJSON("/hap/affinity"),
      getJSON("/hap/user-affinity"),
      getJSON("/hap/users"),
      getJSON("/hap/performance"),
      getJSON("/hap/cache"),
    ]);
    pill($("health-pill"), health.ok && health.data.ok, health.ok && health.data.ok ? "live" : "down");
    pill($("ready-pill"), ready.ok && ready.data.ok, ready.ok && ready.data.ok ? "ready" : "not ready");
    renderBackends(backends.data.backends);
    renderAffinity(affinity.data);
    renderHints(hints.data.userAffinity);
    renderUsers(Array.isArray(users.data) ? users.data : []);
    renderPerf(perf.data, cache.data);
    if (selected) await loadUser(selected);
  }

  $("backends").addEventListener("click", async (ev) => {
    const btn = ev.target.closest("button[data-act]");
    if (!btn) return;
    const name = btn.getAttribute("data-name");
    const act = btn.getAttribute("data-act");
    if (act === "disable" && !confirm("Disable backend " + name + "? Bound clients will get 503.")) return;
    if (act === "enable" && !confirm("Enable backend " + name + "?")) return;
    const res = await postJSON("/hap/backends/" + encodeURIComponent(name) + "/" + act);
    if (res.status === 409) {
      alert("Cannot enable: YAML disabled: true");
      return;
    }
    if (!res.ok) {
      alert(res.data.error || "request failed");
      return;
    }
    if (res.data.backends) renderBackends(res.data.backends);
    else refresh();
  });

  document.querySelector(".tabs").addEventListener("click", (ev) => {
    const btn = ev.target.closest("[data-tab]");
    if (btn) showTab(btn.getAttribute("data-tab"));
  });
  window.addEventListener("hashchange", () => showTab((location.hash || "").replace(/^#/, "")));

  $("users").addEventListener("click", (ev) => {
    const row = ev.target.closest("tr.user-row");
    if (!row) return;
    openUser(row.getAttribute("data-id") || "", row.getAttribute("data-backend") || "", row.getAttribute("data-username") || "");
  });

  $("user-modal").addEventListener("click", async (ev) => {
    if (ev.target.closest("[data-close]")) {
      closeModal();
      return;
    }
    const btn = ev.target.closest("button[data-act=unpin]");
    if (!btn) return;
    const username = btn.getAttribute("data-username");
    if (!username) return;
    await unpinUser(username);
  });

  document.addEventListener("keydown", (ev) => {
    if (ev.key === "Escape" && !$("user-modal").hidden) closeModal();
  });

  async function unpinUser(username) {
    if (!confirm("Unpin " + username + "? Deletes stored token and DeviceId pins.")) return false;
    const res = await postJSON("/hap/users/by-name/" + encodeURIComponent(username) + "/unpin");
    const out = $("unpin-result");
    out.hidden = false;
    if (!res.ok) {
      out.className = "hint err";
      out.textContent = res.data.error || "unpin failed";
      return false;
    }
    out.className = "hint ok-text";
    out.textContent = username + ": tokens " + (res.data.tokens ?? 0) + ", devices " + (res.data.devices ?? 0);
    closeModal();
    await refresh();
    return true;
  }

  $("unpin-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const username = $("unpin-name").value.trim();
    if (!username) return;
    await unpinUser(username);
  });

  showTab((location.hash || "").replace(/^#/, "") || "overview");
  refresh();
  setInterval(refresh, 5000);
})();
