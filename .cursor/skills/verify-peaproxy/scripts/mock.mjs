#!/usr/bin/env node
// Local OpenAI-compat stand-in for Catalog and Showcase.
// It is a provider boundary, not a PeaProxy test endpoint.
import fs from "node:fs";
import http from "node:http";

const port = Number(process.argv[2]);
const logPath = process.argv[3];
if (!port || !logPath) {
  process.stderr.write("usage: mock.mjs <port> <log>\n");
  process.exit(1);
}

const modelId = "pea-verify-local";
const reply = "pea-verify-hello";

const server = http.createServer((req, res) => {
  const chunks = [];
  req.on("data", (chunk) => chunks.push(chunk));
  req.on("end", () => {
    fs.appendFileSync(logPath, `${req.method} ${req.url}\n`);
    const url = req.url || "";
    if (req.method === "GET" && (url === "/v1/models" || url === "/models")) {
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify({ data: [{ id: modelId }] }));
      return;
    }
    if (req.method === "POST" && (url === "/v1/chat/completions" || url === "/chat/completions")) {
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify({ choices: [{ message: { role: "assistant", content: reply } }] }));
      return;
    }
    res.writeHead(404);
    res.end();
  });
});

server.listen(port, "127.0.0.1");
