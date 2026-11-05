#!/bin/bash
# Build the orig tarball for the Debian packaging, with the Go modules
# vendored: Debian build hosts have no network, so the tarball must carry
# everything the build needs.
#
# Output: <outdir>/iaxphone_<version>.orig.tar.gz (outdir defaults to ..),
# the version taken from debian/changelog. The tree comes from git HEAD
# when there is a commit, from the working tree otherwise.
#
# Usage: debian/make-orig-tarball.sh [outdir]   (from the source root)

set -euo pipefail

OUT=$(realpath -m "${1:-..}")
VERSION=$(dpkg-parsechangelog -S Version | sed 's/-[^-]*$//')
DIST="iaxphone-${VERSION}"
TARBALL="$OUT/iaxphone_${VERSION}.orig.tar.gz"
WORK=$(mktemp -d)
trap 'rm -rf "${WORK:?}"' EXIT

mkdir -p "$OUT" "$WORK/$DIST"
if git rev-parse --verify HEAD >/dev/null 2>&1; then
    echo "==> Exporting git HEAD"
    git archive HEAD | tar xf - -C "$WORK/$DIST"
else
    echo "==> No commit yet, exporting the working tree"
    tar cf - --exclude=./.git --exclude=./bin --exclude=./dist \
        --exclude=./vendor --exclude='*.test' . | tar xf - -C "$WORK/$DIST"
fi
# The packaging is not part of the upstream tarball.
rm -rf "${WORK:?}/${DIST:?}/debian"

echo "==> Vendoring the Go modules"
(cd "$WORK/$DIST" && go mod vendor)

echo "==> Writing $TARBALL"
tar czf "$TARBALL" -C "$WORK" "$DIST"
echo "    $(tar tzf "$TARBALL" | grep -c '/vendor/') vendor entries"
