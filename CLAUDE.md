# avahi-controller

Kubernetes controller that publishes LoadBalancer Service IPs to mDNS through a
managed block in `/etc/avahi/hosts`. Architecture and edge cases are in
`PROJECT.md`; user-facing docs are `README.md` and `charts/avahi-controller/README.md`.

## Checks before committing

```sh
gofmt -l .                      # must print nothing (CI fails otherwise)
go vet ./...
go test ./...
helm lint charts/avahi-controller
scripts/check-versions.sh
```

## Commits

Conventional Commits, because release-please derives versions and the changelog
from them: `fix(scope): ...` → patch, `feat(scope): ...` → minor,
`feat!:` or a `BREAKING CHANGE:` footer → major. `ci:`, `docs:`, `test:`,
`refactor:` and `chore:` do not trigger a release. Renovate commits as
`fix(deps)`, so dependency updates produce patch releases.

## Releases

Nothing is tagged, bumped or published by hand.

1. Pushing to `main` runs `.github/workflows/release-please.yml`, which keeps a
   release PR (`release-please--branches--main`) up to date.
2. That PR bumps `.release-please-manifest.json`, `CHANGELOG.md` and every line
   marked `x-release-please-version` (or inside `x-release-please-start-version`
   / `x-release-please-end`): the chart `version` and `appVersion`, the
   `artifacthub.io/images` annotation, `image.tag` in `values.yaml`, and the
   image in `deploy/deployment.yaml`. CI runs on the PR, and
   `scripts/check-versions.sh` fails it if any of those were missed.
3. Merging the PR tags `vX.Y.Z` and creates the GitHub release. Then:
   - `docker-release` pushes `docker.io/mm404/avahi-controller` and
     `ghcr.io/mm503/avahi-controller` with tags `X.Y.Z` and `latest`.
   - `helm-release` checks out the tag, runs `scripts/check-versions.sh X.Y.Z`,
     attaches `avahi-controller-X.Y.Z.tgz` to the same GitHub release, and
     merges the new entry into `index.yaml` on `gh-pages`.
4. Afterwards, confirm `/releases/latest` is the new `vX.Y.Z` with the chart
   attached, and that Artifact Hub lists the version (it polls the index;
   allow about 30 minutes).

### Rules

- The chart and the controller always share one version. Never bump the chart
  separately and never reintroduce a chart-only release or chart-releaser.
- Any new place that carries the release version needs an
  `x-release-please-version` marker and a check in `scripts/check-versions.sh`.
- List every entry in `extra-files` as `{ "type": "generic", "path": ... }`.
  A bare `.yaml` or `.json` path also runs a parser that sets the top-level
  `version` key and re-serializes the file, which strips the marker comments
  (this is what broke `Chart.yaml` in the first 1.0.0 release PR).
- Keep the version markers outside the `artifacthub.io/images` block scalar;
  anything inside it is shown on Artifact Hub.
- On `gh-pages`, change only `index.yaml`. `artifacthub-repo.yml` gives the
  repository its Artifact Hub Verified Publisher badge.
- Never delete the old `avahi-controller-0.1.x` GitHub releases: index entries
  up to chart 0.1.19 download from them.

### Before merging a release PR

- Update `artifacthub.io/changes` in `Chart.yaml` to describe this release
  (kinds: added, changed, deprecated, removed, fixed, security), or remove it.
  Set `artifacthub.io/containsSecurityUpdates` only when the release fixes a
  security issue. Both are static and otherwise carry over to later versions.
  Commit this to `main` as `docs(chart): ...` so the release PR picks it up.
- For a breaking change, add an "Upgrading from X" section to both
  `README.md` and `charts/avahi-controller/README.md`. Helm replaces list
  values (`args`, `volumes`, `volumeMounts`, `env`) instead of merging them, so
  call out anything users must remove from their own copies.
