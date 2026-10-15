# Procedure: Onboard an existing GitHub repository as a project repository

**Owning plan:** plans/16-forgejo.md (§14.5; mirror: plans/17-github-mirror.md §14.8; templates and verification: plans/22-verification-policy.md §9.7)
**SPEC references:** §5 (one primary repository is the platform itself), §30, §31, §108, §128 (single-repository tasks)
**Severity / urgency:** routine
**Expected duration:** 1-3 h of operator time per repository, plus the import (depends on repository size) and review waits
**Requires break-glass:** no (a Forgejo admin account for `forge apply`, CD-38)

## When to use
- The operator wants Strange Company agents to work on a repository that currently lives on GitHub (decision CD-43: fully migrated into Forgejo org `projects`; Forgejo is primary; an optional push mirror keeps a GitHub copy). One run per repository.
- Not for the platform repository (`strange-company`, plan 17) and not for a repository that already exists in Forgejo (then start at step 5).

## Preconditions and required material
- `./bootstrap shell`; the toolbox clone of `strange-company` at `origin/main`; your own Forgejo admin identity (OIDC user in `sc-admins` with a PAT `write:admin,write:organization,write:repository`).
- A read-only GitHub token for the source repository (`SC-GITHUB-IMPORT`: fine-grained PAT with Contents, Issues, Pull requests, Metadata read), expiry <= 7 days. Never put it in a file in the repository, `bootstrap.env` or the cluster.
- If a push mirror is wanted: a fine-grained PAT `SC-GITHUB-PROJECT-MIRROR` over the selected GitHub repositories (Contents R/W, Metadata R), **not** the `SC-GITHUB-MIRROR` token.
- The language of the repository (Python, Rust or TypeScript/JavaScript for the starting templates), a lockfile committed in it, and the digest of the chosen toolchain image (`crane digest docker.io/library/<image>:<tag>`).
- Org `projects` exists in `forge/forge.yaml` (plan 16) and Forgejo is healthy (`sc-bootstrap forge verify`).

## Safety checks before acting
1. Freeze the GitHub repository: tell collaborators to stop pushing, merge or close open PRs you do not want to migrate. Anything pushed to GitHub after the import is not carried over.
2. The import creates the repository **only if absent** (plan 16 16-R-7). Never delete and re-import a repository that already has Forgejo-side work without a bundle restore plan.
3. `.github/workflows/**` stays in the history but no longer runs (Forgejo Actions is disabled). Do not rely on GitHub Actions for this repository after step 7.
4. The agent never sees GitHub credentials; the import and mirror tokens are entered by you only.

## Steps
1. **forge.yaml PR.** Add the `projects/<name>` element to `forge/forge.yaml`: `defaultBranch`, `private: true`, the `import` block (`source`, `items`: git always, then issues, pullRequests, labels, milestones, releases, wiki, lfs as wanted; `credential: {class: SC-GITHUB-IMPORT, source: operator-invocation}`), project branch protection (the plan 16 §7.4 project shape: human-only `sc-mergers` whitelists, required contexts per plan 52 §8.2), and optionally `pushMirrors` (`branchFilter: "<default branch>,release/*"`, class `SC-GITHUB-PROJECT-MIRROR`). `make contracts policy-check` passes locally; a human approves and merges the PR. Expected: merged; nothing exists in Forgejo yet.
2. **Apply.** `./bootstrap exec -- sc-bootstrap forge apply --dry-run` shows one repository create via the migrate API plus protection (and the mirror). Then pass the import token on stdin and run it for real: `./bootstrap exec -- sh -c 'read -rs T; printf %s "$T" | sc-bootstrap forge apply --github-import-token-file /dev/stdin'`. For a mirror, apply also prompts for the `SC-GITHUB-PROJECT-MIRROR` token. Expected: the repository exists, protection is applied after the import, exit 0. On failure the half-created repository is removed; re-run. Revoke the import token in GitHub when done.
3. **Verify the import.** Compare with GitHub: default-branch head SHA (`git ls-remote https://github.com/<owner>/<repo>.git HEAD` equals `$FJ GET /repos/projects/<name>/branches/<default>` `.commit.id`), the number of branches and tags (`git ls-remote --heads --tags` counts on both sides), and, for each item you enabled, the issue, PR, label, milestone and release counts. Expected: SHAs equal, ref counts equal; record documented deviations (placeholder authors, items the migrate API skipped; Q-16-12). If PRs or issues are unusable, re-import is not possible (the repository now exists): migrate them manually or keep them readable on GitHub.
4. **Mirror or archive decision (can be done now or after step 8).** Decide whether GitHub stays as a mirror (step 9a) or is archived (9b).
5. **Onboarding PR (human non-task PR).** The Runner refuses a repository without `.strange-company/repo.yaml`, so this PR is a normal human PR, not a task (plan 55 non-task merge path; Woodpecker `ci/woodpecker/pr/ci` and human approval apply). Branch `onboard/strange-company` from the default branch. Copy `templates/project-onboarding/<language>/` into the repository and edit: `.strange-company/repo.yaml` (languages, buildCommands, importantPaths), `validation.yaml` (set `spec.verification.image` to the digest-pinned image; adjust the suite and checks to the real test layout), `policy.yaml` (`testPaths`, `fixturePaths`, `protectedPaths` incl. `.strange-company/**`, dependency manifests and lockfiles, `.woodpecker/**`; `protectedBranches`), `.strange-company/run-tests.sh` (lockfile-based dependency install through the egress proxy, then the test command with a JUnit report). Port the GitHub Actions workflows to the validation suite and, if you want pull-request CI, an optional `.woodpecker/ci.yaml` (the template already contains one that runs the same wrapper); keep `.github/workflows` as history only. If the language image is not yet allowed, first land a platform PR adding its prefix to `policy/ci.yaml` `imageRegistries`. Expected: PR merged by a `sc-mergers` member.
6. **Repository without a usable test suite.** Add a minimal characterization suite in this same PR if feasible. If not, continue with step 7 using a `validation.yaml` that reports zero tests, and make the first task a verification-first task whose only aim is to establish the suite (plan 22 §9.7, open question 8).
7. **Baseline check.** The merge of step 5 triggers the repository's `ci` pipeline on the default branch (`.woodpecker/ci.yaml` from the template, which runs the same wrapper in the same digest-pinned image; trusted CI, plan 20). Expected: green, and its JUnit report (`out/sc-verify/junit.xml` or the language path) shows at least one `passed` test; the operator also runs the wrapper once locally in the toolbox with the same image as an advisory comparison. The authoritative base run is the Runner's RED run of the first task, so a base that is red or empty here would make every task `red_broken_baseline`: if so, fix the suite or wrapper by a further PR and do not proceed. Nothing may have needed hosts outside the `ci-base` egress profile (check the `ci` listener log of `egress-proxy`).
8. **Managed-repository PR.** PR adding `projects/<name>` to `forgejo.managedRepositories` in the cluster's Runner config values (plan 26 §9.1; plan 54 `runner.config`) and, if not already present, the org-level webhook covers it. `make policy-check` enforces that the entry exists in `forge/forge.yaml` (`MANAGED_REPOSITORY_NOT_IN_FORGE`). After merge and Flux reconcile: `agent_list` through Hermes accepts the repository and a throwaway `research` task against it does not return `REPOSITORY_NOT_MANAGED`.
9. **GitHub side.**
   - **9a Mirror kept:** confirm `$FJ GET /repos/projects/<name>/push_mirrors` shows one entry, `last_error == ""`. On GitHub set the repository to read-only for humans (plan 17 §14.1.2 / 17-R-3: ruleset allowing only the mirror identity to push, issues and wiki disabled if they were imported, Actions disabled), replace the README with a "mirror of git.<domain>" note. Expected: a test commit to the Forgejo default branch appears on GitHub within 2 minutes; `agent/*` branches never appear (17-AC-18).
   - **9b Archived:** archive the GitHub repository (Settings, Archive) and remove the push-mirror block from `forge.yaml` in a PR if it was declared. No GitHub credential remains.
10. **First backup.** Within 90 minutes confirm the first repository bundle: `aws --endpoint-url "$S3_ENDPOINT" s3 ls "s3://${S3_BUCKET_PREFIX}-repo-backups/git/weekly/projects/<name>/"` lists a chain with `manifest-0000.json` whose refs equal Forgejo's (44-AC-29), and `SCRepoBundleBackupStale` is quiet. Optionally dry-run `restore-project-repository.md` steps 1-6.

## Verification
- `forge verify` reports `projects/<name>` matching `forge.yaml` (protection, mirror); `SCForgeConfigDrift` is quiet.
- Step 3 comparison recorded; step 7 baseline passed; step 8 accepted by the Runner; step 10 manifest present.
- No token appears in Git history, `kubectl get secrets -A`, the Forgejo repository settings other than the encrypted push-mirror password, or logs.
- The first genuine test-first task through Hermes is the S01 gate (22-AC-37), not part of this procedure.

## Rollback / abort
- Before step 2: close the PR; nothing changed.
- After step 2, before step 5: delete the Forgejo repository (`DELETE /repos/projects/<name>` as admin) and revert the `forge.yaml` element by PR; GitHub is untouched. After step 5: leave the repository, but remove it from `managedRepositories` (no task can start).
- Mirror problems: remove the `pushMirrors` entry by PR and `forge apply --prune-mirrors`; revoke `SC-GITHUB-PROJECT-MIRROR` in GitHub.
- A wrongly imported repository with Forgejo-side commits: restore with `restore-project-repository.md` rather than re-import.

## Evidence to record
- Source URL and head SHA, ref and item counts on both sides, import duration, deviations; the baseline JUnit summary; the commit SHA of the onboarding PR and the `managedRepositories` PR; mirror or archive decision; first bundle `created_at`.

## Follow-ups
- Rotate `SC-GITHUB-PROJECT-MIRROR` at <= 180 days (`rotate-forge-credentials.md` §G.8); revoke `SC-GITHUB-IMPORT` after step 2.
- Run the first task test-first through Hermes; tell collaborators the GitHub repository is read-only or archived.
- `SCProjectMirrorFailing` runbook: `github-mirror-failure.md`.
