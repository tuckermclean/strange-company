# ADR-0101: Operator-side secret handling — bundle and SOPS are the only rest locations; tmpfs during runs; redaction everywhere

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 50

## Context
`sc-bootstrap` generates most black-start secrets (age identities, OpenTofu passphrase, k3s token, Forgejo admin, break-glass credentials) on the operator machine and consumes operator-supplied credentials from `bootstrap.env`. SPEC §94 and §98 forbid secrets in logs and in plaintext Git; ADR-0012 defines a passphrase-encrypted recovery bundle and two-recipient SOPS. Plaintext copies on the operator's disk (a `.bootstrap/secrets/` directory, a passphrase file) would be a second, unmanaged secret store that survives every rotation and is easy to back up by accident.

## Decision
1. Generated secrets exist at rest on the operator machine **only** inside (a) the passphrase-encrypted recovery bundle in the black-start directory and (b) SOPS-encrypted files in the repository. The single exception is the offline recovery age identity, written once to `/blackstart/<cluster>/recovery-identity.txt` for the operator to move offline (ADR-0012); preflight warns while it remains (PF-23).
2. During a run, secret material lives in the toolbox container's tmpfs `/run/sc` (mode 0700): derived files (age key file for SOPS, Ansible vars, kubeconfig copy) and subprocess environments (`TF_ENCRYPTION`, `SOPS_AGE_KEY_FILE`). The tmpfs vanishes with the container. There is no `.bootstrap/secrets/` directory; references to it in other plans mean `/run/sc/secrets/`.
3. Each invocation that needs generated secrets prompts once for the recovery passphrase and opens the bundle in memory. `SC_RECOVERY_PASSPHRASE_FILE` exists for automation (CI lab, scheduled E2E) and is warned about with paid providers (PF-24).
4. Operator-supplied credentials enter the container only as the read-only mounted `bootstrap.env` (and the SSH key file / agent socket), never as container arguments or initial environment (plan 02 02-R-3).
5. Every secret value is registered with a redactor before first use; every byte written to logs, tool logs, reports, the journal and S3 run copies passes through it (`«redacted:SC-CLASS»`).
6. Off-cluster-only secrets committed to Git (`secrets/offcluster/<cluster>/`) are encrypted to the recovery recipient only, so the in-cluster age key cannot decrypt them (plan 01 §6.5, plan 35).

## Consequences
- The operator types the passphrase on every mutating run; this doubles as a periodic passphrase test (SPEC §22 "periodically tested").
- Losing both the passphrase and the offline identity loses generated secrets; OpenTofu state can then only be rebuilt by import (plan 06 §20.4).
- Plans that staged secrets in `.bootstrap/secrets/` (16, 19, and plan 35's register entries `.bootstrap/{install}/…`) must use `/run/sc/secrets/` or the bundle.

## Alternatives rejected
- An OS keychain integration: not portable across Linux/macOS/WSL2 and invisible to black start.
- Plaintext files under `.bootstrap/` with `chmod 600`: survive rotations, leak into backups and are a second unmanaged store.
- Passing secrets as environment variables to `docker run`: visible in `docker inspect` and process listings.

## Verification
F08 gate: 50-AC-8 (redactor property test) and 50-AC-26 (E2E log scan finds no bundle value). F03 gate: 02-AC-6 (no credential in engine arguments, inspect output or initial environment). A test asserts that after a complete fake install no file under `.bootstrap/` or `/blackstart/` other than the bundle and `recovery-identity.txt` contains any generated secret value.
