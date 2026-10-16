# Architecture map

The canonical specification is `SPEC.md`. Design lives in `plans/` (start at
`plans/00-master-plan.md`, the dependency graph is `plans/dag.yaml`) and the decision records in `adr/`.
Operator procedures are in `procedures/`. The repository layout is defined in `plans/01-repository.md`
section 6.1; the Go module is `src/` (`src/cmd/*` binaries, `src/internal/*` libraries).
