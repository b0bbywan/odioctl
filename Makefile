# Dev / CI helpers. The version comes from the git tag (v-prefix stripped);
# nfpm's semver schema turns 0.1.0-beta.8 into the deb version 0.1.0~beta.8.

# A branch with no reachable tag still needs a dpkg-valid version: 0.0.0~dev.<sha>
# starts with a digit and sorts below any real release.
VERSION     ?= $(shell git describe --tags --dirty 2>/dev/null \
	|| echo "0.0.0-dev.$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)")
PKG_VERSION := $(patsubst v%,%,$(VERSION))
LDFLAGS     := -s -w -X github.com/b0bbywan/odioctl/config.AppVersion=$(PKG_VERSION)

# apt.odio.love serves Raspberry Pi OS: armhf is GOARM=6 so the same deb runs
# from the Zero to the Pi 4 in 32-bit, arm64 covers the 64-bit images.
ARCHES := amd64 armhf arm64
goarch_amd64 := GOARCH=amd64
goarch_armhf := GOARCH=arm GOARM=6
goarch_arm64 := GOARCH=arm64

.PHONY: lint test build build-all deb sudoers check-sudoers clean

lint:
	test -z "$$(gofmt -l . | tee /dev/stderr)"
	go vet ./...

test:
	go test ./...

build:
	go build -ldflags '$(LDFLAGS)' -o bin/odioctl .

build-all: $(ARCHES:%=dist/odioctl-linux-%)

dist/odioctl-linux-%: FORCE
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux $(goarch_$*) go build -ldflags '$(LDFLAGS)' -o $@ .

# One .deb per arch via nfpm (go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest).
deb: $(ARCHES:%=deb-%)

deb-%: dist/odioctl-linux-%
	VERSION='$(PKG_VERSION)' NFPM_ARCH='$*' BINARY_PATH='dist/odioctl-linux-$*' \
		sh -c 'envsubst < nfpm.yaml > dist/.nfpm-$*.yaml'
	nfpm package --config dist/.nfpm-$*.yaml --packager deb --target dist/
	rm -f dist/.nfpm-$*.yaml

# data/sudoers/odioctl lists one line per DAC id (no wildcards) — regenerate
# after touching dac.Catalog.
sudoers:
	go generate ./dac

check-sudoers:
	cd dac && go run ./gen -check

clean:
	rm -rf bin/ dist/

FORCE:
