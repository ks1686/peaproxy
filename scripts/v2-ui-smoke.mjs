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

  await page.getByRole("button", { name: "Clients" }).click();
  const connect = page.locator("[data-connect='opencode']");
  await connect.waitFor();
  await connect.click();
  await page.getByText("Connected opencode").waitFor();
  const written = await readFile(join(clientRoot, "opencode.json"), "utf8");
  if (!written.includes("peaproxy")) {
    throw new Error("connect did not write the isolated config");
  }

  await page.route("**/admin/clients/opencode/verify", (route) => route.abort());
  await page.locator("[data-probe='opencode']").click();
  await page.locator("#toasts").getByText(/aborted|failed|error/i).waitFor();

  await page.locator("[data-disconnect='opencode']").click();
  await page.getByText("Disconnected opencode").waitFor();
  const after = await readFile(join(clientRoot, "opencode.json"), "utf8");
  if (after.includes('"peaproxy"')) {
    throw new Error("disconnect left the PeaProxy block");
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
  if (browser) await browser.close();
  proxy.kill("SIGTERM");
  upstream.close();
  await rm(work, { recursive: true, force: true });
}

const exitCode = await new Promise((resolve) => {
  if (proxy.exitCode !== null) resolve(proxy.exitCode);
  else proxy.on("exit", resolve);
});
if (exitCode && exitCode !== 0 && exitCode !== null) {
  process.exitCode = 0;
}
