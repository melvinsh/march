#!/usr/bin/env bash
#
# Publish march's Homebrew formulae.
#
# Formula/ in this repository is the source of truth. Homebrew installs from a
# different repository — melvinsh/homebrew-march — because `brew tap x/y` only
# ever looks for a repository called homebrew-y, and march's own repository is
# called march. That second repository is a mirror: nothing there is edited by
# hand, and this script is what keeps the two byte-for-byte identical.
#
# It does the whole release: reads the sha256 off the tag's tarball as GitHub
# actually serves it, writes it into the formula, and pushes both repositories.
# Doing that by hand is what produced "Formula: fix v1.5.0 sha256 to match
# GitHub tarball" — a formula that could not install for as long as it took
# somebody to notice.
#
#   docs/publish-formulae.sh            # publish the version in march.rb
#   docs/publish-formulae.sh 1.6.0      # publish that version
#
# The tag has to exist and be pushed first; a formula points at a tarball, and
# GitHub only has one once the tag is there.
set -euo pipefail

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
formula=$repo/Formula/march.rb

version=${1:-$(sed -n 's/^  version "\(.*\)"/\1/p' "$formula")}
[ -n "$version" ] || { echo "publish: no version given and none in march.rb" >&2; exit 1; }
tag=v$version

# The tap is a normal git checkout that Homebrew keeps under its prefix. Point
# MARCH_TAP somewhere else to publish from a different clone.
tap=${MARCH_TAP:-$(brew --repository)/Library/Taps/melvinsh/homebrew-march}
[ -d "$tap/.git" ] || { echo "publish: $tap is not a git checkout of the tap" >&2; exit 1; }

echo "==> Releasing march $version from $repo"

git -C "$repo" rev-parse -q --verify "refs/tags/$tag" >/dev/null \
  || { echo "publish: $tag does not exist — tag it first" >&2; exit 1; }
git -C "$repo" ls-remote --exit-code --tags origin "$tag" >/dev/null 2>&1 \
  || { echo "publish: $tag is not on origin — push it first" >&2; exit 1; }

url=https://github.com/melvinsh/march/archive/refs/tags/$tag.tar.gz
echo "==> Hashing $url"
sha=$(curl -fsSL "$url" | shasum -a 256 | cut -d' ' -f1)
[ -n "$sha" ] || { echo "publish: could not hash the release tarball" >&2; exit 1; }

# Rewritten rather than templated, so the formula stays a file somebody can
# read and edit on its own.
tmp=$(mktemp)
sed -e "s|^  url \".*\"|  url \"$url\"|" \
    -e "s|^  sha256 \".*\"|  sha256 \"$sha\"|" \
    -e "s|^  version \".*\"|  version \"$version\"|" \
    "$formula" > "$tmp"
mv "$tmp" "$formula"

grep -q "$sha" "$formula" && grep -q "$tag.tar.gz" "$formula" \
  || { echo "publish: the formula did not take the new version" >&2; exit 1; }

if ! git -C "$repo" diff --quiet -- Formula/march.rb; then
  git -C "$repo" commit -q -m "Formula: bump to $tag" -- Formula/march.rb
  git -C "$repo" push -q origin HEAD
  echo "==> Bumped Formula/march.rb to $version"
else
  echo "==> Formula/march.rb already at $version"
fi

echo "==> Mirroring Formula/ into $tap"
cp "$repo/Formula/march.rb" "$repo/Formula/qemu-march.rb" "$tap/Formula/"
rm -rf "$tap/Formula/patches"
cp -R "$repo/Formula/patches" "$tap/Formula/patches"

# The invariant: the mirror is the source of truth, exactly. Anything else
# means somebody edited the tap by hand and this release would quietly drop it.
diff -r "$repo/Formula" "$tap/Formula" \
  || { echo "publish: the tap does not match Formula/ after copying" >&2; exit 1; }

if git -C "$tap" diff --quiet && git -C "$tap" diff --cached --quiet; then
  echo "==> Tap already matches"
else
  git -C "$tap" add -A Formula
  git -C "$tap" commit -q -m "march $version"
  git -C "$tap" push -q origin HEAD
  echo "==> Pushed march $version to the tap"
fi

# What a user's `brew install` will do, minus the build: resolve the formula
# from the tap and check the tarball against the sha256 just written.
if command -v brew >/dev/null; then
  echo "==> Verifying through Homebrew"
  brew update --quiet >/dev/null 2>&1 || true
  brew fetch --quiet melvinsh/march/march >/dev/null
  echo "==> brew fetch melvinsh/march/march: ok"
fi

echo "==> march $version published"
