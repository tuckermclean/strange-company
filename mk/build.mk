# Tier V build (plan 01 sections 6.2 and 7.1): every src/cmd/* binary into out/bin/linux_<arch>/.
# Reproducible: no timestamps; Version is `git describe`, Commit the commit SHA (01-AC-9).
.PHONY: build

GOARCH_BUILD := $(shell go env GOARCH)
BUILD_VERSION := $(shell git describe --tags --always --abbrev=12 2>/dev/null || echo unknown)
BUILD_COMMIT := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
VERSION_PKG := strange.company/sc/internal/version

build:
	@mkdir -p out/bin/linux_$(GOARCH_BUILD)
	@cd src && CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false \
		-ldflags "-s -w -X $(VERSION_PKG).Version=$(BUILD_VERSION) -X $(VERSION_PKG).Commit=$(BUILD_COMMIT)" \
		-o ../out/bin/linux_$(GOARCH_BUILD)/ ./cmd/...
