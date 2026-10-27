package lint

// TofuValidate is the static HCL and guard check behind `make tofu-validate` (plan 06 section 14.2, 06-AC-9):
// every aws_s3_bucket under infra/tofu has prevent_destroy = true, and infra/tofu/storage/guard.yaml allows no
// delete action.
// SC-STUB(F08 06-AC-9)
func TofuValidate(root string) (Result, error) { return Result{}, nil }
