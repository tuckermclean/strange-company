# Procedure: SOPS decryption failure

**Owning plan:** plans/35-secrets.md (Flux side: plan 18 `flux-reconciliation-failure.md` §C)
**SPEC references:** §98, §111, §33
**Severity / urgency:** degraded (one layer stuck; running workloads keep their Secrets) · outage if `sops-age` is gone and several layers are blocked during an upgrade or recovery
**Expected duration:** 10–45 min
**Requires break-glass:** yes — `SC-K3S-ADMIN-KUBECONFIG` via `./bootstrap shell` (the `sops-age` Secret is out-of-band); recovery passphrase

## When to use

- `SCSopsDecryptionFailing`, or `SCFluxReconciliationFailing` whose Kustomization condition message contains `DecryptionFailed`, `failed to decrypt`, `no identity matched` or `sops metadata not found`.
- `kubectl get events -A --field-selector reason=DecryptionFailed` is non-empty.
- `./bootstrap verify --only V-34` fails.

## Preconditions and required material

Toolbox, admin kubeconfig, recovery passphrase (to read the cluster key from the bundle), and, for case D, the offline recovery identity.

## Safety checks before acting

1. **Do not** create or edit Kubernetes Secrets by hand to "unstick" a layer: Flux drift detection reverts them and the root cause stays.
2. **Do not** commit a decrypted file or remove the `sops:` block to make Flux apply it. `make secrets-lint` would block the PR anyway.
3. **Never print** the cluster identity; pipe it only into `kubectl create secret … --from-file` inside the toolbox tmpfs (`/run/sc`).
4. Identify the scope first: one file, one layer, or every secret-bearing layer.

## Steps

1. **Classify.** In `./bootstrap shell`:
   `flux get kustomizations -A --status-selector ready=false` → note each failing Kustomization and its message.
   `kubectl -n flux-system get secret sops-age -o jsonpath='{.data.age\.agekey}' | base64 -d | grep -c AGE-SECRET-KEY` (count only).
   - Message names one file and the count is ≥ 1 → **A** or **B**.
   - Every secret-bearing layer fails and the Secret is missing or the count is 0 → **C**.
   - Every layer fails with "no identity matched" and the count is ≥ 1 → **D** (the installed identity is not a recipient).

### A. A file encrypted to the wrong recipients (typical after a manual edit)

2. `./bootstrap exec -- make secrets-lint` on the commit Flux is applying (`git checkout <lastAttemptedRevision>`). *Expected:* S-3 names the file and its actual recipients.
3. Re-encrypt it correctly from a machine that can decrypt it: `./bootstrap exec -- sc-bootstrap secrets reencrypt <path>` (opens the bundle; uses the cluster key, or prompts for the recovery identity if the file is not encrypted to the cluster key) → PR → merge.
4. *Expected:* the Kustomization becomes Ready within its interval (`flux reconcile kustomization <name> --with-source` to hurry).

### B. A file that is not valid SOPS (truncated, merge conflict markers, empty value)

5. `sops --decrypt <path>` in the toolbox fails with a parse/MAC error, or `secrets-lint` S-2/S-4 names it.
6. Restore the last good version from Git history (`git log -- <path>`; `git show <good>:<path> > <path>`), or regenerate the value if the class allows (`rotate-secrets.md` class section) → PR → merge.

### C. `sops-age` missing or emptied

7. Re-create it from the bundle: `./bootstrap exec -- sc-bootstrap sops-age install` (opens the bundle, writes `flux-system/sops-age` with label `strange.company/out-of-band=true`, key `age.agekey`). *Expected:* `secret/sops-age created` (or `configured`).
8. `flux reconcile kustomization flux-system --with-source`; within 10 min every Kustomization Ready.
9. Treat the disappearance as a security event until explained: check the audit log for `delete`/`update` on `secrets/sops-age` (`./bootstrap exec -- sc-bootstrap evidence collect --since <time> --resource secrets/sops-age`). Unexplained → `credential-compromise.md#recovery-material` (the deleter may have read it first).

### D. Installed identity is not a recipient (cluster-key rotation mismatch)

10. Compare: the public key of the installed identity (`sc-bootstrap sops-age fingerprint`, prints `age1…` only) vs. the cluster recipient in `.sops.yaml` and in `recovery-bundle status`.
11. If the bundle holds the identity that matches `.sops.yaml` (rotation step 2 of `rotate-age-key.md#cluster-key` was skipped or reverted): run step 7 (installs the bundle's current identity, plus the previous one if the bundle lists it).
12. If neither the installed nor the bundled identity matches the files (files were rewrapped to a key that exists nowhere): decrypt with the **offline recovery identity** and rewrap to the bundled cluster key: `./bootstrap exec -- sc-bootstrap secrets reencrypt --all --with-recovery-identity` → PR → merge. Then run `rotate-age-key.md#attest`.

## Verification

- `flux get kustomizations -A` all Ready at the current `main` SHA.
- `kubectl get events -A --field-selector reason=DecryptionFailed` empty for 15 min.
- `./bootstrap exec -- make secrets-lint` passes on `main`; `./bootstrap verify --only V-34` passes.
- `SCSopsDecryptionFailing` resolved.

## Rollback / abort

- A PR that made things worse: revert it; Flux re-applies the previous ciphertexts.
- Step 7 is idempotent and only adds identities; to remove a wrongly installed identity re-run `sc-bootstrap sops-age install --exact` (installs only the bundle's current cluster identity).

## Evidence to record

Failing Kustomizations and messages, the case (A–D), commits involved, `sops-age` identity count before/after (counts and public keys only), audit-log excerpt for case C, PR URLs.

## Follow-ups

- Case A/B: add a negative fixture to `secrets-lint` if the defect slipped past CI.
- Case C/D: review who can write `flux-system` Secrets (plan 37 `sc-namespace-guard`, RBAC) and whether a rotation procedure step was skipped.
