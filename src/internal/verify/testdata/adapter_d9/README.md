D9 adapter fixtures (F06 S1 round 2). `go_package_no_result.json`: package b has a `start` event and no final
pass/fail event (the go command died or lost the package result): the report must be BuildFailed, not clean.
Package a is complete. The test twin appends a final pass for b and expects BuildFailed false.

`jest_typeerror_first`, `jest_assertion_first`, `pytest_assertion_first` (N3): only the failure type and the first
message line decide the class (plan 22 section 14.1 step 5). A later line that looks like the other class does not.
All hand-written in the documented formats (decision D5).
