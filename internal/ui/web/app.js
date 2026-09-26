const pages = {
  accounts: accountsPage,
  catalog: catalogPage,
  showcase: showcasePage,
  clients: clientsPage,
  health: healthPage,
  settings: settingsPage,
};

function render(name) {
  const fn = pages[name];
  if (!fn) return;
  document.getElementById("page").innerHTML = "";
  document.querySelectorAll("nav button").forEach((b) => {
    b.classList.toggle("active", b.dataset.page === name);
  });
  fn(document.getElementById("page"));
}

async function getJSON(url) {
  const res = await fetch(url);
  const text = await res.text();
  try {
    return JSON.parse(text);
  } catch {
    throw new Error(text || res.statusText);
  }
}

async function sendJSON(url, method, body) {
  const res = await fetch(url, {
    method,
    headers: { "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function accountsPage(root) {
  root.innerHTML = `
    <section class="card">
      <h2>Add account</h2>
      <p class="muted">Ollama and generic OpenAI-compat work now. OpenCode Zen is a named free adapter. Subscription OAuth is still a stub.</p>
      <div class="row">
        <label>Preset
          <select id="preset">
            <option value="ollama">Ollama (local)</option>
            <option value="lmstudio">LM Studio (local)</option>
            <option value="openai_compat">Custom OpenAI-compat</option>
            <option value="openrouter">OpenRouter</option>
            <option value="opencode_zen">OpenCode Zen free</option>
          </select>
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
      <p class="warn" id="zen-warn" hidden>Zen free models may train on prompts (Nemotron, Big Pickle, MiMo, Muse). Prefer an official API key from opencode.ai.</p>
    </section>
    <section class="card">
      <h2>Accounts</h2>
      <div id="acc-list">loading…</div>
    </section>`;
  const presets = {
    ollama: { id: "ollama-local", adapter: "ollama", url: "http://127.0.0.1:11434/v1", tier: "local" },
    lmstudio: { id: "lmstudio-local", adapter: "openai_compat", url: "http://127.0.0.1:1234/v1", tier: "local" },
    openai_compat: { id: "custom", adapter: "openai_compat", url: "https://api.example.com/v1", tier: "paid" },
    openrouter: { id: "openrouter", adapter: "openai_compat", url: "https://openrouter.ai/api/v1", tier: "free" },
    opencode_zen: { id: "opencode-zen", adapter: "opencode_zen", url: "https://opencode.ai/zen/v1", tier: "free" },
  };
  const applyPreset = () => {
    const p = presets[document.getElementById("preset").value];
    document.getElementById("acc-id").value = p.id;
    document.getElementById("acc-url").value = p.url;
    document.getElementById("acc-tier").value = p.tier;
    document.getElementById("zen-warn").hidden = p.adapter !== "opencode_zen";
  };
  document.getElementById("preset").addEventListener("change", applyPreset);
  applyPreset();
  document.getElementById("acc-add").addEventListener("click", async () => {
    const key = document.getElementById("preset").value;
    const p = presets[key];
    try {
      await sendJSON("/admin/accounts", "POST", {
        id: document.getElementById("acc-id").value,
        adapter: p.adapter,
        baseURL: document.getElementById("acc-url").value,
        apiKey: document.getElementById("acc-key").value,
        tier: document.getElementById("acc-tier").value,
      });
      loadAccounts();
    } catch (err) {
      alert(err.message);
    }
  });
  async function loadAccounts() {
    const data = await getJSON("/admin/accounts");
    const rows = (data.accounts || [])
      .map(
        (a) => `<tr>
        <td>${escapeHtml(a.id)}</td><td>${escapeHtml(a.adapter)}</td>
        <td><span class="pill">${escapeHtml(a.tier || "")}</span></td>
        <td>${escapeHtml(a.baseURL || "")}</td>
        <td>${a.status}</td>
        <td><button class="btn danger" data-del="${escapeHtml(a.id)}">Remove</button></td>
      </tr>`
      )
      .join("");
    document.getElementById("acc-list").innerHTML = `<table>
      <thead><tr><th>ID</th><th>Adapter</th><th>Tier</th><th>Base URL</th><th>Status</th><th></th></tr></thead>
      <tbody>${rows || `<tr><td colspan="6" class="muted">No accounts</td></tr>`}</tbody></table>`;
    document.querySelectorAll("[data-del]").forEach((btn) => {
      btn.addEventListener("click", async () => {
        await sendJSON("/admin/accounts/" + encodeURIComponent(btn.dataset.del), "DELETE");
        loadAccounts();
      });
    });
  }
  loadAccounts();
}

function catalogPage(root) {
  root.innerHTML = `
    <section class="card">
      <h2>Catalog</h2>
      <p class="muted">Live ListModels. Hide removes a model from <code>/v1/models</code> only — it stays routable if a client names the id (CPA #5995).</p>
      <div class="filters" id="filters">
        <button data-filter="all" class="active">All</button>
        <button data-filter="free">Free</button>
        <button data-filter="paid">Paid</button>
        <button data-filter="local">Local</button>
        <button data-filter="subscription_oauth">Subscription OAuth</button>
      </div>
      <button class="btn" id="refresh">Refresh live list</button>
      <div id="cat-table">loading…</div>
    </section>`;
  let filter = "all";
  async function load() {
    const data = await getJSON("/admin/catalog?filter=" + encodeURIComponent(filter));
    const rows = (data.models || [])
      .map((m) => {
        const hideLabel = m.hidden ? "Unhide" : "Hide from /v1/models";
        return `<tr>
          <td>${escapeHtml(m.id)}</td>
          <td><span class="pill">${escapeHtml(m.tier)}</span></td>
          <td>${escapeHtml(m.provider)}</td>
          <td>${escapeHtml(m.accountId || "")}</td>
          <td>${m.exposed ? "listed" : "hidden"} / ${m.routable ? "routable" : "blocked"}</td>
          <td>${m.privacyNote ? `<span class="warn">${escapeHtml(m.privacyNote)}</span>` : ""}</td>
          <td><button class="btn" data-hide="${escapeHtml(m.id)}" data-on="${m.hidden ? "0" : "1"}">${hideLabel}</button></td>
        </tr>`;
      })
      .join("");
    document.getElementById("cat-table").innerHTML = `<table>
      <thead><tr><th>Model</th><th>Tier</th><th>Provider</th><th>Account</th><th>List / route</th><th>Privacy</th><th></th></tr></thead>
      <tbody>${rows || `<tr><td colspan="7" class="muted">No models — add Ollama or an API key on Accounts.</td></tr>`}</tbody></table>`;
    document.querySelectorAll("[data-hide]").forEach((btn) => {
      btn.addEventListener("click", async () => {
        await sendJSON("/admin/hide", "POST", {
          kind: "model",
          id: btn.dataset.hide,
          hidden: btn.dataset.on === "1",
        });
        load();
      });
    });
  }
  document.getElementById("filters").addEventListener("click", (e) => {
    const btn = e.target.closest("button[data-filter]");
    if (!btn) return;
    filter = btn.dataset.filter;
    document.querySelectorAll("#filters button").forEach((b) => b.classList.toggle("active", b === btn));
    load();
  });
  document.getElementById("refresh").addEventListener("click", async () => {
    await sendJSON("/admin/catalog/refresh", "POST");
    load();
  });
  load();
}

function showcasePage(root) {
  root.innerHTML = `
    <section class="card">
      <h2>Showcase</h2>
      <p class="muted">Try a live chat. Usage is recorded in-memory (CPA dropped built-in usage — we kept it).</p>
      <div class="row">
        <label>Model
          <select id="show-model"></select>
        </label>
        <button class="btn primary" id="show-send">Send</button>
      </div>
      <label>Prompt <textarea id="show-prompt">Say hello in one short sentence.</textarea></label>
      <pre id="show-out">Pick a model and send.</pre>
    </section>
    <section class="card">
      <h2>Recent usage</h2>
      <pre id="show-usage">loading…</pre>
    </section>`;
  (async () => {
    const data = await getJSON("/admin/catalog?filter=all");
    const sel = document.getElementById("show-model");
    (data.models || [])
      .filter((m) => m.routable && m.status !== "auth_error")
      .forEach((m) => {
        const opt = document.createElement("option");
        opt.value = m.id;
        opt.textContent = `${m.id} (${m.tier})`;
        sel.appendChild(opt);
      });
    document.getElementById("show-usage").textContent = JSON.stringify(await getJSON("/admin/usage"), null, 2);
  })();
  document.getElementById("show-send").addEventListener("click", async () => {
    const out = document.getElementById("show-out");
    out.textContent = "sending…";
    try {
      const data = await sendJSON("/admin/showcase", "POST", {
        model: document.getElementById("show-model").value,
        prompt: document.getElementById("show-prompt").value,
      });
      out.textContent = data.content || JSON.stringify(data, null, 2);
      document.getElementById("show-usage").textContent = JSON.stringify(await getJSON("/admin/usage"), null, 2);
    } catch (err) {
      out.textContent = err.message;
    }
  });
}

function clientsPage(root) {
  root.innerHTML = `<section class="card"><h2>Clients</h2><p class="muted">OpenCode and Claude Code use <strong>different</strong> Anthropic base URLs.</p><div id="cli-list">loading…</div></section>`;
  getJSON("/admin/clients").then((data) => {
    document.getElementById("cli-list").innerHTML = (data.clients || [])
      .map(
        (c) => `<div class="card snippet">
        <h2>${escapeHtml(c.name)}</h2>
        <p class="muted">${escapeHtml(c.notes)}</p>
        <p><code>${escapeHtml(c.baseURL)}</code></p>
        <button class="btn" data-copy>Copy</button>
        <pre>${escapeHtml(c.snippet)}</pre>
      </div>`
      )
      .join("");
    document.querySelectorAll("[data-copy]").forEach((btn) => {
      btn.addEventListener("click", () => {
        const pre = btn.parentElement.querySelector("pre");
        navigator.clipboard.writeText(pre.textContent);
        btn.textContent = "Copied";
      });
    });
  });
}

function healthPage(root) {
  root.innerHTML = `<section class="card"><h2>Health</h2><pre id="h">loading…</pre></section>
    <section class="card"><h2>Usage</h2><pre id="u">loading…</pre></section>`;
  getJSON("/admin/health").then((d) => (document.getElementById("h").textContent = JSON.stringify(d, null, 2)));
  getJSON("/admin/usage").then((d) => (document.getElementById("u").textContent = JSON.stringify(d, null, 2)));
}

function settingsPage(root) {
  root.innerHTML = `<section class="card"><h2>Settings</h2>
    <p class="muted">Bind stays loopback unless <code>allowNonLoopback</code> + admin token. Hide does not block routing unless <code>hide.blockRouting</code> is true.</p>
    <pre id="s">loading…</pre></section>`;
  getJSON("/admin/settings").then((d) => (document.getElementById("s").textContent = JSON.stringify(d, null, 2)));
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

render("accounts");
