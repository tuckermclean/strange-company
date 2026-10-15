# ADR-0205: SeaweedFS is the S3 emulator for CI and lab tests

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 14

## Context
Deterministic CI (SPEC §104) must exercise OpenTofu locking, CNPG WAL archiving and PITR, Longhorn backups (lab), the Flux `Bucket` seed, repository bundles and the object-storage capability suite without a paid provider. The emulator must support conditional writes (`If-None-Match: *` → 412), object versioning and Object Lock. MinIO, the usual choice, changed its community distribution in 2025 (community binaries and images discontinued and the console reduced; verify the current state), so a pinned community image would stop receiving fixes. Garage has no object versioning or Object Lock (verify). SeaweedFS documents atomic conditional writes, versioning and Object Lock (verify per pinned release).

## Decision
- CI and lab tests use **SeaweedFS** (`weed server -s3 -s3.port=9000`, digest-pinned) as `s3-emulator:9000` on the k3d Docker network and as a service container for integration tests.
- Version: baseline SeaweedFS 4.x (`plans/compatibility-baseline.yaml`, CD-29); each bump must pass `make test-s3-emulator` (conditional create with 8 concurrent writers, versioning round-trip, COMPLIANCE lock blocking `DeleteObjectVersion`, lifecycle round-trip, multipart).
- Fallback: versitygw (POSIX backend) if a SeaweedFS release fails the suite; the suite decides, not this ADR.
- Provider IAM behavior is not emulated; negative permission tests run only against real providers.

## Consequences
- Plans 06, 16, 19, 49, 50 and 52 use the shared emulator (CD-12).
- Emulator quirks can hide provider differences; the live capability suite at P01 remains authoritative.

## Alternatives rejected
- MinIO community edition: distribution model changed; pinned old images age without fixes.
- Garage: lacks versioning and Object Lock.
- LocalStack: heavier, and its S3 conditional-write and lock fidelity is not a project goal.

## Verification
Plan 14 14-AC-1 and 14-AC-2; plan 12 12-AC-12 (`make e2e-cnpg` against the emulator); plan 44 44-AC-14 (`make e2e-dr`).
