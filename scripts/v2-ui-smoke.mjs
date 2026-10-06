import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import net from "node:net";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const adversarial = `<img src=x onerror="window.__xss=1">`;

function freePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      const port = typeof address === "object" && address ? address.port : 0;
      server.close(() => resolve(port));
    });
    server.on("error", reject);
  });
}

function waitFor(url) {
  const started = Date.now();
  return new Promise((resolve, reject) => {
    const tick = () => {
      fetch(url)
        .then((res) => (res.ok ? resolve() : Promise.reject(new Error(String(res.status)))))
        .catch(() => {
          if (Date.now() - started > 30000) {
            reject(new Error("timed out waiting for " + url));
            return;
          }
          setTimeout(tick, 200);
        });
    };
    tick();
  });
}

const work = await mkdtemp(join(tmpdir(), "peaproxy-ui-"));
const clientRoot = join(work, "home");
const upstreamPort = await freePort();
const proxyPort = await freePort();
let chatSeen = false;
let chatCancelled = false;

const upstream = createServer((req, res) => {
  if (req.url === "/v1/models") {
    res.setHeader("Content-Type", "application/json");
    res.end(JSON.stringify({ data: [{ id: adversarial }] }));
    return;
  }
  if (req.url === "/v1/chat/completions") {
    chatSeen = true;
    req.on("close", () => {
      if (!res.writableEnded) chatCancelled = true;
    });
    return;
  }
  res.writeHead(404);
  res.end();
});
await new Promise((resolve) => upstream.listen(upstreamPort, "127.0.0.1", resolve));

const configPath = join(work, "peaproxy.yaml");
await writeFile(
  configPath,
  `schemaVersion: 1
bind: 127.0.0.1
port: ${proxyPort}
providers:
  - id: fixture
    adapter: openai_compat
    tier: local
    baseURL: http://127.0.0.1:${upstreamPort}/v1
`,
);

const bin = join(work, "peaproxy");
const build = spawn("go", ["build", "-o", bin, "./cmd/peaproxy"], {
  cwd: root,
  stdio: "inherit",
  env: { ...process.env, PEAPROXY_SECRET_BACKEND: "file" },
});
const buildCode = await new Promise((resolve) => build.on("exit", resolve));
if (buildCode !== 0) {
  process.exit(buildCode ?? 1);
}

const proxy = spawn(bin, ["serve", "--config", configPath, "--port", String(proxyPort)], {
  cwd: root,
  stdio: "inherit",
  env: {
    ...process.env,
    PEAPROXY_SECRET_BACKEND: "file",
    PEAPROXY_CLIENT_ROOT: clientRoot,
  },
});

let browser;
let dialogFired = false;
try {
  await waitFor(`http://127.0.0.1:${proxyPort}/healthz`);
  const { chromium } = await import("playwright");
  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage();
  page.on("dialog", async (dialog) => {
    dialogFired = true;
    await dialog.dismiss();
  });
  await page.goto(`http://127.0.0.1:${proxyPort}/ui/`);
  await page.getByRole("button", { name: "Catalog" }).click();
  await page.getByText(adversarial, { exact: false }).waitFor();
  if (dialogFired) {
    throw new Error("unexpected dialog");
  }
  const xss = await page.evaluate(() => window.__xss);
  if (xss) {
    throw new Error("adversarial model id executed");
  }
  if (await page.locator("#cat-table img, #cat-table script").count()) {
    throw new Error("catalog rendered the model id as markup");
  }

  // The add-account dropdown is grouped (#113). A flat list is not a visual
  // bug anyone reports, so this is the only place it gets checked.
  await page.getByRole("button", { name: "Accounts" }).click();
  const preset = page.locator("#preset");
  await preset.waitFor();
  // The dropdown is filled by an async fetch after the page renders, so the
  // element existing proves nothing about it being populated.
  await preset.locator("option").first().waitFor({ state: "attached", timeout: 10000 });
  const groups = await preset.locator("optgroup").evaluateAll((els) =>
    els.map((el) => el.label),
  );
  if (groups.length < 2) {
    throw new Error("preset dropdown rendered no optgroups: " + JSON.stringify(groups));
  }
  if (groups[0] !== "Local models") {
    throw new Error("first preset group is " + groups[0]);
  }
  // Groups must be contiguous: a name appearing twice means the sort let a
  // section split, which renders as the same heading twice.
  if (new Set(groups).size !== groups.length) {
    throw new Error("preset groups repeat: " + JSON.stringify(groups));
  }
  const labels = await preset.locator("option").evaluateAll((els) =>
    els.map((el) => el.textContent),
  );
  for (const hidden of ["qwen-oauth", "factory-oauth"]) {
    if (await preset.locator(`option[value="${hidden}"]`).count()) {
      throw new Error("unreleased preset offered for a new account: " + hidden);
    }
  }
  if (await page.locator("#preset img, #preset script").count()) {
    throw new Error("preset dropdown rendered a label as markup");
  }
  console.log(`accounts dropdown: ${groups.length} groups, ${labels.length} presets`);

  // The cost policy panel exists so an opinionated default is visible. A user
  // who cannot see that free-only or a spend ceiling is on has no way to turn
  // it off.
  await page.getByRole("button", { name: "Policy" }).click();
  const policy = page.locator("#policy-body");
  await policy.getByText("Automatic optimizations").waitFor({ timeout: 10000 });
  const policyText = await policy.innerText();
  for (const must of ["Free only", "Context optimization", "Local assistant", "Spent (30d)"]) {
    if (!policyText.includes(must)) {
      throw new Error(`policy panel is missing ${must}: ${policyText}`);
    }
  }
  // An unset ceiling must read as unset, not as a zero budget.
  if (!policyText.includes("none set")) {
    throw new Error("an unconfigured spend ceiling is not reported as unset: " + policyText);
  }
  if (await page.locator("#policy-body img, #policy-body script").count()) {
    throw new Error("policy panel rendered content as markup");
  }
  console.log("policy panel rendered");
  if (!labels.some((l) => l.includes("(not verified)"))) {
    throw new Error("no preset carries the unverified marker");
  }
  // Selecting an unverified preset must surface its note rather than looking
  // like any other entry.
  await preset.selectOption("deepseek-key").catch(() => {});
  if (await preset.locator(`option[value="deepseek-key"]`).count()) {
    await page.waitForFunction(() => {
      const n = document.getElementById("preset-note");
      return n && !n.hidden && /not live-verified/i.test(n.textContent || "");
    }, undefined, { timeout: 3000 }).catch(() => {
      throw new Error("selecting an unverified preset did not show its verification note");
    });
  }

  await page.getByRole("button", { name: "Clients" }).click();
  const guided = page.locator("[data-connect='opencode']");
  await guided.waitFor();
  await guided.click();
  await page.getByText("opencode needs manual setup").waitFor();
  if ((await guided.textContent()) !== "Connect") {
    throw new Error("guided connect was reported as connected");
  }
  if (await readFile(join(clientRoot, "opencode.json"), "utf8").then(() => true, () => false)) {
    throw new Error("guided connect wrote opencode.json");
  }

  const settingsPath = join(clientRoot, ".claude", "settings.json");
  await page.locator("[data-connect='claude-code']").click();
  await page.getByText("Connected claude-code").waitFor();
  const written = JSON.parse(await readFile(settingsPath, "utf8"));
  if (written.env?.ANTHROPIC_API_KEY !== "peaproxy") {
    throw new Error("connect did not write the claude-code env");
  }
  if (written.env?.ANTHROPIC_BASE_URL !== `http://127.0.0.1:${proxyPort}`) {
    throw new Error("connect wrote the wrong base URL: " + written.env?.ANTHROPIC_BASE_URL);
  }

  await page.route("**/admin/clients/claude-code/verify", (route) => route.abort());
  await page.locator("[data-probe='claude-code']").click();
  await page.locator("#toasts").getByText(/aborted|failed|error/i).waitFor();

  await page.locator("[data-disconnect='claude-code']").click();
  await page.getByText("Disconnected claude-code").waitFor();
  const after = await readFile(settingsPath, "utf8");
  if (after.includes("ANTHROPIC_")) {
    throw new Error("disconnect left the PeaProxy env");
  }

  // Pi is connectable from the UI too: the button list comes from the server,
  // not from a list of names in this file.
  const piConnect = page.locator("[data-connect='pi']");
  await piConnect.waitFor();
  const piPath = join(clientRoot, ".pi", "agent", "models.json");
  await piConnect.click();
  await page.getByText("Connected pi").waitFor();
  const piFile = JSON.parse(await readFile(piPath, "utf8"));
  const origin = `http://127.0.0.1:${proxyPort}`;
  if (piFile.providers?.anthropic?.baseUrl !== origin) {
    throw new Error("pi anthropic baseUrl should have no /v1: " + piFile.providers?.anthropic?.baseUrl);
  }
  if (piFile.providers?.openai?.baseUrl !== origin + "/v1") {
    throw new Error("pi openai baseUrl should carry /v1: " + piFile.providers?.openai?.baseUrl);
  }
  if (piFile.providers?.anthropic?.apiKey !== "peaproxy") {
    throw new Error("pi connect did not write the api key");
  }
  await page.locator("[data-disconnect='pi']").click();
  await page.getByText("Disconnected pi").waitFor();
  const piAfter = await readFile(piPath, "utf8");
  if (piAfter.includes("peaproxy")) {
    throw new Error("pi disconnect left the PeaProxy provider");
  }
  // A client with no managed config gets no Connect button.
  if (await page.locator("[data-connect='cursor']").count()) {
    throw new Error("cursor has no managed config and should not offer Connect");
  }

  await page.evaluate((origin) => {
    window.__chatCtrl = new AbortController();
    window.__chatDone = fetch(origin + "/v1/chat/completions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        model: `<img src=x onerror="window.__xss=1">`,
        messages: [{ role: "user", content: "hi" }],
      }),
      signal: window.__chatCtrl.signal,
    }).then(
      () => "done",
      (err) => String(err),
    );
  }, `http://127.0.0.1:${proxyPort}`);
  const seenDeadline = Date.now() + 5000;
  while (!chatSeen && Date.now() < seenDeadline) {
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  if (!chatSeen) {
    throw new Error("chat never reached upstream");
  }
  await page.evaluate(() => window.__chatCtrl.abort());
  await page.evaluate(() => window.__chatDone);
  const cancelDeadline = Date.now() + 3000;
  while (!chatCancelled && Date.now() < cancelDeadline) {
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  if (!chatCancelled) {
    throw new Error("aborted chat did not cancel upstream");
  }
  if (dialogFired || (await page.evaluate(() => window.__xss))) {
    throw new Error("adversarial model id executed");
  }
} finally {
  const proxyExit = new Promise((resolve) => {
    if (proxy.exitCode !== null || proxy.signalCode !== null) {
      resolve(proxy.exitCode);
      return;
    }
    proxy.once("exit", (code) => resolve(code));
  });
  proxy.kill("SIGTERM");
  if (browser) await browser.close();
  upstream.close();
  await rm(work, { recursive: true, force: true });
  const exitCode = await Promise.race([
    proxyExit,
    new Promise((resolve) => setTimeout(() => resolve(0), 2000)),
  ]);
  if (exitCode) process.exitCode = 0;
}
