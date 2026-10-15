package lint

// SC-STUB(F01 01-AC-7) LocksCheck evaluates the version-lock rules of plan 01 section 6.3 on the tree under root.
func LocksCheck(root string) (Result, error) {
	return Result{}, nil
}

// SC-STUB(F01 01-AC-8) SecretsScan evaluates the plaintext-secret and SOPS rules of plan 01 section 7.1 on the tree under root.
func SecretsScan(root string) (Result, error) {
	return Result{}, nil
}

// SC-STUB(F01 52-AC-7) QuarantineCheck evaluates rules Q1 and Q2 of plan 52 section 9.3 on policy/test-quarantine.yaml
// under root, using the committer date of HEAD as "today".
func QuarantineCheck(root string) (Result, error) {
	return Result{}, nil
}
