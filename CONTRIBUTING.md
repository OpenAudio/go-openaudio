Thank you for your interest in contributing to the Open Audio Project.

## Goals

The goal of the Open Audio Protocol is to bring the world's music onchain as a Global Music Database. This repo is a golang implementation of the protocol.

For more information about the Open Audio Protocol, checkout [the docs](https://docs.openaudio.org/).

## Development

See [Developer Documentation](docs/developers.md) for instructions on building and testing go-openaudio.

To begin contributing, create a development branch either on github.com/OpenAudio/go-openaudio, or your fork (using `git remote add origin`).

Before merging a pull request:

* Ensure your branch is up-to-date with `main` (GitHub won't let you merge without this)
* Run `make test` to ensure that all tests pass
* Confirm the PR title follows Conventional Commits, e.g. `feat(console): add storage status`

## Branching Model

This project uses branches to keep breaking changes separate from fixes for specific chain versions.

The `main` branch is for primary feature development while the `mainnet-alpha-beta` can receive backported changes and fixes that remain compatible with the `mainnet-alpha-beta` chain.

If your change should be backported to an existing chain, please also open a PR against the long-lived chain branch (e.g., `mainnet-alpha-beta`) immediately after your change has been merged to `main`.

You can do this by cherry-picking your commit off main:

```bash
$ git checkout mainnet-alpha-beta
$ git checkout -b {new branch name}
$ git cherry-pick {commit SHA from main}
# may need to fix conflicts, and then use git add and git cherry-pick --continue
$ git push origin {new branch name}
```

## Releases

Releases on `main` are automated with [release-please](https://github.com/googleapis/release-please). You do not bump `pkg/version/.version.json` by hand and you do not dispatch a release workflow manually for `main`.

### Commit conventions

All commits on `main` must follow the [Conventional Commits](https://www.conventionalcommits.org/) format. Because PRs are squash-merged, the **PR title** is what lands on `main` — it is what release-please reads and what `.github/workflows/pr-title-lint.yml` checks.

Before opening or updating a PR, set the title to:

```text
<type>(optional-scope): <short description>
```

Allowed types are `feat`, `fix`, `perf`, `revert`, `refactor`, `docs`, `test`, `build`, `ci`, `chore`, and `deps`. Use lowercase for the type and keep the description imperative and concise.

Examples:

- `feat(console): show store-all node status`
- `fix(storage): respect archive disk headroom`
- `docs: clarify release workflow`

Avoid sentence-style titles such as `Show store-all node status in console`; they fail the PR title lint check and block merging.

Bump rules:

| Commit prefix | Bump |
| --- | --- |
| `fix:`, `perf:` | patch |
| `feat:` | minor |
| `feat!:`, or any type with a `BREAKING CHANGE:` footer | major |
| `docs:`, `test:`, `build:`, `ci:`, `chore:`, `refactor:`, `deps:`, `revert:` | no bump (some hidden from CHANGELOG) |

Scopes are optional, e.g. `feat(console): surface archive storage stats`.

### How a release ships

1. As Conventional Commits land on `main`, release-please opens (and keeps updated) a PR titled `chore: release X.Y.Z`. The PR bumps `pkg/version/.version.json`, updates `.release-please-manifest.json`, and rewrites `CHANGELOG.md`.
2. Merging the release PR creates a GitHub Release, pushes the `vX.Y.Z` git tag, and triggers:
   - `tag-released.yml` — retags the multi-arch image as `openaudio/go-openaudio:vX.Y.Z` and promotes `:stable` to that version.
   - `buf-publish.yml` — publishes the proto module to the Buf Schema Registry under the version label.

For the tag push to fire those downstream workflows, release-please authenticates as a dedicated GitHub App (`openaudio-release-bot`) rather than using the default `GITHUB_TOKEN`. The workflow mints a short-lived installation token at run time via `actions/create-github-app-token`, so there is no PAT to rotate or human-owned credential to maintain.

Repo admins maintain two configuration values for this:

- `vars.RELEASE_PLEASE_APP_CLIENT_ID` — the App's **Client ID** (a string like `Iv23li…`, shown on the App's settings page). Stored as a repository **variable**, not a secret — Client IDs are public.
- `secrets.RELEASE_PLEASE_APP_PRIVATE_KEY` — the App's private key (PEM contents).

Note: the App's **Client ID** is a string, distinct from the numeric **App ID** also shown on the same page. `actions/create-github-app-token@v3` expects the Client ID (the App ID input is deprecated).

The App is installed only on this repository and granted the minimum permissions: **Contents: read/write** (push the release tag, create the GitHub Release) and **Pull requests: read/write** (open and maintain the release PR). If the App is ever removed or its key is rotated, the workflow falls back to no-op auth and the release PR will fail to open until the values are restored.

### Manual release fallback

If release-please is broken or unavailable, you can cut a release by hand:

1. Hand-edit `pkg/version/.version.json` on `main` to the new version and merge.
2. Dispatch `.github/workflows/release.yml` with the matching version (e.g. `v1.2.16`).

The resulting tag still triggers `tag-released.yml` and `buf-publish.yml`, so the image and buf module land the same way they do from a release-please release.

## Testing

Tests are located in _test.go files as directed by the Go testing package. If you're adding or removing a function, please check there's a TestType_Method test for it.

Integration tests are located under `pkg/integration_tests`. These test files have a numeric prefix which ensures a specific ordering. When writing a new test, use these numbers to set the ordering in which your test will run. Numbers can be reused if order does not matter for a certain set of tests.

### Running Tests

Use `make test` to run all tests.

Use `make test-unit` to run unit tests.

Use `make test-mediorum` to run tests for the storage service (located under `pkg/mediorum`).

Use `make test-integration` to run integration tests.
