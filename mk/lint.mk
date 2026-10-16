# Tier V lint targets (plan 01 section 7.1): fmt-check and lint.
.PHONY: fmt-check lint

export GOLANGCI_LINT_CACHE := $(CURDIR)/.cache/golangci-lint
SH_FILES := bootstrap toolbox/bin/sc-entrypoint

# fmt-check fails on any gofmt or shfmt diff and names the files.
fmt-check:
	@bad=$$(find src -name '*.go' -not -path '*/testdata/*' -print0 | xargs -0 gofmt -l); \
	if [ -n "$$bad" ]; then echo "fmt-check: gofmt differences in:" >&2; echo "$$bad" >&2; exit 1; fi
	@shfmt -d $(SH_FILES)

lint:
	@cd src && go vet ./...
	@cd src && golangci-lint run --config .golangci.yaml ./...
	@cd src && go run ./cmd/sc-lint netcheck --root ..
	@cd src && go mod verify
	@cd src && go mod tidy -diff
	@shellcheck $(SH_FILES)
	@actionlint .github/workflows/*.yaml
