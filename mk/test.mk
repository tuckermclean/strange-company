# Tier V test target (plan 01 section 7.1): go test -json, raw stream in out/test/test.json, JUnit always
# written to $(JUNIT); the exit status is go test's.
export GOCACHE := $(CURDIR)/.cache/go/build
export GOMODCACHE := $(CURDIR)/.cache/go/mod
GO_TEST_PKGS ?= ./...
GO_TEST_RUN ?=
JUNIT ?= out/test/junit.xml
GO_TEST_RACE := $(if $(filter amd64,$(shell go env GOARCH)),-race,)
# Package paths may be written relative to the repository root (./src/...); go test runs inside src/.
GO_TEST_PKGS_REL := $(patsubst ./src/%,./%,$(GO_TEST_PKGS))
# A nested verify (SC_NESTED_VERIFY=1, started by the lint tests in a throwaway clone) has .cache as a symlink to
# the shared module cache; git cannot check-ignore beneath a symlink, so the real-tree .gitignore test is
# left to the outer run, which judges the same tree.
GO_TEST_SKIP ?= $(if $(filter 1,$(SC_NESTED_VERIFY)),TestRealTree_CheckIgnore_01AC2,)
GO_TEST_SKIP_ARGS := $(if $(GO_TEST_SKIP),-skip "^($(GO_TEST_SKIP))$$",)
GO_TEST_RUN_ARGS := $(if $(GO_TEST_RUN),-run "$(GO_TEST_RUN)",)

.PHONY: test
test:
	@mkdir -p out/test "$(dir $(JUNIT))"
	@rc=0; \
	(cd src && go test -json -timeout 30m $(GO_TEST_RACE) $(GO_TEST_RUN_ARGS) $(GO_TEST_SKIP_ARGS) $(GO_TEST_PKGS_REL)) > out/test/test.json || rc=$$?; \
	(cd src && go run ./cmd/sc-lint junit) < out/test/test.json > "$(JUNIT)" || { echo "test: JUnit conversion failed" >&2; [ $$rc -ne 0 ] || rc=1; }; \
	exit $$rc
