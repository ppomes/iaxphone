GO      ?= go
BIN     := bin/iaxphone
GUI     := bin/iaxphone-gui

.PHONY: all build gui test vet lint fmt clean deb asterisk-up asterisk-down asterisk-logs

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

# Debian packages, built offline in dist/deb from an orig tarball that
# vendors the Go modules: iaxphone (CLI) and iaxphone-gui (window).
deb:
	rm -rf dist/deb && mkdir -p dist/deb
	debian/make-orig-tarball.sh dist/deb
	cd dist/deb && tar xzf iaxphone_*.orig.tar.gz && cp -r ../../debian iaxphone-*/
	cd dist/deb/iaxphone-*/ && dpkg-buildpackage -us -uc
	lintian dist/deb/*.changes || true

# Test Asterisk (chan_iax2) on udp/4569, see docker/asterisk/.
asterisk-up:
	docker compose -f docker/asterisk/compose.yml up -d --build

asterisk-down:
	docker compose -f docker/asterisk/compose.yml down

asterisk-logs:
	docker compose -f docker/asterisk/compose.yml logs -f
