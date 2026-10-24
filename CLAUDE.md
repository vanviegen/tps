# Notes for agents

## Versions and changelog

The version is the `version` in `package.json` (and `package-lock.json`, kept
the same), in semver; 0.x for now, so a feature bumps the minor number and a fix
the patch. Releasing is deploying the project from its board, which runs
`deploy.sh`: that pushes the commit to GitHub with a `v<version>` tag, from
which the release workflow (`.github/workflows/release.yml`) builds the
binaries, and they take their version from it. A version is released once:
when the one in `package.json` already went out (see `refs/tps/deployed`), the
next change bumps it.

There is no changelog: the commit messages are the record, and GitHub writes
each release's notes from them.
