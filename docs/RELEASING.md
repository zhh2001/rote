# Release checklist

Publishing is triggered by pushing a `v*` tag. Never move a published tag or replace its artifacts to change shipped code; use a new patch release instead.

## Before tagging

- Commit the intended changes, including `docs/releases/<tag>.md`.
- Check formatting, `go vet ./...`, `go test -race -count=1 ./...`, and `go mod tidy -diff`. Review any flaky/time-sensitive test failures rather than silently retrying until green.
- Run `goreleaser check` and `goreleaser release --snapshot --parallelism 2`. This builds all archives/packages in `dist/` without publishing. Do not use `--clean` on a directory containing files you need to keep.
- Extract the host archive, verify its checksum, and exercise its binary with:

  ```sh
  ROTE_TEST_BINARY=/absolute/path/to/rote \
  ROTE_TEST_VERSION=<version-printed-by-the-snapshot> \
  go test -count=1 -timeout=10m ./cmd/rote
  ```

  `ROTE_TEST_BINARY` redirects the process tests to that executable; unit tests still run against source. Release binaries themselves are not race-instrumented. Set `ROTE_TEST_PREVIOUS_BINARY` to an extracted, checksum-verified v0.2.1 binary to include the old-database upgrade test.
- Ensure `HOMEBREW_TAP_GITHUB_TOKEN` is configured as a repository Actions secret with write access to `zhh2001/homebrew-tap`. Never put its value in the repo or release notes. The default Actions token publishes the GitHub release.
- Push main and wait for all four native Linux/macOS × amd64/arm64 CI jobs on that exact commit to succeed. Cross-compiling on one host does not check platform-specific runtime behavior. Confirm the proposed tag is unused.

## Publish v1.0.1 (maintainer action)

The v1.0.0 source tag remains unchanged. Its macOS source-test gate failed before publishing any binary assets. The corrected tests and four-platform main/PR CI are included in v1.0.1; do not rerun the old release or move the v1.0.0 tag.

Review the prepared, staged changes, then commit and push:

```sh
git diff --cached --stat
git commit -m "docs: prepare v1.0.1 release"
git push origin main
gh run list --workflow ci.yml --commit "$(git rev-parse HEAD)" --limit 1
gh run watch <ci-run-id> --exit-status
```

Replace `<ci-run-id>` with the ID printed by `gh run list`. If the run has not appeared yet, repeat the list command after a few seconds. Wait for all four jobs on that exact commit, including after these documentation changes. Then check that the release notes exist and the new tag is unused locally and remotely:

```sh
test -f docs/releases/v1.0.1.md
git tag --list v1.0.1
git ls-remote --tags origin refs/tags/v1.0.1
```

Both tag queries must succeed without printing a matching tag. If either finds one, stop and inspect its commit/release state instead of overwriting it. With a clean working tree on the tested main commit, publish:

```sh
git tag -a v1.0.1 -m "Release v1.0.1"
git push origin refs/tags/v1.0.1
gh run list --workflow release.yml --branch v1.0.1 --limit 1
gh run watch <release-run-id> --exit-status
```

Use the ID for this tag's release run, not an older run. A failed post-publication check can leave an already-published release; inspect the failure before retrying.

The release workflow runs native Linux/macOS × amd64/arm64 race-enabled source tests, publishes through GoReleaser, updates the Homebrew cask, then calls `verify-release.yml`. No local GitHub token export is required for the CI release.

## If the release gate fails

Check which jobs ran before retrying. A failed source-test gate skips publishing; a failed post-publication check may leave an already-public release. Inspect the release and its assets rather than inferring publication from the tag alone.

Re-running an old workflow uses its original commit, not a newer main commit. Commit any test/code fixes, push main, and wait for all four native CI jobs first. Keep pushed tags immutable: if a different commit is needed for release, choose a new version and add matching release notes. Do not delete or force-move the old tag as an automatic recovery step.

If publication succeeded and only the verification workflow needs fixing (for example, a missing test-container utility), keep the existing release and artifacts. Commit the workflow fix to main, then dispatch `verify-release.yml` from main against the existing tag as shown below. A verification-only fix does not require a new release. The checkout still uses the selected tag, so the tests exercise the shipped source and binaries with the corrected workflow.

## After publication

Wait for **all** jobs, not only the GoReleaser publishing job. Post-publication verification downloads and checksum-verifies current and v0.2.1 archives; checks the version and initialization; reuses the CLI process suite against the downloaded executable; tests database upgrade/readback; runs both pinned-version and latest installers in temporary directories; installs the Homebrew cask on both Macs; and installs deb/rpm/apk packages in disposable Linux containers on both CPUs, comparing installed binaries and running a job with saved output. Linux additionally exercises real pseudo-terminal dashboard behavior.

To rerun verification without publishing or changing the tag:

```sh
gh workflow run verify-release.yml --ref main -f tag=v1.0.1
gh run list --workflow verify-release.yml --limit 5
gh run watch <verification-run-id> --exit-status
```

The latest installer and Homebrew checks expect the selected release to be the current stable release/tap version. This workflow is intended for the newly published stable release, not prereleases or historical releases after the latest release or tap has advanced.

If CI lacks permission to update the tap, the maintainer must replace the `HOMEBREW_TAP_GITHUB_TOKEN` secret via repository Settings → Secrets and variables → Actions. If a release is already partially public, inspect its assets and workflow logs before retrying; do not delete or retag it as a blanket fix.

Optional manual confidence checks: try the dashboard in your normal terminal and upgrade a **backup copy** of your own database. Hosted-runner checks do not replace application-specific production validation. Apple signing/notarization would require your developer credentials and is not part of the current unsigned distribution policy.
