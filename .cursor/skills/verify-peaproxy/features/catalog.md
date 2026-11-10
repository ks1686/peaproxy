# Catalog pin and hide

Catalog shows the live model list for the accounts on this serve. A user can pin a model and hide it from `GET /v1/models` without removing it from the catalog or from routing.

## Sub-features

- `catalog-list` shows a model published by a local OpenAI-compatible server.
- `catalog-pin` marks that model pinned in the table and in `models list`.
- `catalog-hide` drops it from `GET /v1/models` and keeps it on the Catalog page.
- `catalog-still-routes` still completes a chat to that model id after the hide.

## How to get to it (user POV)

- Add a Custom OpenAI-compat account whose base URL is a local server, then choose `Catalog`.
- Choose `Pin` on that model row.
- Choose `Hide from /v1/models` on that row.
- Ask for the hidden id on `POST /v1/chat/completions`.

## Driving it with verify-peaproxy

Preconditions:

- Doctor prints `ok`.
- Run `verify-peaproxy mock start`. `PEAPROXY_VERIFY_MOCK_BASE` is `http://127.0.0.1:<port>/v1` and the mock log path is `PEAPROXY_VERIFY_MOCK_LOG`.
- `verify-peaproxy cli -- config show` prints `hide.blockRouting: false`.
- No account id `verify-local` exists yet.

- **Add the local server.** Open Accounts with `verify-peaproxy browser start`. Run `verify-peaproxy browser select --selector "#preset" --value "custom"`. Run `verify-peaproxy browser fill --selector "#acc-id" --value "verify-local"`. Run `verify-peaproxy browser fill --selector "#acc-url" --value "$PEAPROXY_VERIFY_MOCK_BASE"`. Run `verify-peaproxy browser select --selector "#acc-tier" --value "local"`. Run `verify-peaproxy browser click --role button --name "Add"`. The page text contains `Account added`.
- **Open Catalog.** Choose `Catalog`. Run `verify-peaproxy browser click --role button --name "Catalog"` and `verify-peaproxy browser wait --text "pea-verify-local"`. The model id is visible in the catalog table.
- **Pin.** Choose `Pin` for that id. Run `verify-peaproxy browser click --selector '[data-pin="pea-verify-local"]'`. The row shows `pin` and the button name becomes `Unpin`.
- **CLI pin.** Run `verify-peaproxy cli -- models list`. Exit code 0. Stdout contains `pea-verify-local` and a `pin` column on that line. `verify-peaproxy cli -- config show` prints `catalog.pin: 1`.
- **Hide.** Choose `Hide from /v1/models`. Run `verify-peaproxy browser click --selector '[data-hide="pea-verify-local"]'`. The catalog table still contains `pea-verify-local`.
- **Listing.** Run `verify-peaproxy api GET /v1/models`. The body does not contain `pea-verify-local`.
- **Still routes.** Send a chat as a client would. Run `curl -sS -H 'Content-Type: application/json' -d '{"model":"pea-verify-local","messages":[{"role":"user","content":"hi"}]}' "$PEAPROXY_VERIFY_BASE/v1/chat/completions"`. The body contains `pea-verify-hello`. `PEAPROXY_VERIFY_MOCK_LOG` contains `POST /v1/chat/completions`.
- **Proof.** Capture `verify-peaproxy browser screenshot --path "$PEAPROXY_VERIFY_EVIDENCE/catalog/hidden.png"` and `verify-peaproxy browser snapshot --path "$PEAPROXY_VERIFY_EVIDENCE/catalog/hidden.aria.txt"` while the catalog still shows the hidden id.

## Gotchas

- Fill the id and the base URL after selecting the `custom` preset. Changing the preset again overwrites both fields.
- The mock base must end in `/v1`. The model id is `pea-verify-local`. The account id is `verify-local`. They are not the same string.
- The Ollama skeleton can show an unavailable row. Assert `pea-verify-local`, not the first row.
- Hide on a fresh config is listing-only. If `hide.blockRouting` is true, the chat step is a different product setting and this recipe does not apply.
- Pin and hide from the CLI while serve is running are adopted on the same two second file poll as other CLI writes. Prefer the buttons above so the browser is the actor. Use `models list` and `GET /v1/models` as the second view.
- `Refresh live list` calls the upstream again. The mock must still be running.
- Do not point this account at a real provider. The mock is the stand-in for a local server you already run.
