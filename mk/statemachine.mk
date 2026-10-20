# State-machine check (plan 29 section 8.2, 29-AC-1). `sc-statemachine` (Go, built from source) validates
# contracts/state-machine.yaml against its schema and every loader invariant and prints the per-mode
# reachability report. STATE_MACHINE_TABLE=<file> checks another table.
.PHONY: state-machine-check

STATE_MACHINE_TABLE ?=

state-machine-check:
	@cd src && go run ./cmd/sc-statemachine check --contracts ../contracts$(if $(STATE_MACHINE_TABLE), --table "$(abspath $(STATE_MACHINE_TABLE))",)

# State-machine coverage (plan 29 section 22, 29-AC-3): runs the transition-table fixtures and prints
# "state-machine coverage: transitions N/94 guards M/66"; the test fails (non-zero exit) below 100%. The target
# is a subset of `make test` (which runs the same test), so it is not part of `make verify`.
.PHONY: state-machine-coverage

state-machine-coverage:
	@cd src && go test -count=1 ./internal/statemachine -run '^TestTransitionCoverage_29AC3$$' -v

# Library trajectories (plan 21 section 22.3, 21-AC-20): replays every test/e2e/tdd/*.yaml against
# contracts/state-machine.yaml with the table-driven fake facts and validates each serialized task record against
# contracts/task.schema.yaml. The tests are the TestTrajectories_* tests of ./internal/statemachine, a subset of
# `make test`, so the target is not part of `make verify`.
.PHONY: test-tdd-library

test-tdd-library:
	@cd src && go test -count=1 ./internal/statemachine -run '^TestTrajectories_' -v
