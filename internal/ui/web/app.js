const pages = {
  policy: policyPage,
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

// Wire routes return a shaped error object (OpenAI or Anthropic); /admin/*
// and the pre-routing middleware return a plain string. Accept both.
function errText(data, fallback) {
  const e = data && data.error;
  if (typeof e === "string") return e;
  if (e && typeof e === "object" && typeof e.message === "string") return e.message;
  return fallback;
}

// contextWindowText renders a provider-published context window. A dash means
// the provider published none, which is most of them -- OpenAI and Anthropic
// send nothing through /v1/models. PeaProxy does not guess one, because a
// harness would take the number at face value.
function contextWindowText(n) {
  if (!n || n <= 0) return "—";
  if (n >= 1e6) return `${+(n / 1e6).toFixed(2)}M`;
  if (n >= 1000) {
    const k = Math.round(n / 1000);
    return k >= 1000 ? "1M" : `${k}K`;
  }
  return String(n);
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
    throw new Error(errText(data, res.statusText || "request failed"));
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
  if (!res.ok) throw new Error(errText(data, res.statusText));
  return data;
}

function emptyState(title, detail) {
  return `<div class="empty"><strong>${escapeHtml(title)}</strong><br />${escapeHtml(detail)}</div>`;
}

function quotaByAccount(list) {
  const m = {};
  (list || []).forEach((q) => {
    if (q && q.accountId) m[q.accountId] = q;
  });
  return m;
}

function formatQuotaRemaining(q) {
  if (!q) return "";
  const parts = [];
  const add = (label, v) => {
    if (v !== null && v !== undefined) parts.push(label + ": " + v);
  };
  add("requests", q.remainingRequests);
  add("tokens", q.remainingTokens);
  add("requests/day", q.remainingRequestsDay);
  add("tokens/min", q.remainingTokensMinute);
  add("input tokens", q.remainingInputTokens);
  add("output tokens", q.remainingOutputTokens);
  add("credits", q.remainingCredits);
  if (q.creditsUnlimited) parts.push("credits: unlimited (provider)");
  return parts.join(" · ");
}

function quotaRemainingHTML(q, hint) {
  if (hint) return escapeHtml(hint);
  const remaining = formatQuotaRemaining(q);
  if (remaining) return escapeHtml(remaining);
  return `<span class="muted">not reported by provider</span>`;
}

function onboardingCard() {
  return `<div class="empty cta" id="onboarding">
    <strong>Getting started</strong>
    <p>Add a local runtime or an official API key, then point a coding tool at this gateway. Subscription OAuth may violate provider ToS and can ban the account; PeaProxy authors are not liable. Prefer API keys.</p>
    <div class="row">
      <button class="btn primary" type="button" data-pick="ollama-local">Ollama local</button>
      <button class="btn" type="button" data-pick="lmstudio-local">LM Studio</button>
      <button class="btn" type="button" data-pick="jan-local">Jan local</button>
      <button class="btn" type="button" data-pick="anthropic-key">Anthropic API key</button>
      <button class="btn" type="button" data-pick="openai-key">OpenAI API key</button>
      <button class="btn" type="button" data-pick="opencode-go">OpenCode Go key</button>
    </div>
    <p class="warn">OAuth is last-resort. Use only if you accept the ban risk.</p>
    <div class="row">
      <button class="btn" type="button" data-pick="anthropic-oauth">Claude OAuth</button>
      <button class="btn" type="button" data-pick="copilot-oauth">Copilot OAuth</button>
      <button class="btn" type="button" data-jump="clients">Then verify a client</button>
    </div>
  </div>`;
}

function bindOnboarding(root, pickPreset) {
  root.querySelectorAll("[data-pick]").forEach((btn) => {
    btn.addEventListener("click", () => pickPreset(btn.dataset.pick));
  });
  root.querySelectorAll("[data-jump]").forEach((btn) => {
    btn.addEventListener("click", () => render(btn.dataset.jump));
  });
}

function usageTables(data) {
  const providers = data.byProvider || [];
  const accounts = data.byAccount || [];
  const days = data.byDay || [];
  const recent = data.recent || [];
  const dayRows = days
    .map((d) => {
      const tokens =
        d.promptTokens || d.completionTokens
          ? String((d.promptTokens || 0) + (d.completionTokens || 0))
          : "—";
      let price = "—";
      // A day's cost is the provider's own figures where they exist plus
      // PeaProxy's estimates for the calls that published tokens and no cost.
      // The two are shown together with the estimated part marked, because a
      // total that silently mixes a bill with an estimate reads as a bill.
      const measured = d.costUSD != null || d.estimatedUSD != null;
      const pricedCalls = (d.costCalls || 0) + (d.estimatedCalls || 0);
      if (measured) {
        price = String((d.costUSD || 0) + (d.estimatedUSD || 0));
        if (d.estimatedUSD) {
          price += ` (incl. $${Number(d.estimatedUSD).toFixed(6)} estimated)`;
        }
        if (pricedCalls < (d.calls || 0)) {
          price += ` (${pricedCalls}/${d.calls} priced)`;
        }
      } else if (d.calls) {
        price = `not measurable (${d.calls} call${d.calls === 1 ? "" : "s"})`;
      }
      return `<tr>
        <td>${escapeHtml(d.day || "")}</td>
        <td>${escapeHtml(d.accountId || "")}</td>
        <td>${escapeHtml(d.provider || "")}</td>
        <td>${escapeHtml(String(d.calls || 0))}</td>
        <td>${escapeHtml(String(d.errors || 0))}</td>
        <td>${escapeHtml(tokens)}</td>
        <td>${escapeHtml(price)}</td>
      </tr>`;
    })
    .join("");
  const providerRows = providers
    .map(
      (p) => `<tr>
        <td>${escapeHtml(p.provider || "")}</td>
        <td>${escapeHtml(String(p.calls || 0))}</td>
        <td>${escapeHtml(String(p.errors || 0))}</td>
        <td>${escapeHtml(String(p.tokens || 0))}</td>
        <td>${escapeHtml(String(p.accounts || 0))}</td>
      </tr>`
    )
    .join("");
  const accountRows = accounts
    .map(
      (a) => `<tr>
        <td>${escapeHtml(a.accountId || "")}</td>
        <td>${escapeHtml(a.provider || "")}</td>
        <td>${escapeHtml(String(a.calls || 0))}</td>
        <td>${escapeHtml(String(a.errors || 0))}</td>
        <td>${escapeHtml(String(a.tokens || 0))}</td>
      </tr>`
    )
    .join("");
  const recentRows = recent
    .slice(0, 12)
    .map((e) => {
      const t = e.time ? new Date(e.time).toLocaleTimeString() : "";
      return `<tr>
        <td>${escapeHtml(t)}</td>
        <td>${escapeHtml(e.provider || "")}</td>
        <td>${escapeHtml(e.accountId || "")}</td>
        <td>${escapeHtml(e.model || "")}</td>
        <td>${escapeHtml(String(e.status || ""))}</td>
        <td class="preview">${escapeHtml(e.preview || e.error || "")}</td>
      </tr>`;
    })
    .join("");
  return `
    <h3>By day</h3>
    <p class="muted">UTC days, kept 90 days. Tokens and price count only calls that published them. A dash means the provider did not send that number.</p>
    ${
      dayRows
        ? `<table>
      <thead><tr><th>Day</th><th>Account</th><th>Provider</th><th>Calls</th><th>Errors</th><th>Tokens</th><th>Published price</th></tr></thead>
      <tbody>${dayRows}</tbody></table>`
        : emptyState("No daily usage yet", "Daily totals are kept after the 200 most recent calls roll off.")
    }
    <h3>By provider</h3>
    ${
      providerRows
        ? `<table>
      <thead><tr><th>Provider</th><th>Calls</th><th>Errors</th><th>Tokens</th><th>Accounts</th></tr></thead>
      <tbody>${providerRows}</tbody></table>`
        : emptyState("No provider usage yet", "Send a Showcase prompt or a client chat to see per-provider totals.")
    }
    <h3>By account</h3>
    ${
      accountRows
        ? `<table>
      <thead><tr><th>Account</th><th>Provider</th><th>Calls</th><th>Errors</th><th>Tokens</th></tr></thead>
      <tbody>${accountRows}</tbody></table>`
        : emptyState("No account usage yet", "Each configured account that serves a request appears here.")
    }
    <h3>Recent</h3>
    ${
      recentRows
        ? `<table>
      <thead><tr><th>Time</th><th>Provider</th><th>Account</th><th>Model</th><th>Status</th><th>Preview</th></tr></thead>
      <tbody>${recentRows}</tbody></table>`
        : emptyState("No recent calls", "Usage is always written to usage.json next to the config.")
    }
    <p class="muted">${escapeHtml(data.path || "usage.json not persisted in this process")}</p>`;
}

function fillModelSelect(sel, models) {
  const groups = {};
  models.forEach((m) => {
    const g = m.provider || "other";
    (groups[g] || (groups[g] = [])).push(m);
  });
  Object.keys(groups)
    .sort()
    .forEach((provider) => {
      const og = document.createElement("optgroup");
      og.label = provider;
      groups[provider].forEach((m) => {
        const opt = document.createElement("option");
        opt.value = m.id;
        opt.textContent = `${m.displayName ? m.displayName + " · " : ""}${m.id} (${m.tier})`;
        og.appendChild(opt);
      });
      sel.appendChild(og);
    });
}

async function toggleRequestLog(box) {
  try {
    const d = await sendJSON("/admin/settings", "POST", { requestLog: box.checked });
    toast(box.checked ? "Request log enabled" : "Request log disabled", "ok");
    return d;
  } catch (err) {
    toast(err.message);
    box.checked = !box.checked;
    return null;
  }
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

// Presets arrive grouped from the server. Rendering optgroups keeps a list of
// forty-odd providers navigable instead of a single undifferentiated column.
// A preset with no group still renders, so a server that predates grouping
// does not produce an empty dropdown.
function presetOptions(presets) {
  const order = [];
  const byGroup = new Map();
  for (const p of presets) {
    const g = p.group || "";
    if (!byGroup.has(g)) {
      byGroup.set(g, []);
      order.push(g);
    }
    byGroup.get(g).push(p);
  }
  return order
    .map((g) => {
      const opts = byGroup
        .get(g)
        .map((p) => {
          const mark = p.unverified ? " (not verified)" : "";
          const label = (p.label || p.id) + mark;
          return `<option value="${escapeHtml(p.id)}">${escapeHtml(label)}</option>`;
        })
        .join("");
      return g
        ? `<optgroup label="${escapeHtml(g)}">${opts}</optgroup>`
        : opts;
    })
    .join("");
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
        <label id="acc-account-wrap" hidden>Account ID
          <input id="acc-account-id" autocomplete="off" placeholder="CLOUDFLARE_ACCOUNT_ID" />
        </label>
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
      <p class="muted" id="preset-env" hidden></p>
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
    const accountWrap = document.getElementById("acc-account-wrap");
    const accountInput = document.getElementById("acc-account-id");
    accountWrap.hidden = !p.accountIDEnv && !p.urlPlaceholder;
    accountInput.value = "";
    accountInput.placeholder = p.accountIDEnv || "account id";
    const envHint = document.getElementById("preset-env");
    const envBits = [];
    if (p.envKey) {
      envBits.push("API key env `" + p.envKey + "` " + (p.envKeySet ? "(set in this process)" : "(not set — paste a key or export the var)"));
    }
    if (p.accountIDEnv) {
      envBits.push("Account ID env `" + p.accountIDEnv + "` " + (p.accountIDEnvSet ? "(set in this process — Add will fill the URL)" : "(not set — paste the id into Account ID)"));
    }
    envHint.hidden = envBits.length === 0;
    envHint.textContent = envBits.join(". ");
    const warn = document.getElementById("zen-warn");
    warn.hidden = !p.warn;
    warn.textContent = p.warn || "";
    const note = document.getElementById("preset-note");
    note.hidden = !p.note;
    note.textContent = p.note || "";
  };
  const fillAccountID = () => {
    const p = presets.find((x) => x.id === sel.value);
    const placeholder = p?.urlPlaceholder;
    if (!placeholder) return;
    const id = document.getElementById("acc-account-id").value.trim();
    let url = document.getElementById("acc-url").value || p.baseURL || "";
    if (!url.includes(placeholder) && p.baseURL) url = p.baseURL;
    if (id) url = url.split(placeholder).join(id);
    document.getElementById("acc-url").value = url;
  };
  const pickPreset = (id) => {
    if (![...sel.options].some((o) => o.value === id)) {
      toast("Preset unavailable: " + id);
      return;
    }
    sel.value = id;
    applyPreset();
    document.getElementById("acc-id")?.focus();
    document.querySelector("#acc-add")?.scrollIntoView({ behavior: "smooth", block: "center" });
    toast("Preset selected — add it above", "ok");
  };
  document.getElementById("preset").addEventListener("change", applyPreset);
  document.getElementById("acc-account-id").addEventListener("input", fillAccountID);
  document.getElementById("acc-add").addEventListener("click", async () => {
    const p = presets.find((x) => x.id === sel.value);
    if (!p) {
      toast("No preset selected");
      return;
    }
    try {
      fillAccountID();
      const url = document.getElementById("acc-url").value;
      if (p.urlPlaceholder && url.includes(p.urlPlaceholder) && !p.accountIDEnvSet) {
        toast("This preset needs an account id. Paste it into Account ID, or export " + (p.accountIDEnv || "the documented env var") + " (value is never shown here).");
        return;
      }
      const body = {
        id: document.getElementById("acc-id").value,
        adapter: p.adapter,
        baseURL: url,
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
      if (!accounts.length || data.onboarding) {
        const cta = onboardingCard();
        if (!accounts.length) {
          host.innerHTML = cta;
          bindOnboarding(host, pickPreset);
          return;
        }
        host.innerHTML = cta;
      } else {
        host.innerHTML = "";
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
      host.insertAdjacentHTML(
        "beforeend",
        `<table>
      <thead><tr><th>ID</th><th>Adapter</th><th>Tier</th><th>Base URL</th><th>Status</th><th></th></tr></thead>
      <tbody>${rows}</tbody></table>`
      );
      bindOnboarding(host, pickPreset);
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
      sel.innerHTML = presetOptions(presets);
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
          const alias = m.aliasOf
            ? `<div class="muted">routes to ${escapeHtml(m.aliasOf)}</div>`
            : `<div class="row tight">
              <input data-rename-input="${escapeHtml(m.id)}" value="${escapeHtml(m.displayName || "")}" placeholder="Display name" size="18" />
              <button class="btn" data-rename="${escapeHtml(m.id)}">Save name</button>
            </div>`;
          const pinBtn = m.aliasOf ? "" : `<button class="btn" data-pin="${escapeHtml(m.id)}" data-on="${m.pinned ? "0" : "1"}">${pinLabel}</button>`;
          const hideBtn = m.aliasOf ? "" : `<button class="btn" data-hide="${escapeHtml(m.id)}" data-on="${m.hidden ? "0" : "1"}">${hideLabel}</button>`;
          return `<tr>
          <td>${m.pinned ? `<span class="pill accent">pin</span> ` : ""}${escapeHtml(shown)}
            <div class="muted">${escapeHtml(m.id)}</div>
            ${alias}
          </td>
          <td><span class="pill">${escapeHtml(m.tier)}</span></td>
          <td>${escapeHtml(m.provider)}</td>
          <td>${escapeHtml(m.accountId || "")}</td>
          <td>${escapeHtml((m.modalities || []).join(", "))}</td>
          <td>${escapeHtml(contextWindowText(m.contextWindow))}</td>
          <td>${m.exposed ? "listed" : "hidden"} / ${m.routable ? "routable" : "blocked"}</td>
          <td>${m.privacyNote ? `<span class="warn">${escapeHtml(m.privacyNote)}</span>` : ""}</td>
          <td>
            ${pinBtn}
            ${hideBtn}
          </td>
        </tr>`;
        })
        .join("");
      host.innerHTML = `<table>
      <thead><tr><th>Model</th><th>Tier</th><th>Provider</th><th>Account</th><th>Modalities</th><th>Context</th><th>List / route</th><th>Privacy</th><th></th></tr></thead>
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
      <p class="muted">Try a live chat, image generation, or embeddings. Usage is persisted next to the config file. Models with <code>image_in</code> accept an image URL or upload (OpenAI content parts). Models tagged <code>image_out</code> one-click generate via <code>POST /v1/images/generations</code> — never faked as chat. Models tagged <code>embeddings</code> try via <code>POST /v1/embeddings</code> — never faked as chat. Subscription OAuth adapters do not proxy image-out or embeddings; use an API-key OpenAI / Google / xAI / OpenAI-compat account.</p>
      <div class="row">
        <label>Model
          <select id="show-model"></select>
        </label>
        <button class="btn primary" id="show-send">Send</button>
        <button class="btn primary" id="show-generate" hidden>Generate image</button>
        <button class="btn primary" id="show-embed" hidden>Embed</button>
      </div>
      <label>Prompt <textarea id="show-prompt">Say hello in one short sentence.</textarea></label>
      <div id="show-vision" hidden>
        <div class="row">
          <label>Image URL <input id="show-image-url" size="42" placeholder="https://… or data:image/png;base64,…" /></label>
          <label>Upload <input id="show-file" type="file" accept="image/*" /></label>
        </div>
      </div>
      <div id="show-image-out" class="warn" hidden>
        <p id="show-image-out-msg"></p>
      </div>
      <div id="show-images" hidden></div>
      <pre id="show-out">Pick a model and send.</pre>
    </section>
    <section class="card">
      <h2>Usage by provider</h2>
      <p class="muted">Totals from <code>usage.json</code>. Each provider is the adapter that served the call (multiple accounts for the same adapter share a row).</p>
      <div id="show-usage">loading…</div>
    </section>`;
  let models = [];
  const sel = document.getElementById("show-model");
  const toggleVision = () => {
    const m = models.find((x) => x.id === sel.value);
    const mods = m?.modalities || [];
    const hasIn = mods.includes("image_in");
    const hasOut = mods.includes("image_out");
    const hasEmbed = mods.includes("embeddings");
    const ready = !!m?.imageOutReady;
    const embedReady = !!m?.embeddingsReady;
    document.getElementById("show-vision").hidden = !hasIn || hasOut || hasEmbed;
    document.getElementById("show-image-out").hidden = !hasOut && !hasEmbed;
    document.getElementById("show-send").hidden = hasOut || hasEmbed;
    document.getElementById("show-generate").hidden = !hasOut;
    document.getElementById("show-generate").disabled = hasOut && !ready;
    document.getElementById("show-embed").hidden = !hasEmbed;
    document.getElementById("show-embed").disabled = hasEmbed && !embedReady;
    const msg = document.getElementById("show-image-out-msg");
    if (hasOut && ready) {
      msg.innerHTML = "<strong>Image generation.</strong> This model is tagged <code>image_out</code>. Generate uses <code>POST /v1/images/generations</code>, not chat.";
    } else if (hasOut) {
      msg.innerHTML = "<strong>Image-out not on this adapter.</strong> Tagged <code>image_out</code>, but the connected account cannot proxy generations (subscription OAuth or chat-only). Use an API-key OpenAI / Google / xAI / OpenAI-compat account.";
    } else if (hasEmbed && embedReady) {
      msg.innerHTML = "<strong>Embeddings.</strong> This model is tagged <code>embeddings</code>. Embed uses <code>POST /v1/embeddings</code>, not chat.";
    } else if (hasEmbed) {
      msg.innerHTML = "<strong>Embeddings not on this adapter.</strong> Tagged <code>embeddings</code>, but the connected account cannot proxy embeddings (subscription OAuth or chat-only). Use an API-key OpenAI / Google / xAI / OpenAI-compat account.";
    }
    if (hasOut) {
      document.getElementById("show-prompt").value = "a simple icon of a pea pod";
    } else if (hasEmbed) {
      document.getElementById("show-prompt").value = "hello world";
    }
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
        document.getElementById("show-generate").disabled = true;
        document.getElementById("show-embed").disabled = true;
      }
      fillModelSelect(sel, models);
      sel.addEventListener("change", toggleVision);
      toggleVision();
    } catch (err) {
      toast(err.message);
      document.getElementById("show-out").textContent = err.message;
    }
    try {
      document.getElementById("show-usage").innerHTML = usageTables(await getJSON("/admin/usage"));
    } catch (err) {
      document.getElementById("show-usage").textContent = err.message;
    }
  })();
  const runShowcase = async (mode) => {
    const out = document.getElementById("show-out");
    const imgs = document.getElementById("show-images");
    out.textContent = "sending…";
    imgs.hidden = true;
    imgs.innerHTML = "";
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
      if (mode === "image") {
        payload.generateImage = true;
      } else if (mode === "embeddings") {
        payload.createEmbeddings = true;
      } else if (!document.getElementById("show-vision").hidden && imageUrl) {
        payload.imageUrl = imageUrl;
      }
      const data = await sendJSON("/admin/showcase", "POST", payload);
      if (data.imageOut) {
        const urls = data.urls || [];
        const b64 = data.b64 || [];
        urls.forEach((u) => {
          const img = document.createElement("img");
          img.className = "showcase-image";
          img.src = u;
          img.alt = "generated";
          imgs.appendChild(img);
        });
        b64.forEach((b) => {
          const img = document.createElement("img");
          img.className = "showcase-image";
          img.src = "data:image/png;base64," + b;
          img.alt = "generated";
          imgs.appendChild(img);
        });
        imgs.hidden = imgs.childElementCount === 0;
        out.textContent = JSON.stringify({ account: data.account, model: data.model, urls, b64Count: b64.length }, null, 2);
      } else if (data.embeddings) {
        out.textContent = JSON.stringify({
          account: data.account,
          model: data.model,
          count: data.count,
          dimensions: data.dimensions,
        }, null, 2);
      } else {
        out.textContent = data.content || JSON.stringify(data, null, 2);
      }
      document.getElementById("show-usage").innerHTML = usageTables(await getJSON("/admin/usage"));
    } catch (err) {
      out.textContent = err.message;
      toast(err.message);
    }
  };
  document.getElementById("show-send").addEventListener("click", () => runShowcase("chat"));
  document.getElementById("show-generate").addEventListener("click", () => runShowcase("image"));
  document.getElementById("show-embed").addEventListener("click", () => runShowcase("embeddings"));
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
        <p class="muted">Verify: <code>${escapeHtml(c.verify || "peaproxy clients verify " + c.name)}</code> against a running <code>peaproxy serve</code>.</p>
        <div class="row snippet-actions">
          <button class="btn" data-copy>Copy snippet</button>
          <button class="btn" data-verify="${escapeHtml(c.verify || "")}">Copy verify</button>
          ${c.connectable ? `<button class="btn" data-connect="${escapeHtml(c.name)}">Connect</button><button class="btn" data-disconnect="${escapeHtml(c.name)}">Disconnect</button><button class="btn" data-probe="${escapeHtml(c.name)}">Check</button>` : ""}
        </div>
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
      document.querySelectorAll("[data-connect]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          const name = btn.dataset.connect || "";
          try {
            const data = await sendJSON("/admin/clients/" + encodeURIComponent(name) + "/connect", "POST", { model: "" });
            if (data.status === "guided") {
              toast(name + " needs manual setup: use Copy snippet and paste it into its config");
              return;
            }
            btn.textContent = "Connected";
            toast("Connected " + name, "ok");
          } catch (err) {
            toast(err.message);
          }
        });
      });
      document.querySelectorAll("[data-disconnect]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          const name = btn.dataset.disconnect || "";
          try {
            await sendJSON("/admin/clients/" + encodeURIComponent(name) + "/disconnect", "POST", {});
            btn.textContent = "Disconnected";
            toast("Disconnected " + name, "ok");
          } catch (err) {
            toast(err.message);
          }
        });
      });
      document.querySelectorAll("[data-probe]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          const name = btn.dataset.probe || "";
          try {
            await sendJSON("/admin/clients/" + encodeURIComponent(name) + "/verify", "POST", {});
            btn.textContent = "Checked";
            toast("Checked " + name, "ok");
          } catch (err) {
            toast(err.message);
          }
        });
      });
      document.querySelectorAll("[data-verify]").forEach((btn) => {
        btn.addEventListener("click", async () => {
          const cmd = btn.dataset.verify || "";
          try {
            await navigator.clipboard.writeText(cmd);
            btn.textContent = "Copied verify";
            toast("Copied " + cmd, "ok");
          } catch (err) {
            toast(cmd || err.message);
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
      <p class="muted">Last live <code>ListModels</code> (or <code>Validate</code> after Probe). Cooldown overlays skip windows without disabling the account.</p>
      <button class="btn" id="probe">Probe adapters</button>
      <div id="ah">loading…</div>
    </section>
    <section class="card"><h2>Quota remaining</h2>
      <p class="muted">Only remaining the provider actually returned (rate-limit headers on chat/embeddings/images, or OpenRouter <code>GET /key</code> on Health refresh). Also <code>GET /admin/quota</code>. Missing remaining is unknown — never shown as 0 or unlimited.</p>
      <div id="quota">loading…</div>
    </section>
    <section class="card"><h2>Account cooldowns</h2>
      <p class="muted">After a rate-limit, overload, or auth-expired failure the account is skipped for 30s, or for the provider's reset hint when it sends one. Connection failures (502, 504, edge 503) skip it for 5s; a slow first token moves on without a cooldown. Cooled accounts are not re-hit until the window expires (avoids cooldown storms). Routing follows <code>failover.policy</code> (round-robin default, or fill-first / sticky).</p>
      <div id="cd">loading…</div>
    </section>
    <section class="card"><h2>Gateway</h2><pre id="h">loading…</pre></section>
    <section class="card"><h2>Usage</h2><div id="u">loading…</div></section>`;
  const fmtRemaining = (ms) => {
    const n = Number(ms) || 0;
    if (n <= 0) return "expired";
    const sec = Math.ceil(n / 1000);
    return sec + "s left";
  };
  const loadHealth = async () => {
    const d = await getJSON("/admin/health");
    document.getElementById("h").textContent = JSON.stringify(d, null, 2);
    const quotaMap = quotaByAccount(d.quota);
    const adapters = d.adapterHealth || [];
    document.getElementById("ah").innerHTML = adapters.length
      ? `<table>
      <thead><tr><th>Account</th><th>Adapter</th><th>Status</th><th>Quota remaining</th><th>Models</th><th>Latency</th><th>Error</th></tr></thead>
      <tbody>${adapters
        .map(
          (a) => `<tr>
        <td>${escapeHtml(a.accountId)}</td>
        <td>${escapeHtml(a.adapter)}</td>
        <td><span class="pill ${a.status === "ok" ? "ok" : ""}">${escapeHtml(a.status)}</span></td>
        <td>${quotaRemainingHTML(quotaMap[a.accountId], a.quotaHint)}</td>
        <td>${escapeHtml(String(a.models ?? 0))}</td>
        <td>${escapeHtml(String(a.latencyMs ?? 0))}ms</td>
        <td>${a.error ? `<span class="warn">${escapeHtml(a.error)}</span>` : ""}</td>
      </tr>`
        )
        .join("")}</tbody></table>`
      : emptyState("No adapters probed yet", "Add an account, then refresh the catalog or click Probe.");
    const quotaRows = (d.quota || [])
      .map((q) => {
        return `<tr>
        <td>${escapeHtml(q.accountId || "")}</td>
        <td>${escapeHtml(q.adapter || "")}</td>
        <td>${quotaRemainingHTML(q, "")}</td>
        <td class="muted">${escapeHtml(q.source || "none")}</td>
      </tr>`;
      })
      .join("");
    document.getElementById("quota").innerHTML = quotaRows
      ? `<table>
      <thead><tr><th>Account</th><th>Adapter</th><th>Remaining</th><th>Source</th></tr></thead>
      <tbody>${quotaRows}</tbody></table>`
      : emptyState("No accounts", "Add an account to see quota remaining when a provider reports it. Missing remaining stays unknown — never shown as 0.");
    const rows = (d.cooldowns || [])
      .map(
        (c) =>
          `<tr><td>${escapeHtml(c.accountId)}</td><td>${escapeHtml(c.reason)}</td><td>${escapeHtml(fmtRemaining(c.remainingMs))}</td><td>${quotaRemainingHTML(quotaMap[c.accountId], c.quotaHint)}</td><td class="muted">${escapeHtml(c.until || "")}</td></tr>`
      )
      .join("");
    document.getElementById("cd").innerHTML = rows
      ? `<table>
      <thead><tr><th>Account</th><th>Reason</th><th>Cooldown</th><th>Quota remaining</th><th>Until</th></tr></thead>
      <tbody>${rows}</tbody></table>`
      : emptyState("No accounts in cooldown", "Retryable failover (429/401, rate-limit / overloaded / auth-expired bodies) will show a skip window here. Last known remaining still appears on adapter health.");
  };
  loadHealth().catch((err) => {
    document.getElementById("h").textContent = err.message;
    document.getElementById("ah").innerHTML = emptyState("Health unavailable", err.message);
    document.getElementById("quota").innerHTML = emptyState("Health unavailable", err.message);
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
    .then((d) => {
      document.getElementById("u").innerHTML = usageTables(d);
    })
    .catch((err) => {
      document.getElementById("u").textContent = err.message;
    });
}

function requestsPage(root) {
  root.innerHTML = `<section class="card">
      <h2>Request log</h2>
      <p class="muted">Opt-in inspector. Previews are redacted (tokens, API keys, JWTs, PEM). The JSONL file is mode <code>0600</code> and rotates at 1MiB. When an upstream response includes rate-limit remaining headers, a compact hint is shown on that row — never invented as 0.</p>
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
            <td>${escapeHtml(e.accountId || "")}${e.quotaHint ? ` <span class="muted">${escapeHtml(e.quotaHint)}</span>` : ""}</td>
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
    const d = await toggleRequestLog(box);
    if (d) await load();
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
  root.innerHTML = `
    <section class="card">
      <h2>Listen</h2>
      <p class="muted">Bind stays loopback unless <code>--allow-lan</code> (or <code>allowNonLoopback</code>) <strong>and</strong> a non-empty admin token. Hide does not block routing unless <code>hide.blockRouting</code> is true.</p>
      <p id="bind-line">loading…</p>
      <div id="lan-settings" class="warn" hidden></div>
      <label>Admin token (sent as <code>X-Admin-Token</code> from this UI)
        <input id="ui-token" type="password" autocomplete="off" />
      </label>
      <button class="btn" id="save-token">Save token in this browser</button>
      <p class="muted" id="token-req"></p>
    </section>
    <section class="card">
      <h2>Config &amp; secrets</h2>
      <p>Config path: <code id="cfg-path"></code> <button class="btn" id="copy-path">Copy path</button></p>
      <p>Secret backend: <strong id="secret-backend"></strong></p>
      <p class="muted" id="secret-note"></p>
      <p class="muted">YAML lists accounts only. Tokens and inline API keys stay in the keychain or <code>secrets.enc</code> — never shown here.</p>
    </section>
    <section class="card">
      <h2>Request log</h2>
      <p class="muted">Same toggle as the Request log page. Usage counters still go to <code>usage.json</code> when this is off.</p>
      <label class="row"><input type="checkbox" id="reqlog" /> Opt-in redacted request log (<code>requests.log</code>)</label>
      <p class="muted" id="reqlog-path"></p>
    </section>
    <section class="card">
      <h2>Catalog overlays</h2>
      <p class="muted" id="overlay-summary"></p>
      <details>
        <summary>Raw settings JSON (no secrets)</summary>
        <pre id="s">loading…</pre>
      </details>
    </section>`;
  const box = document.getElementById("reqlog");
  document.getElementById("ui-token").value = localStorage.getItem(TOKEN_KEY) || "";
  document.getElementById("save-token").addEventListener("click", () => {
    localStorage.setItem(TOKEN_KEY, document.getElementById("ui-token").value);
    toast("Admin token saved for this browser", "ok");
  });
  const apply = (d) => {
    const bind = document.getElementById("bind-line");
    const loop = d.loopback ? "loopback" : "non-loopback";
    bind.textContent = `Listening on ${d.bind || ""}:${d.port || ""} (${loop}).`;
    const lan = document.getElementById("lan-settings");
    if (d.lanWarning) {
      lan.hidden = false;
      lan.textContent = "This process is bound off loopback. Keep the admin token private. /healthz stays public; /admin requires the token.";
    } else {
      lan.hidden = true;
      lan.textContent = "";
    }
    document.getElementById("token-req").textContent = d.adminTokenRequired
      ? "Admin token is required for /admin on this bind."
      : "Loopback: /admin does not require a token.";
    document.getElementById("cfg-path").textContent = d.configPath || "(in-memory)";
    document.getElementById("secret-backend").textContent = d.secretBackend || "file";
    document.getElementById("secret-note").textContent = d.secretBackendNote || "";
    box.checked = !!d.requestLog;
    document.getElementById("reqlog-path").textContent = d.requestLog
      ? "File: " + (d.requestLogPath || "requests.log")
      : "No log file (enable to start writing).";
    const pin = (d.catalog && d.catalog.pin) || [];
    const rename = (d.catalog && d.catalog.rename) || {};
    const hide = (d.hide && d.hide.models) || [];
    document.getElementById("overlay-summary").textContent =
      `Pin ${pin.length}, rename ${Object.keys(rename).length}, hide ${hide.length} model(s). Listing-only hide: ${d.listingOnlyHide !== false}. Edit pin/rename on Catalog.`;
    document.getElementById("s").textContent = JSON.stringify(d, null, 2);
  };
  document.getElementById("copy-path").addEventListener("click", async () => {
    const path = document.getElementById("cfg-path").textContent;
    try {
      await navigator.clipboard.writeText(path);
      toast("Copied config path", "ok");
    } catch (err) {
      toast(path);
    }
  });
  getJSON("/admin/settings")
    .then(apply)
    .catch((err) => {
      document.getElementById("s").textContent = err.message;
      document.getElementById("bind-line").textContent = err.message;
      toast(err.message);
    });
  box.addEventListener("change", async () => {
    const d = await toggleRequestLog(box);
    if (d) apply(d);
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
    case "copilot_oauth":
      return "copilot";
    case "factory_oauth":
      return "factory";
    case "opencode_go":
      return "opencode-go";
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

// policyPage shows what v3 is doing right now.
//
// The defaults are opinionated, which is only fair if they are visible: a user
// who cannot see that free-only or a spend ceiling is in effect has no way to
// turn it off. Values are the resolved ones from /admin/policy -- what is on,
// not what was merely left unset.
function policyPage(root) {
  root.innerHTML = `<section class="card"><h2>Cost policy</h2><div id="policy-body">loading…</div></section>`;
  getJSON("/admin/policy")
    .then((data) => {
      const p = data.policy || {};
      const yesNo = (v) => (v ? "on" : "off");
      const rows = [
        ["Automatic optimizations", yesNo(p.automatic), "routing and prompt-cache savings applied without being asked"],
        ["Free only", yesNo(p.freeOnly), "refuses any deployment that cannot show a verified zero price"],
        ["Context optimization", yesNo(p.contextOptimization), "carries context across turns for clients that cannot run tools"],
        ["Local assistant", yesNo(p.localAssistant), p.localEndpointConfigured ? "configured" : "no endpoint configured"],
        ["Persistent context", yesNo(p.persistentContext), "stored artifacts survive a restart"],
        ["Prompt cache", escapeHtml(p.promptCache || ""), "how request prefixes are handled"],
      ];
      const spendRows = [];
      if (p.spendCeilingUSD === null || p.spendCeilingUSD === undefined) {
        spendRows.push("<tr><td>Ceiling</td><td>none set</td><td class='muted'>0 would mean a budget of zero, so it is reported as unset</td></tr>");
      } else {
        spendRows.push(
          `<tr><td>Ceiling</td><td>$${Number(p.spendCeilingUSD).toFixed(2)}</td><td class='muted'>30-day window</td></tr>`,
        );
      }
      spendRows.push(
        `<tr><td>Spent (30d)</td><td>$${Number(p.spentLast30DaysUSD || 0).toFixed(2)}</td><td class='muted'>${escapeHtml(p.spendNote || "")}</td></tr>`,
      );
      if (Number(p.estimatedLast30DaysUSD || 0) > 0) {
        spendRows.push(
          `<tr><td>of which estimated</td><td>$${Number(p.estimatedLast30DaysUSD).toFixed(4)}</td><td class='muted'>priced by PeaProxy from published token counts, because this provider publishes no cost of its own</td></tr>`,
        );
      }
      if (Number(p.inFlightReservedUSD || 0) > 0) {
        spendRows.push(
          `<tr><td>In flight</td><td>$${Number(p.inFlightReservedUSD).toFixed(4)}</td><td class='muted'>held by requests currently at the provider; counted so a burst cannot all pass the same ceiling check</td></tr>`,
        );
      }
      if (p.spendMeasurable === false) {
        spendRows.push(
          `<tr><td>Completeness</td><td class='warn'>partial</td><td class='muted'>${p.totalCallsLast30Days - p.pricedCallsLast30Days} call(s) had no published price, so this total is a floor rather than a sum</td></tr>`,
        );
      }
      root.querySelector("#policy-body").innerHTML =
        `<table><tbody>${rows
          .map(
            ([k, v, note]) =>
              `<tr><td>${escapeHtml(k)}</td><td>${v}</td><td class='muted'>${escapeHtml(note)}</td></tr>`,
          )
          .join("")}</tbody></table>` +
        `<h3>Spend</h3><table><tbody>${spendRows.join("")}</tbody></table>` +
        `<p class="muted">${escapeHtml(data.honesty || "")}</p>`;
    })
    .catch((err) => {
      root.querySelector("#policy-body").innerHTML = `<p class="warn">${escapeHtml(err.message)}</p>`;
    });
}
