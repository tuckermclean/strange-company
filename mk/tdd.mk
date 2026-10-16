# Bootstrap TDD discipline check (plan 52 section 8.3): re-executes every RED commit of TDD_BASE..HEAD.
# CI passes the PR's target ref, source branch and author (.github/workflows/verify.yaml).
.PHONY: tdd-discipline-check

TDD_BASE ?= origin/main
TDD_BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD)
TDD_AUTHOR ?=

export TDD_BASE TDD_BRANCH TDD_AUTHOR

tdd-discipline-check:
	@cd src && go run ./cmd/sc-lint tdd-discipline --root .. --base "$$TDD_BASE" --branch "$$TDD_BRANCH" --author "$$TDD_AUTHOR"
