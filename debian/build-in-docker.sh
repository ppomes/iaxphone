#!/bin/bash
# Build the binary packages from a source package (.dsc) inside a clean
# Ubuntu or Debian container, so that the shared library dependencies
# dpkg-shlibdeps generates are those of that release rather than of the
# build host's. Packages built on the oldest release they target install
# on every later one.
#
# Usage: debian/build-in-docker.sh <image> <dir> [ppa]
#   image  e.g. ubuntu:22.04
#   dir    holds iaxphone_*.dsc and its tarballs (make deb-src); receives
#          the .deb, .ddeb, .buildinfo and .changes files
#   ppa    optional PPA to enable first (jammy needs one for Go >= 1.26)

set -euo pipefail

[ $# -ge 2 ] || { sed -n '2,12p' "$0" >&2; exit 2; }
IMG=$1
DIR=$(realpath "$2")
PPA=${3:-}

echo "==> Building in $IMG"
docker run --rm -v "$DIR:/out" -e PPA="$PPA" "$IMG" bash -c '
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -q >/dev/null
apt-get install -y -q --no-install-recommends ca-certificates gnupg devscripts \
    equivs build-essential fakeroot lsb-release >/dev/null
if [ -n "$PPA" ]; then
    apt-get install -y -q --no-install-recommends software-properties-common >/dev/null
    add-apt-repository -y "$PPA" >/dev/null
    apt-get update -q >/dev/null
fi
mkdir /build && cd /build
dpkg-source -x /out/iaxphone_*.dsc >/dev/null
cd iaxphone-*/
mk-build-deps -i -r -t "apt-get -y -q --no-install-recommends" debian/control >/dev/null
echo "==> $(lsb_release -ds), $(go version)"
dpkg-buildpackage -us -uc -b
cp ../*.deb ../*.buildinfo ../*_amd64.changes /out/
cp ../*.ddeb /out/ 2>/dev/null || true
chown "$(stat -c %u:%g /out)" /out/*
for p in /out/*.deb; do echo "==> $(basename "$p"): $(dpkg-deb -f "$p" Depends)"; done
'
