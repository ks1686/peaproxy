# PeaProxy verification map

This directory is the maintained source for verifying the user-facing behavior of PeaProxy. Read the index before driving the app, then use the matching feature file as the recipe.

## Baseline preconditions

- Launch with `verify-peaproxy launch` so the config file, port, secret store, and client root are disposable.
- Set `PEAPROXY_VERIFY_STATE=/tmp/pea-v-$RUN_ID` and `PEAPROXY_VERIFY_EVIDENCE` to a directory outside the repo and outside the state directory.
- Run `verify-peaproxy doctor` and require `ok`, the expected base URL, config path, and `secret_backend: file`.
- The first-run config contains the `ollama-local` skeleton. That account is not one you added.
- Never drive an instance that was not started by this verification run.
- Do not export `PEAPROXY_PORT`, `PEAPROXY_BIND`, or `PEAPROXY_CLIENT_ROOT` in the driving shell. The helper sets the isolated values itself.

## Driving conventions

- Start every recipe from the baseline state unless its preconditions say otherwise.
- Prefer accessible names. Use a `data-*` selector or an element id when several controls share one name.
- Treat every command as literal. Keep quoted names and flags unchanged.
- Run browser actions through `verify-peaproxy browser`.
- Run terminal actions through `verify-peaproxy cli --`.
- Run HTTP reads through `verify-peaproxy api`.
- Start `verify-peaproxy mock start` only when the feature names that upstream.
- Restore the baseline after a mutation when a later step in the same feature needs it. Do not remove proof artifacts during cleanup.

## Proof and skip reporting

- Capture the user action and the resulting state, not only the final screen.
- UI proof includes an ARIA snapshot and a screenshot with the `PeaProxy` heading visible.
- CLI proof includes the command, stdout, stderr, and exit code.
- HTTP proof includes the response body.
- Mutation proof includes a second user-facing view of the stored value.
- Record the feature id and entry point used with every artifact.
- Report an unreachable path with the attempted command and the unmet precondition.
- Do not report a skipped entry point as verified through a different path.

## Feature entry contract

Each feature file starts with an H1 title and one paragraph describing the user-visible behavior. It then uses exactly four H2 sections in this order.

1. `Sub-features` lists short IDs with one line for each behavior.
2. `How to get to it (user POV)` lists every user entry point.
3. `Driving it with verify-peaproxy` starts with `Preconditions:` and uses labeled bullets that pair each user action with an exact command and observable result.
4. `Gotchas` lists traps that can waste or invalidate a verification run.

Keep implementation details out of the map. Name only user paths, stable handles, required state, commands, and observable proof.

## Features

- [Add an account](./accounts.md) covers the Accounts page, the Jan local preset, persistence, removal, and `accounts add`.
- [Catalog pin and hide](./catalog.md) covers the live model list, pin, and listing-only hide against a local OpenAI-compatible server.
- [Connect a client](./clients.md) covers Claude Code connect and disconnect, and the OpenCode manual-setup path.
- [Showcase chat](./showcase.md) covers sending a prompt and reading the assistant reply.
- [Cost policy](./policy.md) covers the Policy page and `optimization status`.

Health, Request log, Settings, and subscription OAuth login are user-facing and are not feature files yet. Do not mark them verified by driving one of the files above.
