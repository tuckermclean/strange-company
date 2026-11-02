# Existing-nodes fixtures (05-AC-1)

source: hand-written from the `EXISTING_NODES` / `EXISTING_NODE_OVERRIDES` grammar of plan 05 section 9 and plan 50
section 9.2 (documentation addresses, invented names).

- `env/<case>.env` is merged over a minimal valid existing-provider install; `env/<case>.json` is the expected
  `spec.provider.existing` of the normalized BootstrapConfig.
- `env-invalid/<case>.env` and `env-duplicates/<case>.env` must fail normalization with a configuration error
  (exit 2); every `# want:` comment line names a substring the error message must contain (the field and the
  offending entry). `env-duplicates/` holds the cases for the duplicate-name rule.
