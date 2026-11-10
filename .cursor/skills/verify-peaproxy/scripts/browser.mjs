#!/usr/bin/env node
import fs from "node:fs";
import net from "node:net";
import path from "node:path";
import { pathToFileURL } from "node:url";

const mode = process.argv[2];

function readRunEnv(file) {
  const out = {};
  for (const line of fs.readFileSync(file, "utf8").split("\n")) {
    if (!line || line.startsWith("#")) continue;
    const eq = line.indexOf("=");
    if (eq < 0) continue;
    out[line.slice(0, eq)] = line.slice(eq + 1);
  }
  return out;
}

function fail(error) {
  return { ok: false, error: String(error && error.message ? error.message : error) };
}

if (mode === "call") {
  const sock = process.argv[3];
  const payload = process.argv[4];
  const client = net.createConnection(sock);
  let buf = "";
  client.on("connect", () => client.write(payload + "\n"));
  client.on("data", (chunk) => {
    buf += chunk.toString("utf8");
    const nl = buf.indexOf("\n");
    if (nl < 0) return;
    const msg = JSON.parse(buf.slice(0, nl));
    if (msg.text) process.stdout.write(msg.text.endsWith("\n") ? msg.text : msg.text + "\n");
    if (!msg.ok) {
      process.stderr.write((msg.error || "browser command failed") + "\n");
      process.exit(1);
    }
    process.exit(0);
  });
  client.on("error", (err) => {
    process.stderr.write(err.message + "\n");
    process.exit(1);
  });
} else if (mode === "daemon") {
  const stateDir = process.argv[3];
  const run = readRunEnv(path.join(stateDir, "run.env"));
  const base = run.BASE;
  const evidence = run.EVIDENCE;
  const sock = path.join(stateDir, "browser.sock");
  const profile = path.join(stateDir, "chrome-profile");
  const playwrightPath = path.join(stateDir, "harness/node_modules/playwright/index.mjs");
  if (!base || !evidence) {
    process.stderr.write("run.env is missing BASE or EVIDENCE\n");
    process.exit(1);
  }
  if (!fs.existsSync(playwrightPath)) {
    process.stderr.write("playwright is not installed in the state dir. Run launch.\n");
    process.exit(1);
  }

  const { chromium } = await import(pathToFileURL(playwrightPath).href);
  fs.mkdirSync(profile, { recursive: true });
  fs.rmSync(sock, { force: true });

  const launchOpts = {
    headless: true,
    viewport: { width: 1280, height: 900 },
    args: ["--no-sandbox", "--disable-dev-shm-usage", "--disable-gpu"],
  };
  let context;
  let lastError = "";
  for (const channel of ["chrome", "chromium"]) {
    try {
      context = await chromium.launchPersistentContext(profile, { ...launchOpts, channel });
      lastError = "";
      break;
    } catch (err) {
      lastError = String(err && err.message ? err.message : err);
    }
  }
  if (!context) {
    try {
      context = await chromium.launchPersistentContext(profile, launchOpts);
    } catch (err) {
      process.stderr.write(
        "could not launch Chrome. Install google-chrome, or install Playwright's chromium. " +
          (lastError || String(err && err.message ? err.message : err)) +
          "\n",
      );
      process.exit(1);
    }
  }
  const page = context.pages()[0] || (await context.newPage());
  page.setDefaultTimeout(10000);

  function describe(cmd) {
    if (cmd.selector) return cmd.selector;
    return `${cmd.role || "element"} ${cmd.name || ""}`.trim();
  }

  function locator(cmd) {
    if (cmd.selector) return page.locator(cmd.selector);
    if (cmd.role && cmd.name) return page.getByRole(cmd.role, { name: cmd.name, exact: true });
    throw new Error("command needs --selector or --role and --name");
  }

  function assertEvidencePath(file) {
    if (!file) throw new Error("path is required");
    const parent = path.dirname(path.resolve(file));
    fs.mkdirSync(parent, { recursive: true });
    const resolvedParent = fs.realpathSync(parent);
    const resolvedEvidence = fs.realpathSync(evidence);
    const resolvedState = fs.realpathSync(stateDir);
    const target = path.join(resolvedParent, path.basename(file));
    if (target === resolvedState || target.startsWith(resolvedState + path.sep)) {
      throw new Error("refusing to write proof inside the state directory");
    }
    if (!(target === resolvedEvidence || target.startsWith(resolvedEvidence + path.sep))) {
      throw new Error("proof path must be inside the evidence directory");
    }
    return target;
  }

  async function readyHeading() {
    await page.getByRole("heading", { name: "PeaProxy", level: 1 }).waitFor();
  }

  async function handle(cmd) {
    const timeout = cmd.timeout || 10000;
    if (cmd.op === "ping") return { ok: true };
    if (cmd.op === "stop") return { ok: true, stop: true };
    if (cmd.op === "goto") {
      const url = new URL(cmd.path || "/", base).href;
      await page.goto(url, { waitUntil: "domcontentloaded" });
      await readyHeading();
      return { ok: true, text: page.url() };
    }
    if (cmd.op === "reload") {
      await page.reload({ waitUntil: "domcontentloaded" });
      await readyHeading();
      return { ok: true, text: page.url() };
    }
    if (cmd.op === "click" || cmd.op === "fill" || cmd.op === "select" || cmd.op === "text" || cmd.op === "value") {
      const loc = locator(cmd);
      const count = await loc.count();
      if (count !== 1) throw new Error(`${cmd.op} expected 1 match, found ${count} for ${describe(cmd)}`);
      if (cmd.op === "click") {
        await loc.click();
        return { ok: true };
      }
      if (cmd.op === "fill") {
        await loc.fill(cmd.value ?? "");
        return { ok: true };
      }
      if (cmd.op === "select") {
        await loc.selectOption(cmd.value ?? "");
        return { ok: true };
      }
      if (cmd.op === "value") return { ok: true, text: await loc.inputValue() };
      return { ok: true, text: await loc.innerText() };
    }
    if (cmd.op === "wait") {
      if (cmd.text) {
        await page.waitForFunction((needle) => document.body.innerText.includes(needle), cmd.text, { timeout });
        return { ok: true };
      }
      const loc = locator(cmd);
      await loc.waitFor({ state: "visible", timeout });
      return { ok: true };
    }
    if (cmd.op === "absent") {
      if (cmd.text) {
        const count = await page.getByText(cmd.text, { exact: false }).count();
        if (count !== 0) throw new Error(`expected text absent, found ${count} for ${cmd.text}`);
        return { ok: true };
      }
      const count = await locator(cmd).count();
      if (count !== 0) throw new Error(`expected selector absent, found ${count} for ${describe(cmd)}`);
      return { ok: true };
    }
    if (cmd.op === "snapshot") {
      const file = assertEvidencePath(cmd.path);
      const text = await page.locator("body").ariaSnapshot();
      fs.writeFileSync(file, text.endsWith("\n") ? text : text + "\n");
      return { ok: true, text: file };
    }
    if (cmd.op === "screenshot") {
      const file = assertEvidencePath(cmd.path);
      await page.screenshot({ path: file, fullPage: true });
      return { ok: true, text: file };
    }
    throw new Error("unknown browser op " + cmd.op);
  }

  const server = net.createServer((socket) => {
    let buf = "";
    socket.on("data", (chunk) => {
      buf += chunk.toString("utf8");
      let nl;
      while ((nl = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, nl);
        buf = buf.slice(nl + 1);
        Promise.resolve()
          .then(() => handle(JSON.parse(line)))
          .then((result) => {
            socket.write(JSON.stringify(result) + "\n");
            if (result.stop) {
              socket.end();
              shutdown().finally(() => process.exit(0));
            }
          })
          .catch((err) => {
            socket.write(JSON.stringify(fail(err)) + "\n");
          });
      }
    });
  });

  async function shutdown() {
    server.close();
    try {
      await context.close();
    } catch {
      // The browser is already gone.
    }
    fs.rmSync(sock, { force: true });
  }

  process.on("SIGTERM", () => {
    shutdown().finally(() => process.exit(0));
  });
  process.on("SIGINT", () => {
    shutdown().finally(() => process.exit(0));
  });

  server.listen(sock);
} else {
  process.stderr.write("usage: browser.mjs call <sock> <json> | browser.mjs daemon <state>\n");
  process.exit(1);
}
