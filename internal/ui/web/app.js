const pages = {
  accounts: () => `
    <section class="card">
      <h2>Accounts</h2>
      <p class="muted">OAuth + API keys + local providers. OAuth login is a spike TODO — stubs only.</p>
      <pre id="accounts-json">loading…</pre>
    </section>`,
  catalog: () => `
    <section class="card">
      <h2>Catalog</h2>
      <p class="muted">Live model list. Filters hide providers/models from <code>/v1/models</code>.</p>
      <div class="filters" id="filters">
        <button data-filter="all" class="active">All</button>
        <button data-filter="free">Free</button>
        <button data-filter="paid">Paid</button>
        <button data-filter="local">Local</button>
        <button data-filter="subscription_oauth">Subscription OAuth</button>
      </div>
      <pre id="catalog-json">loading…</pre>
    </section>`,
  showcase: () => `
    <section class="card">
      <h2>Showcase</h2>
      <p class="muted">Per-provider text (and vision when <code>image_in</code>). Stub in v0 — spike will run a real example.</p>
    </section>`,
  clients: () => `
    <section class="card">
      <h2>Clients</h2>
      <p class="muted">Harness presets. Copy from CLI: <code>peaproxy clients show cursor</code></p>
      <pre id="clients-json">loading…</pre>
    </section>`,
  health: () => `
    <section class="card">
      <h2>Health</h2>
      <pre id="health-json">loading…</pre>
    </section>`,
  settings: () => `
    <section class="card">
      <h2>Settings</h2>
      <p class="muted">Bind stays loopback unless <code>allowNonLoopback</code> + admin token.</p>
      <pre id="settings-json">loading…</pre>
    </section>`,
};

const presets = {
  cursor: { baseURL: "http://127.0.0.1:8317/v1", auth: "optional until LAN bind" },
  "claude-code": { baseURL: "http://127.0.0.1:8317", note: "Anthropic /v1/messages — stub" },
  opencode: { baseURL: "http://127.0.0.1:8317/v1" },
  pi: { baseURL: "http://127.0.0.1:8317", note: "Pi cloak defaults documented in docs/HARNESS.md" },
  codex: { baseURL: "http://127.0.0.1:8317/v1", note: "Responses wire — TODO" },
};

function render(name) {
  document.getElementById("page").innerHTML = pages[name]();
  document.querySelectorAll("nav button").forEach((b) => {
    b.classList.toggle("active", b.dataset.page === name);
  });
  if (name === "health") loadJSON("/admin/health", "health-json");
  if (name === "accounts") loadJSON("/admin/accounts", "accounts-json");
  if (name === "settings") loadJSON("/admin/settings", "settings-json");
  if (name === "catalog") {
    loadCatalog("all");
    document.getElementById("filters").addEventListener("click", (e) => {
      const btn = e.target.closest("button[data-filter]");
      if (!btn) return;
      document.querySelectorAll("#filters button").forEach((b) => b.classList.toggle("active", b === btn));
      loadCatalog(btn.dataset.filter);
    });
  }
  if (name === "clients") {
    document.getElementById("clients-json").textContent = JSON.stringify(presets, null, 2);
  }
}

async function loadJSON(url, id) {
  const el = document.getElementById(id);
  try {
    const res = await fetch(url);
    el.textContent = JSON.stringify(await res.json(), null, 2);
  } catch (err) {
    el.textContent = String(err);
  }
}

function loadCatalog(filter) {
  loadJSON("/admin/catalog?filter=" + encodeURIComponent(filter), "catalog-json");
}

document.querySelector("nav").addEventListener("click", (e) => {
  const btn = e.target.closest("button[data-page]");
  if (btn) render(btn.dataset.page);
});

render("accounts");
