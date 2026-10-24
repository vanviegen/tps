#!/bin/sh
# Release TPS: publish the commit being deployed on GitHub — main moved up to
# it, and tagged with the version package.json gives it — which has the release
# workflow (.github/workflows/release.yml) build the binaries that running TPS
# installs with --auto-update. TPS runs this (LABEL tps.deploy in
# Containerfile.dev) on a clean checkout, in a container holding only the
# credentials the project's settings name. The one it wants is
# ~/.ssh/tps_deploy: a key without a passphrase (nobody is there to type it),
# added to the repository on GitHub as a deploy key that may write. Without
# it, whatever else ~/.ssh holds is tried.
set -eu
cd "$(dirname "$0")"
remote=git@github.com:vanviegen/tps.git
ssh="ssh -o StrictHostKeyChecking=accept-new"
key="$HOME/.ssh/tps_deploy"
[ -f "$key" ] && ssh="$ssh -i $key -o IdentitiesOnly=yes"
export GIT_SSH_COMMAND="$ssh"

version="v$(python3 -c 'import json; print(json.load(open("package.json"))["version"])')"
commit="$(git rev-parse HEAD)"

# A version is released once, and for good: this one again is nothing to do,
# and another commit under its number is a version bump that was left out.
if git fetch --quiet "$remote" "refs/tags/$version:refs/tags/$version" 2>/dev/null; then
	if [ "$(git rev-parse "$version^{commit}")" = "$commit" ]; then
		echo "$version is released already, as this very commit; nothing to do."
		exit 0
	fi
	echo "$version is released already, as another commit: bump the version in package.json." >&2
	exit 1
fi

# Fast-forward only: a main on GitHub with commits this one lacks is for a
# human to look at, not to overwrite.
git push "$remote" "$commit:refs/heads/main"
git push "$remote" "$commit:refs/tags/$version"
echo "Pushed $version; the release workflow builds it: https://github.com/vanviegen/tps/actions"
