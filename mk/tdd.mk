# Bootstrap TDD discipline check (plan 52 section 8.3): re-executes every RED commit of TDD_BASE..HEAD.
# CI passes the PR's target ref, source branch and author (.github/workflows/verify.yaml).
# CD-58a: when $(TDD_BASE) already has the tdd-discipline subcommand, sc-lint is built from a git worktree of
# $(TDD_BASE) (the PR cannot change the checker that judges it); otherwise (F01 bootstrap) from this tree.
.PHONY: tdd-discipline-check

TDD_BASE ?= origin/main
TDD_BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD)
TDD_AUTHOR ?=
TDD_PROVENANCE ?=

export TDD_BASE TDD_BRANCH TDD_AUTHOR TDD_PROVENANCE

tdd-discipline-check:
	@set -eu; root=$$(pwd); tmp=$$(mktemp -d); bin=$$tmp/sc-lint; wt=; \
	cleanup() { if [ -n "$$wt" ]; then git worktree remove --force "$$wt" >/dev/null 2>&1 || true; git worktree prune; fi; rm -rf "$$tmp"; }; \
	trap cleanup EXIT; \
	if git grep -q tdd-discipline "$$TDD_BASE" -- src/cmd/sc-lint/ 2>/dev/null; then \
	  wt=$$tmp/base; git worktree add --detach --quiet "$$wt" "$$TDD_BASE"; \
	  (cd "$$wt/src" && go build -o "$$bin" ./cmd/sc-lint); \
	  echo "tdd-discipline-check: checker built from $$TDD_BASE (base)"; \
	else \
	  (cd src && go build -o "$$bin" ./cmd/sc-lint); \
	  echo "tdd-discipline-check: checker built from the head tree (head)"; \
	fi; \
	rc=0; "$$bin" tdd-discipline --root "$$root" --base "$$TDD_BASE" --branch "$$TDD_BRANCH" --author "$$TDD_AUTHOR" $${TDD_PROVENANCE:+--provenance-bound} || rc=$$?; \
	exit $$rc
