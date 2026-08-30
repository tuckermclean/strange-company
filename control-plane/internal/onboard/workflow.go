package onboard

// DefaultWorkflow is the CI this proposes when a repository has none.
//
// Deliberately minimal and deliberately unopinionated about the toolchain: it
// runs whatever the repository already calls a test. The gates read GitHub's
// check runs, so what matters is only that SOMETHING reports a conclusion on
// an agent/** branch -- §11.3's red gate compares the base ref against the
// candidate, and with no checks it has nothing to compare and every card
// stalls.
//
// It also runs on the default branch, because the red gate needs a baseline: a
// suite that was already failing before the agent touched anything is not
// evidence of anything the agent did.
const DefaultWorkflow = `name: tests

# The autonomous engine's gates read this workflow's check runs. Without a run
# on agent/** there is nothing for them to read, and every card stalls at its
# red gate having done the work correctly.
on:
  push:
    branches:
      - main
      - master
      - 'agent/**'
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      # Replace this with however this repository runs its tests. What the
      # gates need is a check run that concludes; what it runs is yours.
      - name: test
        run: |
          if [ -f package.json ]; then
            node --test
          elif [ -f go.mod ]; then
            go test ./...
          elif [ -f pyproject.toml ] || [ -f setup.py ]; then
            python -m pytest
          else
            echo "No test command detected. Edit ${GITHUB_WORKFLOW} before merging."
            exit 1
          fi
`

const pullRequestBody = `Day-0 setup for the autonomous engine.

This adds a workflow that runs the test suite on ` + "`agent/**`" + ` branches.

**Why it is needed.** The engine's red gate proves acceptance tests fail before
the implementation and pass after, by reading this repository's own check runs.
With no run on an agent branch there is nothing to read: cards do the work
correctly and then stall, which looks like the engine failing when it is the
engine refusing to claim something it cannot verify.

**Check the test command before merging.** The detection here is a guess based
on which manifest is present. What the gates need is a check run that reaches a
conclusion; what it runs is entirely yours.

This pull request exists rather than a direct push because a human is the final
merge authority for every change, and a change to the checks themselves is the
last place to make an exception.
`
