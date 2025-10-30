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

## Testing

Tests are located in _test.go files as directed by the Go testing package. If you're adding or removing a function, please check there's a TestType_Method test for it.

Integration tests are located under `pkg/integration_tests`. These test files have a numeric prefix which ensures a specific ordering. When writing a new test, use these numbers to set the ordering in which your test will run. Numbers can be reused if order does not matter for a certain set of tests.

### Running Tests

Use `make test` to run all tests.

Use `make test-unit` to run unit tests.

Use `make test-mediorum` to run tests for the storage service (located under `pkg/mediorum`).

Use `make test-integration` to run integration tests.

