# Distribution

Threave ships as a single executable with embedded frontend assets through [GitHub Releases](https://github.com/threave-io/threave/releases) and the [`threave-io/homebrew-tap`](https://github.com/threave-io/homebrew-tap) tap.

## GitHub releases

Use the **Prepare Release** workflow with a version such as `0.12.0`. It checks out `main`, builds and commits the embedded frontend assets, creates the annotated tag, and starts the release workflow. The release workflow verifies the staged assets, runs Go tests, builds archives for macOS, Linux, and Windows (arm64 and amd64), and publishes checksums. Manual version-tag pushes run the same guarded workflow. After publishing, the release workflow dispatches **Update Threave** in `threave-io/homebrew-tap`. The tap's six-hour schedule remains a fallback; the workflow can also be dispatched manually.

Archives are named `threave_<version>_<os>_<arch>.tar.gz` or `.zip` on Windows. Each archive contains `threave` and a `gorchestra` compatibility binary, plus README and LICENSE. Build locally with:

```sh
cd web
bun install --frozen-lockfile
VITE_THREAVE_VERSION=0.12.0 bun run build
cd ..
bun run build:stage
go test ./...
VERSION=0.12.0 bun run release:archives
```

Release tags must include current `internal/webassets/dist` because the Homebrew source formula builds without Bun. The prepare workflow handles this; the release workflow rejects stale assets.

## Homebrew

The formula template is `packaging/homebrew/threave.rb.template`. The tap's **Update Threave** workflow reads the latest release and its template, then writes `Formula/threave.rb` using the tap repository's own `GITHUB_TOKEN`. The formula update uses the tap's own token. Dispatching it from the release repository requires the `HOMEBREW_TAP_TOKEN` Actions secret in `threave-io/threave`: use a fine-grained token restricted to `threave-io/homebrew-tap` with **Actions: write** permission. Dispatch failures fail the release workflow's final step without undoing the published release; the scheduled fallback can still update the tap. The tap's `formula_renames.json` maps `gorchestra` to `threave` for upgrades. The formula installs a `gorchestra` binary alias and copies an existing `etc/gorchestra/gorchestra.env` into the new service config when present, preserving its data directory. New installations use `etc/threave/threave.env` and `var/threave`.

```sh
brew install threave-io/tap/threave
brew test threave-io/tap/threave
brew audit --strict --online threave-io/tap/threave
```

For a manual update, replace `{{VERSION}}` and `{{SOURCE_SHA256}}` in the template using the tagged source archive SHA-256, commit `Formula/threave.rb` and `formula_renames.json` to the tap, then run the commands above.
