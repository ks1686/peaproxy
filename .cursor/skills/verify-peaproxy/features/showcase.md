# Showcase chat

Showcase sends the prompt in the box to the selected model and shows the assistant text. The usage panel updates from the same serve.

## Sub-features

- `showcase-send` sends the default prompt and shows the upstream reply.
- `showcase-usage` leaves a usage record that the page and `GET /admin/usage` both show.
- `showcase-image` and `showcase-embed` stay hidden for a text model.

## How to get to it (user POV)

- Add an account that lists a chat model, then choose `Showcase`.
- Leave the prompt as written, or type a new one, and choose `Send`.

## Driving it with verify-peaproxy

Preconditions:

- Complete the Catalog steps that add account `verify-local` pointed at `PEAPROXY_VERIFY_MOCK_BASE`, through the `Account added` result. The model does not need to be pinned or hidden.
- The mock is still running.
- Doctor still prints `ok`.

- **Open Showcase.** Choose `Showcase`. Run `verify-peaproxy browser click --role button --name "Showcase"` and `verify-peaproxy browser wait --selector "#show-model"`. The model select contains an option whose value is `pea-verify-local`.
- **Select the model.** Run `verify-peaproxy browser select --selector "#show-model" --value "pea-verify-local"`.
- **Send.** The prompt box `#show-prompt` already says `Say hello in one short sentence.` Choose `Send`. Run `verify-peaproxy browser click --role button --name "Send"`, then `verify-peaproxy browser wait --text "pea-verify-hello"`. Run `verify-peaproxy browser text --selector "#show-out"`. It prints `pea-verify-hello`.
- **Upstream.** `PEAPROXY_VERIFY_MOCK_LOG` contains `POST /v1/chat/completions`.
- **Usage.** The page contains `Usage by provider`. Run `verify-peaproxy api GET /admin/usage`. The HTTP status is 200 and the body is JSON.
- **Proof.** Capture `verify-peaproxy browser screenshot --path "$PEAPROXY_VERIFY_EVIDENCE/showcase/reply.png"` and `verify-peaproxy browser snapshot --path "$PEAPROXY_VERIFY_EVIDENCE/showcase/reply.aria.txt"` while `#show-out` shows `pea-verify-hello`. Copy the mock log into the evidence directory.

## Gotchas

- `Send` is hidden for models tagged `image_out` or `embeddings`. `pea-verify-local` is a text id, so `Send` stays visible. Proving image generation or embeddings needs a model the catalog tags that way and an upstream that implements `/v1/images/generations` or `/v1/embeddings`. This mock does neither. Those two buttons stay unverified.
- An empty catalog shows `Nothing to try yet` and disables `Send`. Add the account and wait until the select lists `pea-verify-local` before sending.
- The reply text comes from the mock. A run without `mock start` is not this journey.
- Subscription OAuth accounts are not a stand-in for this chat. Do not start an OAuth login to populate Showcase.
