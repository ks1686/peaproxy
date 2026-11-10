# Connect a client

Connect a client writes a managed harness config under the isolated client root, or tells the user to copy a snippet when PeaProxy cannot write that harness. Disconnect removes only the PeaProxy block.

## Sub-features

- `clients-connect-claude` writes Claude Code settings that point at this serve.
- `clients-disconnect-claude` removes those settings.
- `clients-opencode-guided` tells the user to paste a snippet and writes no file.
- `clients-cursor` offers no Connect button.

## How to get to it (user POV)

- Choose `Clients` in the nav.
- Choose `Connect` on the Claude Code card.
- Choose `Disconnect` on that card.
- Choose `Connect` on the OpenCode card.
- Run `peaproxy clients status --root <client-root>` and read the same files.

## Driving it with verify-peaproxy

Preconditions:

- Doctor prints `ok`.
- `PEAPROXY_VERIFY_CLIENT_ROOT` from `verify-peaproxy env` exists and contains no `.claude/settings.json` and no `opencode.json`.
- The browser is on this serve.

- **Open Clients.** Choose `Clients`. Run `verify-peaproxy browser click --role button --name "Clients"`. A heading `claude-code` is visible and `[data-connect="claude-code"]` is visible.
- **No Cursor connect.** Run `verify-peaproxy browser absent --selector '[data-connect="cursor"]'`.
- **Connect Claude Code.** Choose Connect on that card. Run `verify-peaproxy browser click --selector '[data-connect="claude-code"]'`. The page text contains `Connected claude-code`. The button text is `Connected`.
- **File view.** `$PEAPROXY_VERIFY_CLIENT_ROOT/.claude/settings.json` contains `ANTHROPIC_BASE_URL` set to `PEAPROXY_VERIFY_BASE` with no `/v1` suffix, and `ANTHROPIC_API_KEY` set to `peaproxy`.
- **CLI view.** Run `verify-peaproxy cli -- clients status --root "$PEAPROXY_VERIFY_CLIENT_ROOT"`. Exit code 0. Stdout contains `claude-code` and the settings path above.
- **Disconnect.** Choose Disconnect on the Claude Code card. Run `verify-peaproxy browser click --selector '[data-disconnect="claude-code"]'`. The page text contains `Disconnected claude-code`. The settings file no longer contains `ANTHROPIC_BASE_URL`.
- **OpenCode.** Choose Connect on the OpenCode card. Run `verify-peaproxy browser click --selector '[data-connect="opencode"]'`. The page text contains `needs manual setup`. `$PEAPROXY_VERIFY_CLIENT_ROOT/opencode.json` does not exist.
- **Proof.** Capture `verify-peaproxy browser screenshot --path "$PEAPROXY_VERIFY_EVIDENCE/clients/connected.png"` and `verify-peaproxy browser snapshot --path "$PEAPROXY_VERIFY_EVIDENCE/clients/connected.aria.txt"` after the Claude Code connect and before disconnect. Copy the settings file into the evidence directory as well.

## Gotchas

- Several cards say `Connect`. Click `[data-connect="claude-code"]` or `[data-connect="opencode"]`. A role click on the name `Connect` is ambiguous and the helper rejects it.
- The value `peaproxy` in `ANTHROPIC_API_KEY` is the marker PeaProxy writes. It is not a provider secret.
- Claude Code's base URL must not end in `/v1`. A URL with `/v1` is the wrong wire for that client.
- `Check` on a card only asks the server for the verify hint. It does not send a chat. `peaproxy clients verify <name> --chat` is a live request and needs a working model. Do not treat the Check toast as a completed chat.
- The helper unsets `PI_CODING_AGENT_DIR` for serve and for `cli`. A Pi connect outside this helper can write outside the client root when that variable is set.
- Disconnect must leave any non-PeaProxy keys in a pre-existing file. This recipe starts from an empty client root, so the proof is that the PeaProxy keys are gone.
