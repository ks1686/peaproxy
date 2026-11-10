---
name: verify-peaproxy
description: "Drive PeaProxy the way a user does, through the loopback web UI, the peaproxy CLI, and the local HTTP API. Use to launch an isolated serve, doctor it, and prove Accounts, Catalog, Clients, Showcase, or Policy."
---

# Verify PeaProxy

PeaProxy is a local AI gateway. The user opens the browser UI, runs the `peaproxy` CLI, and points other tools at the OpenAI-compatible HTTP API. This skill drives those three surfaces on an isolated config, port, and client root. It does not log in to a provider.

Read [features/README.md](features/README.md) before a journey. Then follow that feature file. One green entry point does not cover the other entry points in the same file.

## Launch

From the repo root, with a short run id:

```bash
export PEAPROXY_VERIFY_STATE=/tmp/pea-v-$RUN_ID
export PEAPROXY_VERIFY_EVIDENCE=/opt/cursor/artifacts/peaproxy-verify-$RUN_ID
.cursor/skills/verify-peaproxy/scripts/verify-peaproxy launch
```

Both paths must be absolute, must not contain whitespace, and the state directory's basename must start with `pea-v-`. The evidence directory must be outside the state directory and outside the git repo. Two runs need two state directories and two evidence directories.

`launch` builds `./cmd/peaproxy` with `GOTOOLCHAIN=auto` (go.mod requires Go 1.26.9), installs Playwright 1.64.1 under the state directory, and starts `serve`. It writes the first-run config with `config init`, then sets that file's `port` to a free loopback port before `serve` so the file and the listener agree. `peaproxy serve --port` alone does not save the port, and `peaproxy status` probes the port in the file.

Ready means the helper prints `ready` after both of these are true.

- The serve log contains `peaproxy listening on http://127.0.0.1:<port>`.
- `GET /healthz` returns `"status":"ok"`.

The first-run file includes the `ollama-local` skeleton. That is a usable Accounts page, not an empty provider list. Launch does not start Ollama, Jan, or a provider mock.

The helper forces `PEAPROXY_SECRET_BACKEND=file` and `PEAPROXY_CLIENT_ROOT` to the state directory's `client-root`. It unsets `PEAPROXY_BIND`, `PEAPROXY_PORT`, `PEAPROXY_ALLOW_LAN`, `PEAPROXY_ADMIN_TOKEN`, `PEAPROXY_REQUEST_LOG`, `PEAPROXY_FAILOVER_POLICY`, and `PI_CODING_AGENT_DIR` for the serve process and for every `cli` invocation. Do not export those variables in the shell you use to drive the instance.

The serve process, every `cli` invocation, the Go build, npm, and the browser run with `HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, `XDG_DATA_HOME`, and `GOCACHE` under `home` in the state directory. Chrome crash reports, Go telemetry, the Go build cache, and the fontconfig cache stay there and are deleted with the state directory. The module cache stays the existing `GOMODCACHE` so the toolchain is reused.

## Doctor

Run this before the first drive, after any failed drive, and whenever the page or the CLI looks wrong.

```bash
.cursor/skills/verify-peaproxy/scripts/verify-peaproxy doctor
```

Doctor is read-only. It exits 0 only when all of these hold.

- The recorded pid is alive and its command line is this binary's `serve` with this config file.
- That pid owns the `127.0.0.1` listen socket for the recorded port.
- `GET /healthz` reports `status` ok, bind `127.0.0.1`, the recorded port, `adminTokenRequired` false, and the same version string as `<binary> --version` after the `peaproxy ` prefix.
- `GET /admin/settings` reports that config path, port, bind, `secretBackend` `file`, and `loopback` true.
- `peaproxy status` prints `phase: running`, the same listen address, and the same config path.

A dirty git tree builds as version `dev`. Doctor compares the running binary with `/healthz`. It does not expect a release tag.

If doctor fails, stop driving. Fix the launch, or run cleanup and launch again. Do not point the browser at a different port.

## Drive

The harness is the helper script. UI commands talk to one Chrome window that `browser start` opened on this serve. CLI commands talk to the same config file. HTTP checks talk to this serve's base URL.

```bash
.cursor/skills/verify-peaproxy/scripts/verify-peaproxy env
.cursor/skills/verify-peaproxy/scripts/verify-peaproxy browser start
.cursor/skills/verify-peaproxy/scripts/verify-peaproxy browser click --role button --name "Accounts"
.cursor/skills/verify-peaproxy/scripts/verify-peaproxy browser click --selector '[data-connect="claude-code"]'
.cursor/skills/verify-peaproxy/scripts/verify-peaproxy cli -- accounts list
.cursor/skills/verify-peaproxy/scripts/verify-peaproxy api GET /admin/accounts
.cursor/skills/verify-peaproxy/scripts/verify-peaproxy api wait GET /admin/accounts --contains '"id":"jan-local"' --timeout 15
.cursor/skills/verify-peaproxy/scripts/verify-peaproxy mock start
```

`browser click --role` uses the accessible name, exact match, and requires one match. Use `--selector` for `data-connect`, `data-del`, `data-pin`, `data-hide`, and ids such as `#acc-add`. `browser value --selector "#acc-id"` prints an input's value. `browser wait --text` waits until the page text contains the string. Toasts disappear after about five seconds, so wait for the toast in the next command and then prove the durable state. `api wait` polls twice a second. Use it after a CLI write. The running server adopts the file on a two second poll.

`mock start` serves `GET /v1/models` with id `pea-verify-local` and `POST /v1/chat/completions` with assistant text `pea-verify-hello`. Catalog and Showcase use it as the upstream a local OpenAI-compatible server would be. It is not a PeaProxy endpoint. `env` prints `PEAPROXY_VERIFY_MOCK_BASE` as `http://127.0.0.1:<port>/v1`.

`browser start` tries Playwright's `chrome` channel. On Linux that channel is the file `/opt/google/chrome/chrome`. Playwright does not search `PATH` for `google-chrome` or `chromium`. The helper then tries the channel name `chromium`, which Playwright does not ship, and then launches with no channel. The no-channel launch is Playwright's downloaded Chromium, not a `chromium` binary on `PATH`. The profile directory is `chrome-profile` inside the state directory. It is not the user's Chrome profile.

The UI is `GET /`. Accounts is the first page. Nav buttons are `Accounts`, `Catalog`, `Showcase`, `Clients`, `Policy`, `Health`, `Request log`, and `Settings`. The header tagline always says `127.0.0.1:8317`. Ignore it. The live port is the doctor output and the Settings sentence `Listening on <bind>:<port>`.

## Evidence

Capture the action and the resulting state. A screenshot of the last screen is not enough.

- UI. An ARIA snapshot and a full-page screenshot, taken before the action and after the UI has reached the new state. The page heading `PeaProxy` is the on-screen identity. The doctor transcript is the identity of the process.
- CLI. The command, stdout, stderr, and exit code.
- HTTP. The response body of the user-facing GET that shows the same change.
- Files. A second view of the config file, the client file under `PEAPROXY_VERIFY_CLIENT_ROOT`, or the mock log. Copy them into the evidence directory before cleanup. The serve log is under the state directory and cleanup deletes it, so copy the listening lines across first.

Write screenshots and snapshots only through the helper. It rejects paths inside the state directory or outside the evidence directory.

```bash
.cursor/skills/verify-peaproxy/scripts/verify-peaproxy browser screenshot --path "$PEAPROXY_VERIFY_EVIDENCE/accounts/added.png"
.cursor/skills/verify-peaproxy/scripts/verify-peaproxy browser snapshot --path "$PEAPROXY_VERIFY_EVIDENCE/accounts/added.aria.txt"
```

Copy `serve.log` and `env` output into the evidence directory before cleanup. Record the feature id and the entry point in a small text file next to those artifacts.

Mocks are allowed only at the provider boundary. `mock start` is that boundary for Catalog and Showcase. Do not stub `/admin/oauth/*` or any other PeaProxy route. A journey that needs a real OAuth login or a real API key stays unverified. Say which command you would have run and which credential was missing.

## Cleanup

```bash
cp "$PEAPROXY_VERIFY_STATE/serve.log" "$PEAPROXY_VERIFY_EVIDENCE/serve.log"
.cursor/skills/verify-peaproxy/scripts/verify-peaproxy env > "$PEAPROXY_VERIFY_EVIDENCE/run.env"
.cursor/skills/verify-peaproxy/scripts/verify-peaproxy cleanup
.cursor/skills/verify-peaproxy/scripts/verify-peaproxy evidence check accounts/added.png serve.log run.env
```

Launch stores the serve start time from proc stat field 22 in `run.env` as `PID_START`. `browser start` and `mock start` store `BROWSER_PID_START` and `MOCK_PID_START`. Cleanup sends `stop` to the browser helper, then signals the recorded browser pid, mock pid, and serve pid only when that pid's command line still matches the process this run launched and the recorded start time still matches. On a mismatch it prints a skip message and does not signal that pid. It does not kill by process name. It then deletes the state directory. It leaves the evidence directory in place. `evidence check` fails if a listed file is missing, empty, or outside the evidence directory. Run it after cleanup. Also confirm the recorded serve pid is gone with `kill -0`.

Run cleanup after a failed launch too, so the next launch is not blocked by a leftover pid or `run.env`.

## Helpers

The only command to invoke is the executable script. `browser.mjs` and `mock.mjs` are started by that script.

```bash
.cursor/skills/verify-peaproxy/scripts/verify-peaproxy --help
```

`chmod +x` is already set in git for `scripts/verify-peaproxy`, `scripts/browser.mjs`, and `scripts/mock.mjs`. Invoke the shell script. Do not call the `.mjs` files yourself.

## Maintenance

When the UI, CLI, or routes change, run `/maintain-verification-skill` so the feature map stays tied to the product. This seed covers Accounts, Catalog, Clients, Showcase, and Policy. Health, Request log, Settings, and `auth login` are real surfaces and are not mapped yet.
