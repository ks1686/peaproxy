# Releasing PeaProxy

Push an annotated `v*` tag on `main`. GitHub Actions runs GoReleaser
(linux/darwin/windows × amd64/arm64), publishes GitHub Release archives, and
on stable tags pushes the Homebrew cask to `ks1686/homebrew-tap` when
`HOMEBREW_TAP_GITHUB_TOKEN` is set.

## macOS Developer ID + notarization

Darwin GitHub Release archives (and therefore Homebrew cask payloads) are
signed with **Developer ID Application** and notarized via App Store Connect
(GoReleaser → anchore/quill on `ubuntu-latest`). Bare CLI tools do not get a
stapled ticket the way a `.app`/`.dmg` does; Gatekeeper checks the notarization
ticket online. That is the same pattern as `ks1686/genv`.

Identity (do not substitute Apple Development):

```
Developer ID Application: KARIM SMIRES (7R2VPW8GH4)
```

Team ID: `7R2VPW8GH4`. The **Apple Development** identity is local-only and
must never be used for GitHub Releases or Homebrew.

Without the secrets below, GoReleaser skips `notarize.macos` and ships unsigned
Darwin binaries (previous behavior). GitHub Releases and the Homebrew tap
update still run.

### GitHub Actions secrets (`ks1686/peaproxy`)

Never commit `.p8`, `.p12`, or passphrases. Add these repository secrets
(same names as genv; paste values locally, do not invent them):

| Secret | Contents |
| --- | --- |
| `MACOS_SIGN_P12` | base64 of the Developer ID Application `.p12` (export of `developer-id-application.p12`) |
| `MACOS_SIGN_PASSWORD` | password that opens the `.p12` (Keychain service `developer-id-p12` if that is how it is stored) |
| `MACOS_NOTARY_KEY` | base64 of the App Store Connect API `.p8` (`AuthKey_<KEY_ID>.p8`) |
| `MACOS_NOTARY_KEY_ID` | App Store Connect API Key ID |
| `MACOS_NOTARY_ISSUER_ID` | App Store Connect Issuer UUID |

Already required for tap updates:

| Secret | Contents |
| --- | --- |
| `HOMEBREW_TAP_GITHUB_TOKEN` | fine-grained PAT with Contents: read+write on `ks1686/homebrew-tap` |

GoReleaser’s notarize step maps to the usual ASC API fields
(`APPLE_API_KEY_ID` / `APPLE_API_ISSUER_ID` / `APPLE_API_KEY_PATH` or a
base64 `.p8`): use the `MACOS_NOTARY_*` names above, not those aliases.

Helper (does not print secret values):

```bash
./scripts/set-macos-signing-secrets.sh \
  --p12 /path/to/developer-id-application.p12 \
  --p12-password '…' \
  --p8 /path/to/AuthKey_XXXXXXXXXX.p8 \
  --key-id XXXXXXXXXX \
  --issuer-id 00000000-0000-0000-0000-000000000000
```

### Verify a Darwin artifact after the next release

```bash
codesign -dv --verbose=4 ./peaproxy
# Expect: Authority=Developer ID Application: KARIM SMIRES (7R2VPW8GH4)
#         TeamIdentifier=7R2VPW8GH4

# Bare CLI tools often report "does not seem to be an app" from spctl -a;
# that is normal. Prefer exec assessment / successful launch under quarantine:
spctl -a -t exec -vv ./peaproxy
./peaproxy --version
```

Local `go install` / `go run` builds are unsigned. Only tagged Release /
Homebrew darwin artifacts go through Developer ID + notary.

## Dry-run packaging

```bash
goreleaser check
goreleaser release --clean --snapshot
```

Artifacts land in `./dist/`. Nothing is published. Snapshot skips notarization
unless `MACOS_SIGN_P12` is set in the environment.
