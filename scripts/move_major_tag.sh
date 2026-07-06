#!/usr/bin/env bash
#
# move_major_tag.sh <vX.Y.Z[-suffix]> — after a release, pin the released
# version into a VERSION file and force-move the floating major tag (e.g. v0)
# to that commit, then push it. Users reference `gnanam1990/sieve@v0` and get
# the matching binary, because action.yml reads VERSION at the pinned ref.
#
# Prerelease policy: by convention a major tag tracks the latest STABLE
# release, so a prerelease (tag containing a '-', e.g. v0.0.9-rc1) does NOT move
# the major tag — UNLESS MOVE_MAJOR_ON_PRERELEASE=true (used to bootstrap/test
# @v0 before the first stable release exists). Remove that override once a
# stable v0.x.0 has shipped.
#
# Runs in GitHub Actions (release.yml) with contents:write; the checkout action
# has already configured push credentials.
set -euo pipefail

TAG="${1:?usage: move_major_tag.sh <vX.Y.Z>}"

# Derive the major series tag: v0.0.9-rc1 -> v0, v1.4.2 -> v1, v12.0.0 -> v12.
MAJOR="v$(printf '%s' "$TAG" | sed -E 's/^v?([0-9]+).*/\1/')"

# Skip prereleases unless explicitly allowed.
case "$TAG" in
*-*)
	if [ "${MOVE_MAJOR_ON_PRERELEASE:-false}" != "true" ]; then
		echo "prerelease ${TAG}: not moving ${MAJOR} (set MOVE_MAJOR_ON_PRERELEASE=true to override)"
		exit 0
	fi
	echo "prerelease ${TAG}: moving ${MAJOR} because MOVE_MAJOR_ON_PRERELEASE=true"
	;;
esac

printf '%s\n' "$TAG" >VERSION

git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
git add VERSION
# The tagged commit may already carry this exact VERSION (e.g. the first
# release). Committing nothing would fail under `set -e` and skip the tag move,
# so only commit when there is a real change — but always move and push the tag.
if git diff --cached --quiet; then
	echo "VERSION already ${TAG}; moving ${MAJOR} onto the current commit"
else
	git commit -m "chore(release): pin VERSION ${TAG} and move ${MAJOR}"
fi
git tag -f "$MAJOR"
git push origin "refs/tags/${MAJOR}" --force

echo "moved ${MAJOR} -> $(git rev-parse --short HEAD) (VERSION=${TAG})"
