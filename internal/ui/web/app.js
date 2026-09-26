const pages = {
  accounts: accountsPage,
  catalog: catalogPage,
  showcase: showcasePage,
  clients: clientsPage,
  health: healthPage,
  requests: requestsPage,
  settings: settingsPage,
};

const FILTER_KEY = "peaproxy.catalogFilter";
const TOKEN_KEY = "peaproxy.adminToken";

function render(name) {
  const fn = pages[name];
  if (!fn) return;
  document.getElementById("page").innerHTML = "";
  document.querySelectorAll("nav button").forEach((b) => {
    b.classList.toggle("active", b.dataset.page === name);
  });
  fn(document.getElementById("page"));
}

function adminHeaders(extra) {
  const h = Object.assign({}, extra || {});
  const tok = localStorage.getItem(TOKEN_KEY) || "";
  if (tok) h["X-Admin-Token"] = tok;
  return h;
}

function toast(message, kind) {
  const host = document.getElementById("toasts");
  if (!host) return;
  const el = document.createElement("div");
  el.className = "toast" + (kind === "ok" ? " ok" : "");
  el.textContent = message;
  host.appendChild(el);
  setTimeout(() => el.remove(), 5000);
}

async function getJSON(url) {
  const res = await fetch(url, { headers: adminHeaders() });
  const text = await res.text();
  let data = {};
  try {
    data = text ? JSON.parse(text) : {};
  } catch {
    throw new Error(text || res.statusText);
  }
  if (!res.ok) {
    throw new Error(data.error || res.statusText || "request failed");
  }
  return data;
}

async function sendJSON(url, method, body) {
  const res = await fetch(url, {
    method,
    headers: adminHeaders({ "Content-Type": "application/json" }),
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function emptyState(title, detail) {
  return `<div class="empty"><strong>${escapeHtml(title)}</strong><br />${escapeHtml(detail)}</div>`;
}

async function refreshLanBanner() {
  const el = document.getElementById("lan-banner");
  if (!el) return;
  try {
    const d = await fetch("/healthz").then((r) => r.json());
    if (!d.lanWarning && !d.adminTokenRequired) {
      el.hidden = true;
      el.innerHTML = "";
      return;
    }
    el.hidden = false;
    el.innerHTML = `
      <strong>LAN bind:</strong> listening on ${escapeHtml(String(d.bind || ""))}:${escapeHtml(String(d.port || ""))}.
      Admin routes require <code>X-Admin-Token</code>. Do not expose this host to the public internet.
      <div class="row">
        <label>Admin token <input id="banner-token" type="password" autocomplete="off" /></label>
        <button class="btn" id="banner-save">Save token</button>
      </div>`;
    const input = document.getElementById("banner-token");
    input.value = localStorage.getItem(TOKEN_KEY) || "";
    document.getElementById("banner-save").addEventListener("click", () => {
      localStorage.setItem(TOKEN_KEY, input.value);
      toast("Admin token saved for this browser", "ok");
      render(document.querySelector("nav button.active")?.dataset.page || "accounts");
    });
  } catch {
    el.hidden = true;
  }
}

function accountsPage(root) {
  root.innerHTML = `
    <section class="card">
      <h2>Add account</h2>
      <p class="muted">Presets for local servers, API keys, subscription OAuth, and OpenAI-compat hosts. Gemini API keys use Google AI Studio’s official OpenAI-compat endpoint; Gemini/Antigravity subscription OAuth is a separate Cloud Code path. Subscription OAuth may violate provider ToS and can ban the account; PeaProxy authors are not liable. Prefer API keys.</p>
      <div class="row">
        <label>Preset
          <select id="preset"></select>
        </label>
        <label>ID <input id="acc-id" placeholder="ollama-local" /></label>
        <label>Base URL <input id="acc-url" size="36" /></label>
        <label>API key <input id="acc-key" type="password" autocomplete="off" /></label>
        <label>Tier
          <select id="acc-tier">
            <option>local</option>
            <option>free</option>
            <option>freemium</option>
            <option>paid</option>
          </select>
        </label>
        <button class="btn primary" id="acc-add">Add</button>
      </div>
      <p class="warn" id="zen-warn" hidden></p>
      <p class="muted" id="preset-note" hidden></p>
    </section>
    <section class="card">
      <h2>Accounts</h2>
      <div id="acc-list">loading…</div>
    </section>`;
  let presets = [];
  const sel = document.getElementById("preset");
  const applyPreset = () => {
    const p = presets.find((x) => x.id === sel.value) || presets[0];
    if (!p) return;
    document.getElementById("acc-id").value = p.id;
    document.getElementById("acc-url").value = p.baseURL || "";
    document.getElementById("acc-tier").value = p.tier || "paid";
    const warn = document.getElementById("zen-warn");
    warn.hidden = !p.warn;
    warn.textContent = p.warn || "";
    const note = document.getElementById("preset-note");
    note.hidden = !p.note;
    note.textContent = p.note || "";
  };
  document.getElementById("preset").addEventListener("change", applyPreset);
  document.getElementById("acc-add").addEventListener("click", async () => {
    const p = presets.find((x) => x.id === sel.value);
    if (!p) {
      toast("No preset selected");
      return;
    }
    try {
      const body = {
        id: document.getElementById("acc-id").value,
        adapter: p.adapter,
        baseURL: document.getElementById("acc-url").value,
        apiKey: document.getElementById("acc-key").value,
        tier: document.getElementById("acc-tier").value,
      };
      if (!body.apiKey && p.envKey) body.apiKeyEnv = p.envKey;
      await sendJSON("/admin/accounts", "POST", body);
      toast("Account added", "ok");
      loadAccounts();
    } catch (err) {
      toast(err.message);
    }
  });
  async function loadAccounts() {
    const host = document.getElementById("acc-list");
    try {
      const data = await getJSON("/admin/accounts");
      const accounts = data.accounts || [];
      if (!accounts.length) {
        host.innerHTML = emptyState("No accounts yet", "Add Ollama, LM Studio, llama.cpp, vLLM, Ollama Cloud, or an API key using a preset above.");
        return;
      }
      const rows = accounts
        .map((a) => {
          const oauth = isOAuthAdapter(a.adapter);
          const login = oauth
            ? `<button class="btn" data-oauth="${escapeHtml(a.id)}">OAuth login</button>
               <button class="btn" data-cli="${escapeHtml(a.adapter)}">Copy CLI</button>`
            : "";
          return `<tr>
        <td>${escapeHtml(a.id)}</td><td>${escapeHtml(a.adapter)}</td>
        <td><span class="pill">${escapeHtml(a.tier || "")}</span></td>
        <td>${escapeHtml(a.baseURL || "")}</td>
        <td>${escapeHtml(a.status || "")}</td>
        <td>${login}<button class="btn danger" data-del="${escapeHtml(a.id)}">Remove</button></td>
      </tr>`;
        })
        .join("");
      host.innerHTML = `<table>
      <thead><tr><th>ID</th><th>Adapter</th><th>Tier</th><th>Base URL</th><th>Status</th><th></th></tr></thead>
      <tbody>${rows}</tbody></table>`;
      document.querySelectorAll("[data-del]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          try {
            await sendJSON("/admin/accounts/" + encodeURIComponent(btn.dataset.del), "DELETE");
            toast("Account removed", "ok");
            loadAccounts();
          } catch (err) {
            toast(err.message);
          }
        });
      });
      document.querySelectorAll("[data-cli]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          const cmd = "peaproxy auth login --provider " + oauthCLIProvider(btn.dataset.cli);
          try {
            await navigator.clipboard.writeText(cmd);
            toast("Copied " + cmd, "ok");
          } catch (err) {
            toast(cmd);
          }
        });
      });
      document.querySelectorAll("[data-oauth]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          try {
            const started = await sendJSON("/admin/oauth/start", "POST", { id: btn.dataset.oauth });
            if (started.warning) toast(started.warning.slice(0, 180));
            if (started.loginURL) window.open(started.loginURL, "_blank", "noopener");
            toast("Complete login in the browser, or run: " + (started.cli || "peaproxy auth login"), "ok");
            const id = btn.dataset.oauth;
            const poll = async () => {
              const st = await getJSON("/admin/oauth/status?id=" + encodeURIComponent(id));
              if (st.status === "complete") {
                toast("OAuth login saved", "ok");
                loadAccounts();
                return;
              }
              if (st.status === "error") {
                toast(st.error || "OAuth failed");
                return;
              }
              setTimeout(poll, 2000);
            };
            setTimeout(poll, 2000);
          } catch (err) {
            toast(err.message);
          }
        });
      });
    } catch (err) {
      host.innerHTML = emptyState("Could not load accounts", err.message);
      toast(err.message);
    }
  }
  (async () => {
    try {
      const data = await getJSON("/admin/presets");
      presets = data.presets || [];
      sel.innerHTML = presets
        .map((p) => `<option value="${escapeHtml(p.id)}">${escapeHtml(p.label || p.id)}</option>`)
        .join("");
      applyPreset();
    } catch (err) {
      sel.innerHTML = `<option>unavailable</option>`;
      toast(err.message);
    }
    loadAccounts();
  })();
}

function catalogPage(root) {
  root.innerHTML = `
    <section class="card">
      <h2>Catalog</h2>
      <p class="muted">Live ListModels is the source of truth. Hide removes a model from <code>/v1/models</code> only — it stays routable if a client names the id (CPA #5995). Pin and rename are local overlays; they never change the live id used for routing.</p>
      <div class="filters" id="filters">
        <button data-filter="all">All</button>
        <button data-filter="free">Free</button>
        <button data-filter="paid">Paid</button>
        <button data-filter="local">Local</button>
        <button data-filter="subscription_oauth">Subscription OAuth</button>
      </div>
      <button class="btn" id="refresh">Refresh live list</button>
      <div id="cat-table">loading…</div>
    </section>`;
  let filter = localStorage.getItem(FILTER_KEY) || "all";
  const markFilter = () => {
    document.querySelectorAll("#filters button").forEach((b) => {
      b.classList.toggle("active", b.dataset.filter === filter);
    });
  };
  markFilter();
  async function load() {
    const host = document.getElementById("cat-table");
    try {
      const data = await getJSON("/admin/catalog?filter=" + encodeURIComponent(filter));
      const models = data.models || [];
      if (!models.length) {
        host.innerHTML = emptyState("No models in this filter", "Add an account on Accounts, or pick All / Local after Ollama, LM Studio, llama.cpp, or vLLM is running.");
        return;
      }
      const rows = models
        .map((m) => {
          const hideLabel = m.hidden ? "Unhide" : "Hide from /v1/models";
          const pinLabel = m.pinned ? "Unpin" : "Pin";
          const shown = m.displayName || m.id;
          return `<tr>
          <td>${m.pinned ? `<span class="pill accent">pin</span> ` : ""}${escapeHtml(shown)}
            <div class="muted">${escapeHtml(m.id)}</div>
            <div class="row tight">
              <input data-rename-input="${escapeHtml(m.id)}" value="${escapeHtml(m.displayName || "")}" placeholder="Display name" size="18" />
              <button class="btn" data-rename="${escapeHtml(m.id)}">Save name</button>
            </div>
          </td>
          <td><span class="pill">${escapeHtml(m.tier)}</span></td>
          <td>${escapeHtml(m.provider)}</td>
          <td>${escapeHtml(m.accountId || "")}</td>
          <td>${escapeHtml((m.modalities || []).join(", "))}</td>
          <td>${m.exposed ? "listed" : "hidden"} / ${m.routable ? "routable" : "blocked"}</td>
          <td>${m.privacyNote ? `<span class="warn">${escapeHtml(m.privacyNote)}</span>` : ""}</td>
          <td>
            <button class="btn" data-pin="${escapeHtml(m.id)}" data-on="${m.pinned ? "0" : "1"}">${pinLabel}</button>
            <button class="btn" data-hide="${escapeHtml(m.id)}" data-on="${m.hidden ? "0" : "1"}">${hideLabel}</button>
          </td>
        </tr>`;
        })
        .join("");
      host.innerHTML = `<table>
      <thead><tr><th>Model</th><th>Tier</th><th>Provider</th><th>Account</th><th>Modalities</th><th>List / route</th><th>Privacy</th><th></th></tr></thead>
      <tbody>${rows}</tbody></table>`;
      document.querySelectorAll("[data-hide]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          try {
            await sendJSON("/admin/hide", "POST", {
              kind: "model",
              id: btn.dataset.hide,
              hidden: btn.dataset.on === "1",
            });
            load();
          } catch (err) {
            toast(err.message);
          }
        });
      });
      document.querySelectorAll("[data-pin]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          try {
            await sendJSON("/admin/catalog/overlay", "POST", {
              id: btn.dataset.pin,
              pinned: btn.dataset.on === "1",
            });
            load();
          } catch (err) {
            toast(err.message);
          }
        });
      });
      document.querySelectorAll("[data-rename]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          const input = btn.parentElement.querySelector("input");
          try {
            await sendJSON("/admin/catalog/overlay", "POST", {
              id: btn.dataset.rename,
              displayName: input ? input.value : "",
            });
            toast("Display name saved", "ok");
            load();
          } catch (err) {
            toast(err.message);
          }
        });
      });
    } catch (err) {
      host.innerHTML = emptyState("Could not load catalog", err.message);
      toast(err.message);
    }
  }
  document.getElementById("filters").addEventListener("click", (e) => {
    const btn = e.target.closest("button[data-filter]");
    if (!btn) return;
    filter = btn.dataset.filter;
    localStorage.setItem(FILTER_KEY, filter);
    markFilter();
    load();
  });
  document.getElementById("refresh").addEventListener("click", async () => {
    try {
      await sendJSON("/admin/catalog/refresh", "POST");
      toast("Catalog refreshed", "ok");
      load();
    } catch (err) {
      toast(err.message);
    }
  });
  load();
}

function showcasePage(root) {
  root.innerHTML = `
    <section class="card">
      <h2>Showcase</h2>
      <p class="muted">Try a live chat. Usage is persisted next to the config file. Models with <code>image_in</code> accept an image URL or upload (OpenAI content parts). Models tagged <code>image_out</code> from the live catalog show a gated notice — PeaProxy does not proxy <code>/v1/images/generations</code> and will not fake a drawing as chat.</p>
      <div class="row">
        <label>Model
          <select id="show-model"></select>
        </label>
        <button class="btn primary" id="show-send">Send</button>
      </div>
      <label>Prompt <textarea id="show-prompt">Say hello in one short sentence.</textarea></label>
      <div id="show-vision" hidden>
        <div class="row">
          <label>Image URL <input id="show-image-url" size="42" placeholder="https://… or data:image/png;base64,…" /></label>
          <label>Upload <input id="show-file" type="file" accept="image/*" /></label>
        </div>
      </div>
      <div id="show-image-out" class="warn" hidden>
        <p><strong>Image generation not yet.</strong> This model is tagged <code>image_out</code> from live capabilities. There is no generation button on purpose — sending chat would fake it.</p>
      </div>
      <pre id="show-out">Pick a model and send.</pre>
    </section>
    <section class="card">
      <h2>Recent usage</h2>
      <pre id="show-usage">loading…</pre>
    </section>`;
  let models = [];
  const sel = document.getElementById("show-model");
  const toggleVision = () => {
    const m = models.find((x) => x.id === sel.value);
    const mods = m?.modalities || [];
    const hasIn = mods.includes("image_in");
    const hasOut = mods.includes("image_out");
    document.getElementById("show-vision").hidden = !hasIn;
    document.getElementById("show-image-out").hidden = !hasOut;
  };
  (async () => {
    try {
      const data = await getJSON("/admin/catalog?filter=all");
      models = (data.models || []).filter((m) => m.routable && m.status !== "auth_error");
      if (!models.length) {
        document.getElementById("show-out").textContent = "";
        document.getElementById("show-out").insertAdjacentHTML(
          "beforebegin",
          emptyState("Nothing to try yet", "Add a working account, then refresh the catalog.")
        );
        document.getElementById("show-send").disabled = true;
      }
      models.forEach((m) => {
        const opt = document.createElement("option");
        opt.value = m.id;
        opt.textContent = `${m.displayName ? m.displayName + " · " : ""}${m.id} (${m.tier})`;
        sel.appendChild(opt);
      });
      sel.addEventListener("change", toggleVision);
      toggleVision();
    } catch (err) {
      toast(err.message);
      document.getElementById("show-out").textContent = err.message;
    }
    try {
      document.getElementById("show-usage").textContent = JSON.stringify(await getJSON("/admin/usage"), null, 2);
    } catch (err) {
      document.getElementById("show-usage").textContent = err.message;
    }
  })();
  document.getElementById("show-send").addEventListener("click", async () => {
    const out = document.getElementById("show-out");
    out.textContent = "sending…";
    try {
      let imageUrl = document.getElementById("show-image-url").value.trim();
      const file = document.getElementById("show-file").files[0];
      if (file) {
        imageUrl = await fileToDataURL(file);
      }
      const payload = {
        model: sel.value,
        prompt: document.getElementById("show-prompt").value,
      };
      if (!document.getElementById("show-vision").hidden && imageUrl) {
        payload.imageUrl = imageUrl;
      }
      const data = await sendJSON("/admin/showcase", "POST", payload);
      out.textContent = data.content || JSON.stringify(data, null, 2);
      document.getElementById("show-usage").textContent = JSON.stringify(await getJSON("/admin/usage"), null, 2);
    } catch (err) {
      out.textContent = err.message;
      toast(err.message);
    }
  });
}

function clientsPage(root) {
  root.innerHTML = `<section class="card"><h2>Clients</h2><p class="muted">OpenCode and Claude Code use <strong>different</strong> Anthropic base URLs. Pi cloak defaults are <strong>off</strong>. Codex uses <code>/v1/responses</code>. Amp uses a Custom URL (not <code>amp.url</code>). Use <code>peaproxy clients verify &lt;name&gt; --chat</code> against a running serve.</p><div id="cli-list">loading…</div></section>`;
  getJSON("/admin/clients")
    .then((data) => {
      const list = data.clients || [];
      if (!list.length) {
        document.getElementById("cli-list").innerHTML = emptyState("No client presets", "This build should ship Cursor, Claude Code, OpenCode, Pi, Codex, Continue, Cline, and Amp.");
        return;
      }
      document.getElementById("cli-list").innerHTML = list
        .map(
          (c) => `<div class="card snippet">
        <h2>${escapeHtml(c.name)}</h2>
        <p class="muted">${escapeHtml(c.notes)}</p>
        <p><code>${escapeHtml(c.baseURL)}</code> · cloak ${escapeHtml(c.cloak || "off")}</p>
        <button class="btn" data-copy>Copy</button>
        <pre>${escapeHtml(c.snippet)}</pre>
      </div>`
        )
        .join("");
      document.querySelectorAll("[data-copy]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          const pre = btn.parentElement.querySelector("pre");
          try {
            await navigator.clipboard.writeText(pre.textContent);
            btn.textContent = "Copied";
            toast("Copied preset", "ok");
          } catch (err) {
            toast(err.message);
          }
        });
      });
    })
    .catch((err) => {
      document.getElementById("cli-list").innerHTML = emptyState("Could not load clients", err.message);
      toast(err.message);
    });
}

function healthPage(root) {
  root.innerHTML = `<section class="card"><h2>Adapter health</h2>
      <p class="muted">Last live <code>ListModels</code> (or <code>Validate</code> after Probe). Cooldown overlays 429/401 skip windows without disabling the account.</p>
      <button class="btn" id="probe">Probe adapters</button>
      <div id="ah">loading…</div>
    </section>
    <section class="card"><h2>Account cooldowns</h2>
      <p class="muted">After HTTP 429 or 401 the account is skipped for 30s. Cooled accounts are not re-hit until the window expires (avoids cooldown storms). Round-robin tries the next hot key for the same model.</p>
      <div id="cd">loading…</div>
    </section>
    <section class="card"><h2>Gateway</h2><pre id="h">loading…</pre></section>
    <section class="card"><h2>Usage</h2><pre id="u">loading…</pre></section>`;
  const fmtRemaining = (ms) => {
    const n = Number(ms) || 0;
    if (n <= 0) return "expired";
    const sec = Math.ceil(n / 1000);
    return sec + "s left";
  };
  const loadHealth = async () => {
    const d = await getJSON("/admin/health");
    document.getElementById("h").textContent = JSON.stringify(d, null, 2);
    const adapters = d.adapterHealth || [];
    document.getElementById("ah").innerHTML = adapters.length
      ? `<table>
      <thead><tr><th>Account</th><th>Adapter</th><th>Status</th><th>Models</th><th>Latency</th><th>Error</th></tr></thead>
      <tbody>${adapters
        .map(
          (a) => `<tr>
        <td>${escapeHtml(a.accountId)}</td>
        <td>${escapeHtml(a.adapter)}</td>
        <td><span class="pill ${a.status === "ok" ? "ok" : ""}">${escapeHtml(a.status)}</span></td>
        <td>${escapeHtml(String(a.models ?? 0))}</td>
        <td>${escapeHtml(String(a.latencyMs ?? 0))}ms</td>
        <td>${a.error ? `<span class="warn">${escapeHtml(a.error)}</span>` : ""}</td>
      </tr>`
        )
        .join("")}</tbody></table>`
      : emptyState("No adapters probed yet", "Add an account, then refresh the catalog or click Probe.");
    const rows = (d.cooldowns || [])
      .map(
        (c) =>
          `<tr><td>${escapeHtml(c.accountId)}</td><td>${escapeHtml(c.reason)}</td><td>${escapeHtml(fmtRemaining(c.remainingMs))}</td><td class="muted">${escapeHtml(c.until || "")}</td></tr>`
      )
      .join("");
    document.getElementById("cd").innerHTML = rows
      ? `<table>
      <thead><tr><th>Account</th><th>Reason</th><th>Remaining</th><th>Until</th></tr></thead>
      <tbody>${rows}</tbody></table>`
      : emptyState("No accounts in cooldown", "429/401 failover will show a skip window here.");
  };
  loadHealth().catch((err) => {
    document.getElementById("h").textContent = err.message;
    document.getElementById("ah").innerHTML = emptyState("Health unavailable", err.message);
    document.getElementById("cd").innerHTML = emptyState("Health unavailable", err.message);
    toast(err.message);
  });
  document.getElementById("probe").addEventListener("click", async () => {
    try {
      await sendJSON("/admin/health/probe", "POST");
      toast("Adapters probed", "ok");
      await loadHealth();
    } catch (err) {
      toast(err.message);
    }
  });
  getJSON("/admin/usage")
    .then((d) => (document.getElementById("u").textContent = JSON.stringify(d, null, 2)))
    .catch((err) => {
      document.getElementById("u").textContent = err.message;
    });
}

function requestsPage(root) {
  root.innerHTML = `<section class="card">
      <h2>Request log</h2>
      <p class="muted">Opt-in inspector. Previews are redacted (tokens, API keys, JWTs, PEM). The JSONL file is mode <code>0600</code> and rotates at 1MiB.</p>
      <label class="row"><input type="checkbox" id="reqlog-page" /> Enable redacted <code>requests.log</code></label>
      <p class="muted" id="req-path"></p>
      <button class="btn" id="req-refresh">Refresh</button>
      <div id="req-table">loading…</div>
    </section>`;
  const box = document.getElementById("reqlog-page");
  let timer = 0;
  const stop = () => {
    if (timer) {
      clearInterval(timer);
      timer = 0;
    }
  };
  const renderEvents = (d) => {
    document.getElementById("req-path").textContent = d.path ? "File: " + d.path : "No log file (enable to start writing).";
    box.checked = !!d.enabled;
    const host = document.getElementById("req-table");
    if (!d.enabled) {
      host.innerHTML = emptyState("Request log is off", "Enable the checkbox to persist a redacted JSONL inspector next to the config file.");
      return;
    }
    const events = d.events || [];
    if (!events.length) {
      host.innerHTML = emptyState("No requests yet", "Send a chat, Showcase prompt, or /v1/messages call while the log is enabled.");
      return;
    }
    host.innerHTML = `<table>
      <thead><tr><th>Time</th><th>Path</th><th>Model</th><th>Account</th><th>Status</th><th>ms</th><th>Preview</th></tr></thead>
      <tbody>${events
        .map((e) => {
          const t = e.time ? new Date(e.time).toLocaleTimeString() : "";
          return `<tr>
            <td>${escapeHtml(t)}</td>
            <td><code>${escapeHtml(e.path || e.protocol || "")}</code></td>
            <td>${escapeHtml(e.model || "")}</td>
            <td>${escapeHtml(e.accountId || "")}</td>
            <td>${escapeHtml(String(e.status || ""))}${e.error ? ` <span class="warn">${escapeHtml(e.error)}</span>` : ""}</td>
            <td>${escapeHtml(String(e.durationMs || 0))}</td>
            <td class="preview">${escapeHtml(e.preview || "")}</td>
          </tr>`;
        })
        .join("")}</tbody></table>`;
  };
  const load = async () => {
    try {
      renderEvents(await getJSON("/admin/requests"));
    } catch (err) {
      document.getElementById("req-table").innerHTML = emptyState("Could not load request log", err.message);
      toast(err.message);
    }
  };
  box.addEventListener("change", async () => {
    try {
      await sendJSON("/admin/settings", "POST", { requestLog: box.checked });
      toast(box.checked ? "Request log enabled" : "Request log disabled", "ok");
      await load();
    } catch (err) {
      toast(err.message);
      box.checked = !box.checked;
    }
  });
  document.getElementById("req-refresh").addEventListener("click", load);
  load().then(() => {
    stop();
    timer = setInterval(load, 4000);
  });
  const obs = new MutationObserver(() => {
    if (!document.getElementById("req-table")) stop();
  });
  obs.observe(document.getElementById("page"), { childList: true });
}

function settingsPage(root) {
  root.innerHTML = `<section class="card"><h2>Settings</h2>
    <p class="muted">Bind stays loopback unless <code>--allow-lan</code> (or <code>allowNonLoopback</code>) <strong>and</strong> a non-empty admin token. Hide does not block routing unless <code>hide.blockRouting</code> is true.</p>
    <div id="lan-settings" class="warn" hidden></div>
    <label>Admin token (sent as <code>X-Admin-Token</code> from this UI)
      <input id="ui-token" type="password" autocomplete="off" />
    </label>
    <button class="btn" id="save-token">Save token in this browser</button>
    <label class="row"><input type="checkbox" id="reqlog" /> Opt-in redacted request log (<code>requests.log</code>)</label>
    <pre id="s">loading…</pre></section>`;
  const box = document.getElementById("reqlog");
  document.getElementById("ui-token").value = localStorage.getItem(TOKEN_KEY) || "";
  document.getElementById("save-token").addEventListener("click", () => {
    localStorage.setItem(TOKEN_KEY, document.getElementById("ui-token").value);
    toast("Admin token saved for this browser", "ok");
  });
  getJSON("/admin/settings")
    .then((d) => {
      document.getElementById("s").textContent = JSON.stringify(d, null, 2);
      box.checked = !!d.requestLog;
      const lan = document.getElementById("lan-settings");
      if (d.lanWarning) {
        lan.hidden = false;
        lan.textContent = "This process is bound off loopback. Keep the admin token private. /healthz stays public; /admin requires the token.";
      }
    })
    .catch((err) => {
      document.getElementById("s").textContent = err.message;
      toast(err.message);
    });
  box.addEventListener("change", async () => {
    try {
      const d = await sendJSON("/admin/settings", "POST", { requestLog: box.checked });
      document.getElementById("s").textContent = JSON.stringify(d, null, 2);
      toast("Settings saved", "ok");
    } catch (err) {
      toast(err.message);
    }
  });
}

function fileToDataURL(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result || ""));
    reader.onerror = () => reject(reader.error || new Error("read failed"));
    reader.readAsDataURL(file);
  });
}

function isOAuthAdapter(adapter) {
  return adapter === "antigravity" || String(adapter || "").endsWith("_oauth");
}

function oauthCLIProvider(adapter) {
  switch (adapter) {
    case "openai_oauth":
      return "openai";
    case "anthropic_oauth":
      return "anthropic";
    case "antigravity":
    case "gemini_oauth":
      return "gemini";
    case "xai_oauth":
      return "xai";
    case "kimi_oauth":
      return "kimi";
    case "kimi_ai_oauth":
      return "kimi-ai";
    case "meta_oauth":
      return "meta";
    case "qwen_oauth":
      return "qwen";
    default:
      return adapter;
  }
}

function escapeHtml(s) {
  return String(s ?? "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

document.querySelector("nav").addEventListener("click", (e) => {
  const btn = e.target.closest("button[data-page]");
  if (btn) render(btn.dataset.page);
});

refreshLanBanner();
render("accounts");
