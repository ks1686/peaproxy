#!/usr/bin/env node
// Capture README screenshots from a live localhost PeaProxy UI.
// Uses a throwaway config + in-process OpenAI-compat mock (no API keys, no OAuth).
//
//   go build -o /tmp/peaproxy ./cmd/peaproxy
//   PEAPROXY_BIN=/tmp/peaproxy node scripts/capture-readme-screenshots.mjs
//
// Requires Google Chrome (or CHROME_PATH) and puppeteer-core.

import http from "node:http";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";
import zlib from "node:zlib";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, "..");
const outDir = path.join(repoRoot, "docs", "screenshots");
const uiPort = Number(process.env.PEAPROXY_UI_PORT || 8317);
const mockPort = Number(process.env.PEAPROXY_MOCK_PORT || 18434);
const uiURL = `http://127.0.0.1:${uiPort}/`;
const bin = process.env.PEAPROXY_BIN || path.join(repoRoot, "peaproxy");
const shotDir = process.env.PEAPROXY_SHOT_DIR || path.join(os.tmpdir(), "peaproxy-demo");
const chromePath =
  process.env.CHROME_PATH ||
  ["/usr/local/bin/google-chrome", "/usr/bin/google-chrome-stable", "/usr/bin/google-chrome"].find((p) =>
    fs.existsSync(p)
  );

const DEMO_PNG = pngRGB(96, 96, [182, 224, 74]);

function pngRGB(width, height, rgb) {
  const raw = Buffer.alloc((width * 3 + 1) * height);
  for (let y = 0; y < height; y++) {
    const row = y * (width * 3 + 1);
    raw[row] = 0;
    for (let x = 0; x < width; x++) {
      const i = row + 1 + x * 3;
      raw[i] = rgb[0];
      raw[i + 1] = rgb[1];
      raw[i + 2] = rgb[2];
    }
  }
  const chunk = (type, data) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    const crcBuf = Buffer.concat([type, data]);
    const crc = Buffer.alloc(4);
    crc.writeUInt32BE(zlib.crc32(crcBuf) >>> 0);
    return Buffer.concat([len, type, data, crc]);
  };
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8;
  ihdr[9] = 2;
  return Buffer.concat([
    Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]),
    chunk(Buffer.from("IHDR"), ihdr),
    chunk(Buffer.from("IDAT"), zlib.deflateSync(raw)),
    chunk(Buffer.from("IEND"), Buffer.alloc(0)),
  ]);
}

function startMock() {
  const models = {
    object: "list",
    data: [
      { id: "llama3.2", object: "model" },
      { id: "gpt-image-1", object: "model" },
    ],
  };
  const server = http.createServer((req, res) => {
    const url = req.url.split("?")[0];
    if (req.method === "GET" && (url === "/v1/models" || url === "/models")) {
      res.writeHead(200, { "content-type": "application/json" });
      res.end(JSON.stringify(models));
      return;
    }
    if (req.method === "POST" && (url === "/v1/chat/completions" || url === "/chat/completions")) {
      res.writeHead(200, { "content-type": "application/json" });
      res.end(
        JSON.stringify({
          id: "chatcmpl-demo",
          object: "chat.completion",
          choices: [
            {
              index: 0,
              message: { role: "assistant", content: "Hello from the local PeaProxy demo catalog." },
              finish_reason: "stop",
            },
          ],
        })
      );
      return;
    }
    if (req.method === "POST" && (url === "/v1/images/generations" || url === "/images/generations")) {
      res.writeHead(200, { "content-type": "application/json" });
      res.end(
        JSON.stringify({
          created: Math.floor(Date.now() / 1000),
          data: [{ b64_json: DEMO_PNG.toString("base64") }],
        })
      );
      return;
    }
    res.writeHead(404);
    res.end("not found");
  });
  return new Promise((resolve, reject) => {
    server.listen(mockPort, "127.0.0.1", () => resolve(server));
    server.on("error", reject);
  });
}

function writeYAML(file, body) {
  fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
  fs.writeFileSync(file, body, { mode: 0o600 });
}

function onboardingConfig(file) {
  writeYAML(
    file,
    `schemaVersion: 1
bind: 127.0.0.1
port: ${uiPort}
requestLog: false
providers:
  - id: ollama-local
    adapter: ollama
    tier: local
    baseURL: http://127.0.0.1:11434/v1
`
  );
}

function demoConfig(file) {
  writeYAML(
    file,
    `schemaVersion: 1
bind: 127.0.0.1
port: ${uiPort}
requestLog: true
catalog:
  pin:
    - llama3.2
  rename:
    llama3.2: Llama 3.2 local
    gpt-image-1: Demo image-out
providers:
  - id: demo-local
    adapter: openai_compat
    tier: local
    baseURL: http://127.0.0.1:${mockPort}/v1
`
  );
}

function spawnServe(configPath) {
  const child = spawn(bin, ["serve", "--config", configPath], {
    env: {
      ...process.env,
      PEAPROXY_SECRET_BACKEND: "file",
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let log = "";
  child.stdout.on("data", (d) => {
    log += d.toString();
  });
  child.stderr.on("data", (d) => {
    log += d.toString();
  });
  child.log = () => log;
  return child;
}

async function waitHealth(timeoutMs = 15000) {
  const deadline = Date.now() + timeoutMs;
  let last = "";
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`http://127.0.0.1:${uiPort}/healthz`);
      if (res.ok) return;
      last = await res.text();
    } catch (err) {
      last = err.message;
    }
    await sleep(150);
  }
  throw new Error("peaproxy did not become healthy: " + last);
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

async function stopServe(child) {
  if (!child || child.killed) return;
  child.kill("SIGTERM");
  await new Promise((resolve) => {
    const t = setTimeout(() => {
      child.kill("SIGKILL");
      resolve();
    }, 3000);
    child.on("exit", () => {
      clearTimeout(t);
      resolve();
    });
  });
}

function loadPuppeteer() {
  const candidates = [
    path.join(os.tmpdir(), "peaproxy-capture", "node_modules", "puppeteer-core"),
    path.join(repoRoot, "node_modules", "puppeteer-core"),
    "puppeteer-core",
  ];
  const require = createRequire(path.join(repoRoot, "scripts", "capture-readme-screenshots.mjs"));
  let last = null;
  for (const id of candidates) {
    try {
      return require(id);
    } catch (err) {
      last = err;
    }
  }
  throw new Error(
    "puppeteer-core is required. Install with: npm install --prefix /tmp/peaproxy-capture puppeteer-core\n" +
      (last ? last.message : "")
  );
}

async function withPage(fn) {
  const puppeteer = loadPuppeteer();
  if (!chromePath) {
    throw new Error("Chrome not found; set CHROME_PATH");
  }
  const browser = await puppeteer.launch({
    executablePath: chromePath,
    headless: "new",
    args: [
      "--no-sandbox",
      "--disable-gpu",
      "--disable-dev-shm-usage",
      `--window-size=1440,900`,
    ],
    defaultViewport: { width: 1440, height: 900, deviceScaleFactor: 1 },
  });
  try {
    const page = await browser.newPage();
    await page.goto(uiURL, { waitUntil: "networkidle0", timeout: 20000 });
    await page.waitForSelector("nav button");
    await fn(page);
  } finally {
    await browser.close();
  }
}

async function shot(page, name, opts = {}) {
  const dest = path.join(outDir, name);
  await page.screenshot({ path: dest, type: "png", fullPage: opts.fullPage !== false });
  console.log("wrote", dest);
}

async function openPage(page, name) {
  await page.click(`nav button[data-page="${name}"]`);
  await sleep(250);
}

async function waitGone(page, selector, text) {
  await page.waitForFunction(
    (sel, needle) => {
      const el = document.querySelector(sel);
      return el && !String(el.textContent || "").includes(needle);
    },
    { timeout: 10000 },
    selector,
    text
  );
}

async function captureOnboarding(configPath) {
  onboardingConfig(configPath);
  const child = spawnServe(configPath);
  try {
    await waitHealth();
    await withPage(async (page) => {
      await page.waitForSelector("#onboarding", { timeout: 10000 });
      await shot(page, "accounts.png");
    });
  } finally {
    await stopServe(child);
  }
}

async function captureDemo(configPath) {
  demoConfig(configPath);
  const child = spawnServe(configPath);
  try {
    await waitHealth();
    const chat = await fetch(`http://127.0.0.1:${uiPort}/v1/chat/completions`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        model: "llama3.2",
        messages: [{ role: "user", content: "hi" }],
      }),
    });
    if (!chat.ok) {
      throw new Error("demo chat failed: " + (await chat.text()));
    }
    await withPage(async (page) => {
      await openPage(page, "catalog");
      await waitGone(page, "#cat-table", "loading…");
      await shot(page, "catalog.png");

      await openPage(page, "showcase");
      await page.waitForSelector("#show-model");
      await page.select("#show-model", "gpt-image-1");
      await sleep(200);
      await page.click("#show-generate");
      await page.waitForFunction(
        () => {
          const imgs = document.querySelectorAll("#show-images img");
          const out = document.querySelector("#show-out");
          return imgs.length > 0 || (out && /account|imageOut|b64Count/i.test(out.textContent || ""));
        },
        { timeout: 10000 }
      );
      await sleep(300);
      await shot(page, "showcase.png");

      await openPage(page, "health");
      await waitGone(page, "#ah", "loading…");
      await shot(page, "health.png", { fullPage: false });

      await openPage(page, "requests");
      await page.waitForSelector("#req-table");
      await waitGone(page, "#req-table", "loading…");
      await shot(page, "requests.png");

      await openPage(page, "settings");
      await page.waitForFunction(() => {
        const line = document.querySelector("#bind-line");
        return line && !line.textContent.includes("loading");
      });
      await shot(page, "settings.png");
    });
  } finally {
    await stopServe(child);
    if (child.exitCode && child.exitCode !== 0 && child.exitCode !== null) {
      console.error(child.log());
    }
  }
}

async function main() {
  if (!fs.existsSync(bin)) {
    throw new Error("missing peaproxy binary at " + bin + " (build with go build -o peaproxy ./cmd/peaproxy)");
  }
  fs.mkdirSync(outDir, { recursive: true });
  fs.rmSync(shotDir, { recursive: true, force: true });
  fs.mkdirSync(shotDir, { recursive: true, mode: 0o700 });
  const onboardingPath = path.join(shotDir, "onboarding.yaml");
  const demoPath = path.join(shotDir, "config.yaml");
  const mock = await startMock();
  try {
    await captureOnboarding(onboardingPath);
    await captureDemo(demoPath);
  } finally {
    mock.close();
    fs.rmSync(shotDir, { recursive: true, force: true });
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
