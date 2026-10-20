#!/usr/bin/env bash
# Prints the image tags a release publishes, as `tags=...` for
# $GITHUB_OUTPUT. The release workflow runs it; you can too:
#
#   bash scripts/release-image-tags.sh v1.2.3
#
# Every release gets its exact tag (v1.2.3) and the bare version (1.2.3).
# A stable release also moves 1.2, 1 and latest, but only while it's the
# newest release in that line, so rerunning or patching an old release
# never drags latest backwards. A prerelease (v1.3.0-rc.1) moves nothing.
# 0.x releases skip the major tag: 0 would mean "anything before 1.0".
#
# The known releases come from `git tag`, or from RELEASE_TAGS (one per
# line) when you want to try a scenario.
set -euo pipefail

tag=${1:?Usage: scripts/release-image-tags.sh vMAJOR.MINOR.PATCH}
if [[ ! "$tag" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)(-[0-9A-Za-z.-]+)?$ ]]; then
	echo "release-image-tags: $tag isn't vMAJOR.MINOR.PATCH" >&2
	exit 1
fi
major=${BASH_REMATCH[1]} minor=${BASH_REMATCH[2]} pre=${BASH_REMATCH[4]}
tags=("$tag" "${tag#v}")

if [[ -z "$pre" ]]; then
	known=$({ printf '%s\n' "${RELEASE_TAGS-$(git tag -l 'v*')}"; echo "$tag"; } |
		grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -uV)
	newest() { grep -E "^$1" <<<"$known" | tail -n 1; }
	if [[ "$(newest "v${major}\.${minor}\.")" == "$tag" ]]; then
		tags+=("${major}.${minor}")
	fi
	if [[ "$major" != 0 && "$(newest "v${major}\.")" == "$tag" ]]; then
		tags+=("$major")
	fi
	if [[ "$(newest v)" == "$tag" ]]; then
		tags+=(latest)
	fi
fi

echo "tags=${tags[*]}"
