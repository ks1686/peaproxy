# Cost policy

Cost policy shows the switches PeaProxy is applying and the spend ceiling it will enforce. On a fresh config the user can read those defaults without changing them. The CLI prints the same resolution.

## Sub-features

- `policy-defaults` shows automatic optimizations on, free-only off, local assistant off, and no spend ceiling.
- `policy-cli` prints the same facts from `optimization status`.

## How to get to it (user POV)

- Choose `Policy` in the nav.
- Run `peaproxy optimization status` against the same config.

## Driving it with verify-peaproxy

Preconditions:

- Doctor prints `ok`.
- The config is the fresh file from launch. Do not set a spend ceiling first. If one is set, this recipe does not describe that config.

- **Open Policy.** Choose `Policy`. Run `verify-peaproxy browser click --role button --name "Policy"` and `verify-peaproxy browser wait --text "Automatic optimizations"`. The heading `Cost policy` is visible.
- **Defaults.** The policy panel text includes `Automatic optimizations`, `on`, `Free only`, `off`, `Context optimization`, `on`, `Local assistant`, `off`, `none set`, and `Spent (30d)`.
- **CLI.** Run `verify-peaproxy cli -- optimization status`. Exit code 0. Stdout contains `automatic: true`, `freeOnly: false`, `contextOptimization: true`, `localAssistant: false`, `spendCeilingUSD: unset`, and `config: ` followed by `PEAPROXY_VERIFY_CONFIG`.
- **HTTP.** Run `verify-peaproxy api GET /admin/policy`. The body contains `"freeOnly":false` and `"spendCeilingUSD":null`.
- **Proof.** Capture `verify-peaproxy browser screenshot --path "$PEAPROXY_VERIFY_EVIDENCE/policy/defaults.png"` and `verify-peaproxy browser snapshot --path "$PEAPROXY_VERIFY_EVIDENCE/policy/defaults.aria.txt"`. Save the CLI stdout beside them.

## Gotchas

- `none set` is the UI wording for an absent ceiling. `0` would be a real budget of zero. Do not accept a zero in place of `none set`.
- `optimization explain <model>` refreshes the live catalog. It is read-only, and it is not this journey.
- This page does not edit the switches. A journey that turns free-only or a ceiling on is a different feature and is not mapped yet.
- The dollar figure for `Spent (30d)` on a fresh file is `$0.00` because there have been no calls. After Showcase has sent a chat, the panel can show a non-zero or estimated figure. Read the ceiling line, not the spent line, to prove the ceiling is unset.
