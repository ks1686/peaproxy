# Releasing PeaProxy

Push an annotated `v*` tag on `main`. The `release` workflow first runs the
whole `ci` workflow as its `ci` job (the test matrix on Linux, macOS and
Windows, the race job with `goreleaser check`, and the UI smoke test). The
`goreleaser` job `needs: ci`, so nothing is built or published unless all of
it passes. GoReleaser then builds linux/darwin/windows × amd64/arm64,
publishes GitHub Release archives, and on stable tags pushes the Homebrew cask
to `ks1686/homebrew-tap` when `HOMEBREW_TAP_GITHUB_TOKEN` is set. The same tag
also publishes a Scoop manifest to `ks1686/scoop-bucket`
(`SCOOP_BUCKET_GITHUB_TOKEN`) and the AUR package `peaproxy-bin` (`AUR_KEY`).
Each publisher skips itself when its secret is empty, so a release never
breaks over a missing token.

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

Already required for the package publishers:

| Secret | Contents |
| --- | --- |
| `HOMEBREW_TAP_GITHUB_TOKEN` | fine-grained PAT with Contents: read+write on `ks1686/homebrew-tap` |
| `SCOOP_BUCKET_GITHUB_TOKEN` | fine-grained PAT with Contents: read+write on `ks1686/scoop-bucket` |
| `AUR_KEY` | passphrase-free ed25519 private key for the AUR account (see below) |

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

## Scoop (`ks1686/scoop-bucket`)

GoReleaser's `scoops` block writes `peaproxy.json` to the root of
`ks1686/scoop-bucket` on stable tags. Both Windows zips land under
`architecture` (`64bit` + `arm64`) with their own sha256, so an arm64 machine
installs natively instead of falling back to x64 emulation. No `persist` is
declared: config lives in `%AppData%\peaproxy`, outside the Scoop app dir, so
`scoop uninstall` cannot take your accounts or secrets with it.

The bucket repo already exists; GoReleaser does not create it and the release
workflow's `GITHUB_TOKEN` cannot push to another repository. One-time:

1. Add `SCOOP_BUCKET_GITHUB_TOKEN` to `ks1686/peaproxy` → Settings → Secrets
   and variables → Actions: a fine-grained PAT with **Contents: read+write** on
   `ks1686/scoop-bucket` only. Do not reuse the Homebrew token unless it is
   also granted on that repo, and never paste a `gh` OAuth token (`gho_…`).

   ```bash
   gh secret set SCOOP_BUCKET_GITHUB_TOKEN < /path/to/token.txt
   ```

2. Leave `scoops.directory` unset so the manifest stays at the bucket root
   (`scoop install peaproxy` cannot find it otherwise). Do not add the
   `scoop-bucket` GitHub topic — that lists the bucket on scoop.sh.

With the secret missing, GoReleaser logs `skip_upload is set` and the rest of
the release still publishes.

## AUR (`peaproxy-bin`)

GoReleaser's `aurs` block generates `PKGBUILD` + `.SRCINFO` from the linux
amd64/arm64 archives — sha256 comes from the artifacts, the tarballs are not
re-downloaded — and pushes both over SSH to
`ssh://aur@aur.archlinux.org/peaproxy-bin.git` on `master`. The package
installs `peaproxy` to `/usr/bin/peaproxy` and carries
`provides=('peaproxy')` / `conflicts=('peaproxy')`, so it can never sit next to
a source build. Config stays in `~/.config/peaproxy`, the same path a Homebrew
or `go install` build uses.

AUR is slower and flakier than the GitHub API path, so:

- `retry: {attempts: 5, delay: 15s, max_delay: 2m}` in `.goreleaser.yaml`
  covers the transient SSH drops on clone and push (worst case ~4 min inside
  the job's 60m timeout).
- The pipe sets `ContinueOnError`, and `skip_upload` is `auto` on stable tags /
  `true` when `AUR_KEY` is empty, so AUR can never fail a release.
- A missed push self-heals on the next tag. If a release's AUR push did not
  land, re-run it by tagging the next patch, not the whole workflow — a re-run
  of the release job would hit `already_exists` on the GitHub Release.
- `git_ssh_command` forces IPv4 (`-4`, `AddressFamily=inet`): some CI networks
  reach AUR over IPv6, stall, and get the "down due to maintenance" banner.

One-time setup:

1. AUR account and a dedicated SSH key, if not already there for `genv`:

   ```bash
   ssh-keygen -t ed25519 -C "aur" -f ~/.ssh/aur
   # no passphrase — the CI key must open without a prompt
   ```

   Add the **public** key at <https://aur.archlinux.org/account/> → SSH keys.

2. Create the `peaproxy-bin` package. AUR takes the first push into a name it
   has not seen before, but seeding the repo locally means the namespace (and
   its web page) exists before the next tag, and you push a PKGBUILD you have
   already read. For the first push there is no release page to copy from yet,
   so generate one:

   ```bash
   goreleaser release --clean --snapshot --skip=publish
   cat dist/aur/peaproxy-bin.pkgbuild   # replace the 2.0.9-SNAPSHOT-… version
   cat dist/aur/peaproxy-bin.srcinfo    # with a real tag, e.g. 2.0.9
   ```

   Then seed AUR with those two files:

   ```bash
   git clone https://aur.archlinux.org/peaproxy-bin.git /tmp/aur-pkg
   cd /tmp/aur-pkg
   # paste PKGBUILD + .SRCINFO
   git add PKGBUILD .SRCINFO
   git commit -m "Initial release v2.0.9"
   git remote set-url origin ssh://aur@aur.archlinux.org/peaproxy-bin.git
   GIT_SSH_COMMAND="ssh -4 -i ~/.ssh/aur -o AddressFamily=inet" git push origin master
   ```

   Every later PKGBUILD comes from the release job, or by hand from
   <https://aur.archlinux.org/packages/peaproxy-bin>. Never leave `SKIP` in a
   sha256 line: AUR flags the package as untrustworthy.

3. Add the private key to `ks1686/peaproxy` (the same `AUR_KEY` value `genv`
   uses is fine):

   ```bash
   gh secret set AUR_KEY < ~/.ssh/aur
   ```

Verify from an Arch machine once the next stable tag lands:

```bash
paru -Sy genv-bin          # sanity: the known-good AUR path
paru -Sy peaproxy-bin
peaproxy --version         # expect the tag, not dev
```

## Dry-run packaging

```bash
goreleaser check
goreleaser release --clean --snapshot
```

Artifacts land in `./dist/`. Nothing is published. Snapshot skips notarization
unless `MACOS_SIGN_P12` is set in the environment.

Inspect the generated package metadata without publishing anything:

```bash
goreleaser check
goreleaser release --clean --snapshot --skip=publish
cat dist/scoop/peaproxy.json        # 64bit + arm64, hashes from checksums.txt
cat dist/aur/peaproxy-bin.pkgbuild  # source_/sha256sums_ per arch
cat dist/aur/peaproxy-bin.srcinfo   # tab-indented, AUR requires it
```
