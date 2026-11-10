# Add an account

Add an account lets a user attach the Jan local preset from the browser or the CLI, see it in the accounts table, and remove that row without removing the first-run Ollama skeleton.

## Sub-features

- `accounts-onboarding` shows Getting started while the only account is the first-run `ollama-local` skeleton.
- `accounts-add-jan` selects Jan local and saves it from the Add button.
- `accounts-persist` shows the same row after a reload, from `accounts list`, from `GET /admin/accounts`, and in the config file.
- `accounts-remove` removes only the Jan row.
- `accounts-cli` adds the same preset from the terminal and the running UI adopts it.
- `accounts-oauth` would start a provider login. It is not part of the driven steps below.

## How to get to it (user POV)

- Open the serve URL. Accounts is already showing.
- Choose `Accounts` in the nav.
- Choose `Jan local` in Getting started, then choose `Add`.
- Run `peaproxy accounts add jan-local` in a terminal against the same config.

## Driving it with verify-peaproxy

Preconditions:

- Doctor prints `ok` for this state directory.
- `GET /admin/accounts` contains `ollama-local` and does not contain `jan-local`.
- No Jan server is required. This journey proves the account was saved, not that Jan answered.

- **Onboarding.** Open the UI. Run `verify-peaproxy browser start`, then `verify-peaproxy browser wait --role heading --name "Add account"` and `verify-peaproxy browser wait --role button --name "Jan local"`. The accounts table contains `ollama-local`.
- **Select Jan.** Choose `Jan local`. Run `verify-peaproxy browser click --role button --name "Jan local"`. The page text contains `Preset selected`. Run `verify-peaproxy browser value --selector "#acc-id"`. It prints `jan-local`.
- **Save.** Choose `Add`. Run `verify-peaproxy browser click --role button --name "Add"`. The page text contains `Account added`, and a control `[data-del="jan-local"]` is visible. Capture `verify-peaproxy browser screenshot --path "$PEAPROXY_VERIFY_EVIDENCE/accounts/added.png"` and `verify-peaproxy browser snapshot --path "$PEAPROXY_VERIFY_EVIDENCE/accounts/added.aria.txt"`.
- **Reload.** Run `verify-peaproxy browser reload` and `verify-peaproxy browser wait --selector '[data-del="jan-local"]'`. The Jan row is still there.
- **HTTP view.** Run `verify-peaproxy api GET /admin/accounts`. The body contains `"id":"jan-local"` and `"id":"ollama-local"`.
- **CLI view.** Run `verify-peaproxy cli -- accounts list`. Exit code 0. Stdout contains `jan-local`, `adapter=jan`, `tier=local`, and `base=http://127.0.0.1:1337/v1`.
- **File view.** The config file from `verify-peaproxy env` (`PEAPROXY_VERIFY_CONFIG`) contains `id: jan-local`.
- **Remove.** Choose `Remove` on the Jan row. Run `verify-peaproxy browser click --selector '[data-del="jan-local"]'`. The page text contains `Account removed`. Run `verify-peaproxy browser absent --selector '[data-del="jan-local"]'`. `accounts list` and `GET /admin/accounts` no longer contain `jan-local`. `ollama-local` remains.
- **CLI entry.** Run `verify-peaproxy cli -- accounts add jan-local`. Exit code 0. Stdout contains `added jan-local` and `adapter=jan`. Run `verify-peaproxy api wait GET /admin/accounts --contains '"id":"jan-local"' --timeout 15`. Reload the Accounts page. `[data-del="jan-local"]` is visible. Capture a second screenshot at `$PEAPROXY_VERIFY_EVIDENCE/accounts/added-from-cli.png`.
- **CLI cleanup.** Remove the Jan row again with `[data-del="jan-local"]` so the config returns to the Ollama skeleton.

## Gotchas

- The header tagline always says `127.0.0.1:8317`. Prove the instance with doctor, not with that line.
- `Jan local` is the onboarding button. `Add` is `#acc-add`. After a successful add there is one `Remove` per row. Target `[data-del="jan-local"]`.
- Adding Jan does not start Jan. An adapter error for `127.0.0.1:1337` does not mean the add failed. Confirm the row, the CLI line, and the config file.
- The Ollama skeleton stays for the life of this config. Do not treat `ollama-local` as the account under test.
- A CLI add shows up in the UI only after the server reloads the file. Wait on `GET /admin/accounts` instead of sleeping a fixed time.
- `OAuth login` and `Copy CLI` open a real provider login. Do not click them here. That path stays unverified without a provider account.
- Pasting an API key is a different journey and stays unverified without a real key. The helper must keep `secret_backend: file` so a key never goes to the OS keychain.
- The `Account added` toast disappears after about five seconds. The row and the config file are the proof.
