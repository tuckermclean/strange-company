# Adapter fixtures (F06, plan 22 section 22)

Each directory holds the adapter name (`adapter`), one tool report (`report.json` for `go test -json`,
`report.xml` for the JUnit-based adapters) and the normalized statuses the adapter must produce
(`expect.json`, suite name `unit`).

Only Go runs for real in the `ci` image. The Go reports follow the test2json event format; the pytest (xunit2),
jest-junit (`addFileAttribute`), vitest and cargo-nextest reports are hand-written in the documented formats of
those tools, because the tools are not installed there (decision D5 of the F06 brief). Re-recording them with the
real tools against recorded fixture repositories is 22-AC-12 (node C04). The cases cover assertion failure,
panic or exception, collection/compile error, timeout, skip, subtests or parametrization, missing symbol, stub
panic, a failure that matches neither the assertion nor the error list (fail closed), and unusable reports.
