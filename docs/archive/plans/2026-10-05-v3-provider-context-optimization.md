
### T10 executor

The multi-round loop is wired into the non-streaming chat path. When a response
contains a `pea_search` call, PeaProxy answers it from the session store, appends
the result as a tool message, and re-sends. Rounds are capped, and the cap is
enforced by the model having to ask again and again rather than by refusing the
fourth.

Two decisions worth stating plainly:

**Streaming clients are not offered the tool.** Answering a tool call
mid-stream would mean buffering the response, which defeats the incremental
delivery the client asked for. Streaming is detected from the request body
rather than passed in by the call site, so no caller can forget it; those
requests get pre-retrieval instead.

**Tool rounds do not consume the request attempt budget.** That budget exists to
stop a retry storm when a provider is failing. A search round is a successful
call, and charging it to the same budget stopped legitimate searches after one
round. Separating them fixed a failure the first test run produced.
