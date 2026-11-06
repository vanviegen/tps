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

`CHANGELOG.md` has a section per version, newest first. It keeps to
highlights, in a few bullets a user would care about: fold related commits
into one bullet, gather small fixes into a single "Fixes:" line, and leave out
refactors, tests, docs and what a later change in the same release undid. A
change that alters what users get extends the section of the version in
`package.json`, rewording its bullets where the change revises one, and starts
a new section when it bumps the version past a released one. The commit
messages remain the full record, and GitHub writes each release's notes from
them.
