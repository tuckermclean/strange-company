# ADR-0352: Missing symbols are handled by declared SC-STUB stubs; compile and import failures are never valid RED

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plans:** 22, 21

## Context
SPEC §50: "A test that merely fails to compile is not automatically useful RED evidence." In statically typed languages a test that calls a function that does not exist yet fails to compile; in dynamic languages it fails with `NameError`/`AttributeError`/`ImportError`. Such failures prove only that a name is missing, not that behavior is absent, and they are indistinguishable from broken tests. Yet the tester must not write the implementation (SPEC §48, §49).

## Decision
- The tester may add **declared stubs** in `stubPaths` (repository policy): additions only, each preceded by the marker comment `SC-STUB(<task-id> AC-n…)`, body ≤ 12 lines, returning **zero values** (`return "", nil`, `None`, `null`, `Default::default()`), or, only for languages in `allowPanicStubsFor` (default: Rust), panicking with `SC-STUB-NOT-IMPLEMENTED`.
- The Runner checks stub shape from the Git diff (Go by AST, other languages by per-language rules). Malformed stubs make RED invalid.
- With zero-value stubs the test reaches its assertion, so RED is an assertion-class failure. Compile, collection, import and missing-symbol failures are always error-class (`TEST_NEEDS_WORK`).
- Stubs are listed in the test manifest. The implementer must replace every one (GREEN fails with `stub_not_replaced` while a marker remains). Stubs are not protected files.

## Consequences
Testers in typed languages write a few lines outside test paths, bounded and auditable. A zero-value stub could accidentally satisfy a test (e.g. expecting an empty string); that shows up as `already_satisfied`/`partially_satisfied` and is investigated, never manufactured.

## Alternatives rejected
- Accepting compile failures as RED: contradicts SPEC §50 and hides broken tests.
- Letting testers write interfaces plus fakes only: does not work for free functions and changes production structure.
- Panic stubs everywhere: a panic is an error-class failure in most frameworks and would weaken R7.

## Verification
22-AC-4 (compile-only invalid; zero-value stub valid; panic stub valid only for Rust), plan 21 trajectory `red-compile-error-then-stub`.
