GO      ?= go
BIN     := bin/iaxphone
GUI     := bin/iaxphone-gui

.PHONY: all build gui test vet lint fmt clean deb-src deb asterisk-up asterisk-down asterisk-logs

all: build

build:
	$(GO) build -o $(BIN) ./cmd/iaxphone

gui:
	$(GO) build -o $(GUI) ./cmd/iaxphone-gui

test:
	$(GO) test -race -cover ./...

vet:
	$(GO) vet ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -s -w .

clean:
	rm -rf bin dist

# Debian packages: iaxphone (CLI) and iaxphone-gui (window), from an orig
# tarball that vendors the Go modules so that the build needs no network.
# `make deb-src` prepares the source package (orig tarball, .dsc) in
# dist/deb; `make deb` then builds the binaries in an ubuntu:22.04
# container (debian/build-in-docker.sh, needs Docker). Built against
# jammy's libraries, the same .deb installs on Ubuntu 22.04 and on every
# later release.
# Add SNAPSHOT=1 for a snapshot of the current commit, versioned
# <changelog version>+YYYYMMDDHHMM (UTC) with a changelog entry added in
# the build copy only; debian/changelog in the tree is left alone.
deb-src:
	rm -rf dist/deb && mkdir -p dist/deb
	set -e; v=$$(dpkg-parsechangelog -S Version | sed 's/-[^-]*$$//'); \
	if [ -n "$(SNAPSHOT)" ]; then v=$$v+$$(date -u +%Y%m%d%H%M); fi; \
	VERSION=$$v debian/make-orig-tarball.sh dist/deb; \
	cd dist/deb && tar xzf iaxphone_*.orig.tar.gz && cp -r ../../debian iaxphone-*/; \
	if [ -n "$(SNAPSHOT)" ]; then \
		DEBFULLNAME="Pierre POMES" DEBEMAIL=pierre.pomes@gmail.com \
		dch -c iaxphone-*/debian/changelog -v "$$v-1" -D unstable \
			"Snapshot of git $$(git -C ../.. rev-parse --short HEAD)."; \
	fi; \
	cd iaxphone-*/ && dpkg-buildpackage -S -us -uc -d

deb: deb-src
	debian/build-in-docker.sh ubuntu:22.04 dist/deb ppa:longsleep/golang-backports
	lintian dist/deb/*.changes || true

# Test Asterisk (chan_iax2) on udp/4569, see docker/asterisk/.
asterisk-up:
	docker compose -f docker/asterisk/compose.yml up -d --build

asterisk-down:
	docker compose -f docker/asterisk/compose.yml down

asterisk-logs:
	docker compose -f docker/asterisk/compose.yml logs -f
