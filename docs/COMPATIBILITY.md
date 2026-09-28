# Compatibility metadata

PeaProxy can read a small signed document that describes known client and cache behavior. It does not add model ids. Live `ListModels` remains the catalog.

The document schema is version 1:

```json
{
  "schema": 1,
  "profiles": {
    "anthropic-claude": {
      "tools": "yes",
      "vision": "yes",
      "cache": "preserve"
    }
  }
}
```

Rules:

- `schema` must be 1. Any other value is rejected and the previous good document stays.
- A top-level `models` field is rejected. Metadata cannot create catalog rows.
- Updates are verified with Ed25519 before they replace the file. Unsigned bytes are refused.
- The replacement is written to a temporary file and renamed into place.
- Nothing fetches this document on its own. Remote auto-update stays off.

The built-in document only records the Anthropic Claude profile used by prompt-cache optimize mode.
